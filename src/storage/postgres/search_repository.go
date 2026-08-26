package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// ErrRetrievalDepth reports a page that lies past what the retrievers reached.
//
// A sentinel, because the caller has to tell this apart from a query that
// legitimately ran out of results: the fused list holds at most
// MaxCandidatesPerMode ids, so an offset beyond it can only slice past the end
// — while Total correctly reports thousands, because the count query has no
// cap. Answering with the empty slice would give a caller walking pages a full
// page 25 and an empty page 26 indistinguishable from the end of the results.
var ErrRetrievalDepth = errors.New("the requested page is past the retrieval depth")

// SearchRepository is the read half of the PostgreSQL adapter: the modes, the
// fusion over them, and the hydration of what the fusion decided.
type SearchRepository struct {
	retrievers map[domain.Capability]domain.Retriever
	hydrator   domain.Hydrator
	search     config.Search

	// semantic records whether this deployment has a query-side embedder at
	// all. Kept as its own field rather than derived from the retriever map, so
	// that Capabilities answers the same question the map was built from.
	semantic bool
}

var _ domain.SearchRepository = (*SearchRepository)(nil)

// NewSearchRepository builds the read repository over a pool.
//
// The embedder is NIL when this deployment has no semantic mode, and that is
// the whole signal: Phase 1 defaults to EMBEDDING_PROVIDER=noop (A5), whose
// Embed returns nothing on every call, so a repository handed it would declare
// a capability, run a query that can only return zero rows, and report nothing
// wrong. Nil is the composition root saying the mode is absent, which is what
// Capabilities then tells the negotiation, which is what puts the mode in
// X-Beckn-Degraded instead of silently dropping it (C11).
//
// It takes config.Search rather than reading the environment, for the same
// reason NewCatalogRepository takes a resolution: a second copy of a setting
// one layer below the composition root that owns it is a second thing to keep
// true.
func NewSearchRepository(
	pool *pgxpool.Pool, search config.Search, embedder embeddings.Embedder,
) *SearchRepository {
	repository := &SearchRepository{
		retrievers: map[domain.Capability]domain.Retriever{
			domain.CapabilityLexical: NewLexicalRetriever(pool, search.MaxCandidatesPerMode),
			domain.CapabilityFuzzy:   NewFuzzyRetriever(pool, search.MaxCandidatesPerMode),
		},
		hydrator: NewHydrator(pool, embedder),
		search:   search,
		semantic: embedder != nil,
	}
	if embedder != nil {
		repository.retrievers[domain.CapabilitySemantic] =
			NewSemanticRetriever(pool, embedder, search.MaxCandidatesPerMode)
	}
	return repository
}

// Capabilities declares what this backend can answer.
//
// Spatial is unconditional and is NOT a ranked mode: it is part of the WHERE
// clause every retriever and the counter share, so a backend that has any
// retriever at all can answer it. JSONPath is absent until the attribute filter
// lands (Task 22) — Postgres could run one, but this adapter does not yet bind
// it, and a capability declared before the predicate exists would narrow
// nothing while telling the negotiation it had.
func (s *SearchRepository) Capabilities() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical:  true,
		domain.CapabilityFuzzy:    true,
		domain.CapabilitySemantic: s.semantic,
		domain.CapabilitySpatial:  true,
	}
}

// outcome is one mode's answer. Collected per mode rather than merged as they
// arrive, because `capped` and `err` are per-mode facts the count guards read
// individually.
type outcome struct {
	mode   domain.Capability
	ids    []string
	capped bool
	err    error
}

// Search runs the enabled modes concurrently under one deadline, fuses them and
// hydrates the page.
func (s *SearchRepository) Search(
	ctx context.Context, query domain.SearchQuery, modes []domain.Capability,
) (domain.SearchResult, error) {
	// The cap is also the reachable pagination depth. Checked BEFORE anything
	// runs: a request that cannot be answered should not spend three queries
	// discovering it, and the boundary is named so the caller can tell this
	// from the end of the results.
	if query.Offset+query.Limit > s.search.MaxCandidatesPerMode {
		return domain.SearchResult{}, fmt.Errorf(
			"%w: offset %d plus limit %d passes the %d ids a mode retrieves",
			ErrRetrievalDepth, query.Offset, query.Limit, s.search.MaxCandidatesPerMode)
	}

	// One deadline for the whole fan-out (A2/A3), not one per mode: three modes
	// each allowed the full budget make a request that takes three times as
	// long as the budget says it may.
	ctx, cancel := context.WithTimeout(ctx, s.search.ReadDeadline)
	defer cancel()

	// A6: the instant is captured ONCE, here, so that every mode agrees on
	// "now". Postgres ignores it and calls now(); it exists for the backends
	// that have no now(), and capturing it here is what makes the two backends
	// answerable by the same conformance fixture.
	scope := domain.Scope{NetworkID: query.NetworkID, Now: time.Now().UTC()}

	outcomes := s.retrieve(ctx, query, modes, scope)

	ranked, degraded, anyCap := fold(outcomes)

	fused := RRF(ranked...)
	page := pageOf(fused, query.Offset, query.Limit)

	total, err := s.total(ctx, query, scope, fused, page, len(degraded) > 0, anyCap)
	if err != nil {
		return domain.SearchResult{}, err
	}

	catalogs, err := s.hydrator.Hydrate(ctx, page, scope)
	if err != nil {
		return domain.SearchResult{}, err
	}
	return domain.SearchResult{Catalogs: catalogs, Total: total, Degraded: degraded}, nil
}

