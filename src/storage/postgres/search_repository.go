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
// MaxCandidatesPerMode ids, so an offset beyond it can only slice past the end.
// Answering with the empty slice would give a caller walking pages a full page
// 25 and an empty page 26 indistinguishable from the end of the results — and
// since A19 removed the count, this refusal is the ONLY thing that tells them
// apart.
var ErrRetrievalDepth = errors.New("the requested page is past the retrieval depth")

// SearchRepository is the read half of the PostgreSQL adapter: the modes, the
// fusion over them, and the hydration of what the fusion decided.
type SearchRepository struct {
	retrievers map[domain.Capability]domain.Retriever
	hydrator   domain.Hydrator
	search     config.Search

	// candidates answers a query that names a filter but no ranked mode — a
	// geo-only or filter-only intent — with the rows the shared predicate
	// admits, in the stable (catalog_id, id) order the memory backend sorts by.
	//
	// It is the LEXICAL retriever, and that is not a shortcut: LexicalCandidates
	// reads a NULL query_text as "every row the shared predicates admit" and its
	// ORDER BY then falls through to the stable key, which is exactly this list.
	// A fourth query would be a second copy of a WHERE clause that is already
	// repeated three times, and the copy that drifts is the one nothing ranks.
	candidates domain.Retriever

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
	lexical := NewLexicalRetriever(pool, search.MaxCandidatesPerMode)
	repository := &SearchRepository{
		retrievers: map[domain.Capability]domain.Retriever{
			domain.CapabilityLexical: lexical,
			domain.CapabilityFuzzy:   NewFuzzyRetriever(pool, search.MaxCandidatesPerMode),
		},
		hydrator:   NewHydrator(pool, embedder),
		search:     search,
		semantic:   embedder != nil,
		candidates: lexical,
	}
	if embedder != nil {
		repository.retrievers[domain.CapabilitySemantic] =
			NewSemanticRetriever(pool, embedder, search.MaxCandidatesPerMode)
	}
	return repository
}

// Capabilities declares what this backend can answer.
//
// Neither Spatial nor JSONPath is a ranked mode: both are part of the WHERE
// clause every retriever shares, so a backend that has any retriever at all can
// answer them. Both are unconditional here — the geometry predicate needs only
// the H3 columns and the filter predicate only `resources.filter_doc`, and
// every publish through this adapter writes both.
//
// The memory backend declares JSONPath false and is right to: it holds the
// documents but not PostgreSQL's jsonpath engine, and the whole point of the
// capability is that the negotiation learns which of the two it is talking to
// BEFORE a filter silently narrows nothing.
func (s *SearchRepository) Capabilities() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical:  true,
		domain.CapabilityFuzzy:    true,
		domain.CapabilitySemantic: s.semantic,
		domain.CapabilitySpatial:  true,
		domain.CapabilityJSONPath: true,
	}
}

// outcome is one mode's answer. Collected per mode rather than merged as they
// arrive, because a failure is a per-mode fact: it names ONE mode in Degraded,
// and a merged stream would have lost which.
type outcome struct {
	mode domain.Capability
	ids  []string
	err  error
}

// Search runs the enabled modes concurrently under one deadline, fuses them and
// hydrates the page.
func (s *SearchRepository) Search(
	ctx context.Context, query domain.SearchQuery, modes []domain.Capability,
) (domain.SearchResult, error) {
	if err := s.withinRetrievalDepth(query); err != nil {
		return domain.SearchResult{}, err
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

	ranked, filtering, degraded := s.negotiate(modes)

	lists, failed := fold(s.retrieve(ctx, query, ranked, scope))
	degraded = append(degraded, failed...)

	if len(lists) == 0 && filtering {
		ids, err := s.filterOnly(ctx, query, scope)
		if err != nil {
			return domain.SearchResult{}, err
		}
		lists = append(lists, ids)
	}

	fused := RRF(lists...)
	page := pageOf(fused, query.Offset, query.Limit)

	catalogs, err := s.hydrator.Hydrate(ctx, page, scope)
	if err != nil {
		return domain.SearchResult{}, err
	}
	return domain.SearchResult{Catalogs: catalogs, Degraded: degraded}, nil
}

// withinRetrievalDepth refuses a page that lies past what any mode retrieves.
//
// The cap is also the reachable pagination depth, and it is checked BEFORE
// anything runs: a request that cannot be answered should not spend three
// queries discovering it, and the boundary is named so the caller can tell this
// from the end of the results.
func (s *SearchRepository) withinRetrievalDepth(query domain.SearchQuery) error {
	if query.Offset+query.Limit > s.search.MaxCandidatesPerMode {
		return fmt.Errorf(
			"%w: offset %d plus limit %d passes the %d ids a mode retrieves",
			ErrRetrievalDepth, query.Offset, query.Limit, s.search.MaxCandidatesPerMode)
	}
	return nil
}

// filterOnly is the retrieval for an intent that named a filter and no ranked
// mode this deployment runs — a geo-only intent, or one whose every ranked mode
// was declined. The predicate IS the query, so the candidate order stands in
// for a relevance nobody supplied. Without it the fusion is empty and "what is
// near me" answers nothing while reporting success.
//
// The error is returned rather than degraded, unlike a mode failure. A failed
// mode leaves siblings that still answered; this leaves nothing, and an empty
// page is indistinguishable at the caller from a query that matched nothing.
// Only one of those is an answer.
func (s *SearchRepository) filterOnly(
	ctx context.Context, query domain.SearchQuery, scope domain.Scope,
) ([]string, error) {
	ids, err := s.candidates.Retrieve(ctx, query, scope)
	if err != nil {
		return nil, fmt.Errorf("run the candidate retrieval: %w", err)
	}
	return ids, nil
}

// fold splits the per-mode outcomes into the lists to fuse and the modes to
// report.
//
// A failed mode is RECORDED, not fatal. Two modes returning is a better answer
// than none — and the caller is TOLD, through X-Beckn-Degraded, which is the
// difference between a degraded answer and a wrong one.
//
// Whether a mode stopped at its cap is no longer tracked (A19). Its only reader
// was the count, which had to know that a short list meant "truncated" rather
// than "the end"; the page itself never cared, because it is sliced from the
// fusion either way.
func fold(outcomes []outcome) (ranked [][]string, degraded []string) {
	for _, result := range outcomes {
		if result.err != nil {
			degraded = append(degraded, string(result.mode))
			continue
		}
		ranked = append(ranked, result.ids)
	}
	return ranked, degraded
}

// negotiate splits the requested modes into the ranked ones this backend will
// run and the ones it has to report as missing, and says whether a filter was
// asked for at all.
//
// A filter mode (domain.Capability.Ranked) is neither ranked nor missing: it is
// part of the WHERE clause every retriever and the counter already share, so
// asking for it is satisfied by running the search — reporting it degraded
// would tell a caller their geometry was ignored when it was applied, and
// looking for a retriever under its name finds none, because there is no query
// that is only a filter.
//
// Mirrors memory.Repository.negotiate deliberately, and the conformance case
// aSpatialOnlyIntentIsAnsweredRatherThanDegraded is what holds the two to one
// answer.
func (s *SearchRepository) negotiate(
	modes []domain.Capability,
) (ranked []domain.Capability, filtering bool, degraded []string) {
	declared := s.Capabilities()
	for _, mode := range modes {
		switch {
		case !mode.Ranked():
			filtering = true
			if !declared.Has(mode) {
				degraded = append(degraded, string(mode))
			}
		case !declared.Has(mode):
			// Asked for and not available. The negotiation in front of Search
			// is supposed to have removed it, so reaching here means the two
			// disagreed — reported rather than ignored, because an ignored mode
			// is one the caller believes ran.
			degraded = append(degraded, string(mode))
		default:
			ranked = append(ranked, mode)
		}
	}
	return ranked, filtering, degraded
}

// retrieve fans the ranked modes out and waits for all of them.
//
// A barrier and not a race: the fusion needs every list, and a mode that is
// still running when its siblings finish has not failed. The shared deadline is
// what bounds the wait.
func (s *SearchRepository) retrieve(
	ctx context.Context, query domain.SearchQuery, modes []domain.Capability, scope domain.Scope,
) []outcome {
	// Sized before any goroutine starts, and every mode in it has a retriever:
	// negotiate has already moved the ones this backend cannot run into the
	// degraded list, so there is no arm here for a mode with nothing behind it.
	outcomes := make([]outcome, len(modes))
	for index, mode := range modes {
		outcomes[index].mode = mode
	}

	var waiting sync.WaitGroup
	for index := range outcomes {
		waiting.Add(1)
		go func(slot *outcome) {
			defer waiting.Done()
			// Each goroutine writes only its OWN element, so there is no
			// shared write and no mutex. A channel here would buy nothing: the
			// barrier below already waits for all of them.
			slot.ids, slot.err = s.retrievers[slot.mode].Retrieve(ctx, query, scope)
		}(&outcomes[index])
	}
	waiting.Wait()
	return outcomes
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