// fold splits the per-mode outcomes into the lists to fuse, the modes to report
// and whether any of them stopped at its cap.
//
// A failed mode is RECORDED, not fatal. Two modes returning is a better answer
// than none — and the caller is TOLD, through X-Beckn-Degraded, which is the
// difference between a degraded answer and a wrong one.
func fold(outcomes []outcome) (ranked [][]string, degraded []string, anyCapped bool) {
	for _, result := range outcomes {
		if result.err != nil {
			degraded = append(degraded, string(result.mode))
			continue
		}
		ranked = append(ranked, result.ids)
		anyCapped = anyCapped || result.capped
	}
	return ranked, degraded, anyCapped
}

// retrieve fans the enabled modes out and waits for all of them.
//
// A barrier and not a race: the fusion needs every list, and a mode that is
// still running when its siblings finish has not failed. The shared deadline is
// what bounds the wait.
func (s *SearchRepository) retrieve(
	ctx context.Context, query domain.SearchQuery, modes []domain.Capability, scope domain.Scope,
) []outcome {
	outcomes := make([]outcome, 0, len(modes))
	for _, mode := range modes {
		if _, enabled := s.retrievers[mode]; !enabled {
			// Asked for and not available. The negotiation in front of Search
			// is supposed to have removed it, so reaching here means the two
			// disagreed — reported rather than ignored, because an ignored mode
			// is one the caller believes ran.
			outcomes = append(outcomes, outcome{mode: mode, err: fmt.Errorf(
				"the %s mode is not available on this backend", mode)})
			continue
		}
		outcomes = append(outcomes, outcome{mode: mode})
	}

	var waiting sync.WaitGroup
	for index := range outcomes {
		if outcomes[index].err != nil {
			continue
		}
		waiting.Add(1)
		go func(slot *outcome) {
			defer waiting.Done()
			// Each goroutine writes only its OWN element of a slice sized
			// before any of them started, so there is no shared write and no
			// mutex. A channel here would buy nothing: the barrier below
			// already waits for all of them.
			slot.ids, slot.err = s.retrievers[slot.mode].Retrieve(ctx, query, scope)
			slot.capped = len(slot.ids) == s.search.MaxCandidatesPerMode
		}(&outcomes[index])
	}
	waiting.Wait()
	return outcomes
}

// total answers the count, skipping the query when the fused list already IS
// the count.
//
// All four guards are load-bearing, and each of them alone would under-report:
//   - offset > 0 means the caller is past page 1, so nothing here can say how
//     much sits behind them.
//   - a full page means there may be more; only a short one proves the end.
//   - a degraded mode makes the list short because a retriever died.
//   - a capped mode makes it short because a retriever stopped early.
//
// When none of them fires, every id in the union is in `fused`, so len(fused)
// IS the count and issuing the query would spend milliseconds re-deriving a
// number already in hand.
func (s *SearchRepository) total(
	ctx context.Context, query domain.SearchQuery, scope domain.Scope,
	fused, page []string, degraded, capped bool,
) (int, error) {
	if query.Offset == 0 && len(page) < query.Limit && !degraded && !capped {
		return len(fused), nil
	}
	return s.hydrator.Count(ctx, query, scope)
}

// pageOf slices the fused list, clamping rather than panicking.
//
// The past-the-depth case is already a refusal above; this handles the ordinary
// short tail, where offset is inside the cap but past what actually matched.
func pageOf(fused []string, offset, limit int) []string {
	if offset >= len(fused) {
		return nil
	}
	end := offset + limit
	if end > len(fused) {
		end = len(fused)
	}
	return fused[offset:end]
}
