package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// SearchRepository is the read half of the PostgreSQL adapter: the modes, the
// fusion over them, and the hydration of what the fusion decided.
type SearchRepository struct {
	retrievers map[domain.Capability]domain.Retriever
	hydrator   domain.Hydrator
	search     config.Search

	// candidates answers a filter-only or geo-only intent with the rows the
	// shared predicate admits, in the stable (catalog_id, id) order the memory
	// backend sorts by. It is the LEXICAL retriever: LexicalCandidates reads a
	// NULL query_text as "every row the shared predicates admit", and its ORDER
	// BY then falls through to that same key.
	candidates domain.Retriever

	// semantic records whether this deployment has a query-side embedder, kept
	// as a field so Capabilities answers the question the retriever map was
	// built from.
	semantic bool
}

var _ domain.SearchRepository = (*SearchRepository)(nil)

// NewSearchRepository builds the read repository over a pool.
//
// A NIL embedder means this deployment has no semantic mode, and that is the
// signal Capabilities reports — not a noop embedder (A5), which would declare
// the capability and then return zero rows forever.
func NewSearchRepository(
	pool *pgxpool.Pool, search config.Search, embedder embeddings.Embedder,
) *SearchRepository {
	lexical := NewLexicalRetriever(pool, search.MaxCandidatesPerMode)
	repository := &SearchRepository{
		retrievers: map[domain.Capability]domain.Retriever{
			domain.CapabilityLexical: lexical,
			domain.CapabilityFuzzy:   NewFuzzyRetriever(pool, search.MaxCandidatesPerMode),
		},
		hydrator:   NewHydrator(pool),
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
// Spatial and JSONPath are unconditional: both are part of the WHERE clause
// every retriever shares, and every publish through this adapter writes the H3
// columns and `resources.filter_doc` they read. The memory backend declares
// JSONPath false, because it has the documents but not PostgreSQL's engine.
func (s *SearchRepository) Capabilities() domain.Capabilities {
	return domain.Capabilities{
		domain.CapabilityLexical:  true,
		domain.CapabilityFuzzy:    true,
		domain.CapabilitySemantic: s.semantic,
		domain.CapabilitySpatial:  true,
		domain.CapabilityJSONPath: true,
	}
}

// outcome is one mode's answer, kept per mode because a failure names ONE mode
// in Degraded.
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

	// One deadline for the whole fan-out (A2/A3), not one per mode.
	ctx, cancel := context.WithTimeout(ctx, s.search.ReadDeadline)
	defer cancel()

	// A6: captured ONCE so every mode agrees on "now". Postgres ignores it and
	// calls now(); it is for the backends that have none.
	scope := domain.Scope{NetworkID: query.NetworkID, Now: time.Now().UTC()}

	ranked, filtering, degraded := s.negotiate(modes)

	lists, failed, fatal := fold(s.retrieve(ctx, query, ranked, scope))
	if fatal != nil {
		return domain.SearchResult{}, fatal
	}
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

// withinRetrievalDepth refuses a page past what any mode retrieves. Checked
// before anything runs, and named in the error so the caller can tell it from
// the end of the results.
func (s *SearchRepository) withinRetrievalDepth(query domain.SearchQuery) error {
	if query.Offset+query.Limit > s.search.MaxCandidatesPerMode {
		return fmt.Errorf(
			"%w: offset %d plus limit %d passes the %d ids a mode retrieves",
			domain.ErrRetrievalDepth, query.Offset, query.Limit, s.search.MaxCandidatesPerMode)
	}
	return nil
}

// filterOnly retrieves for an intent that named a filter and no ranked mode
// this deployment runs. The predicate IS the query, so the candidate order
// stands in for a relevance nobody supplied.
//
// Its error is returned rather than degraded: unlike a failed mode, this leaves
// no siblings, and an empty page reads at the caller as "nothing matched".
func (s *SearchRepository) filterOnly(
	ctx context.Context, query domain.SearchQuery, scope domain.Scope,
) ([]string, error) {
	ids, err := s.candidates.Retrieve(ctx, query, scope)
	if err != nil {
		return nil, fmt.Errorf("run the candidate retrieval: %w", callersFilter(query, err))
	}
	return ids, nil
}

// fold splits the per-mode outcomes into the lists to fuse and the modes to
// report. A failed mode is RECORDED in X-Beckn-Degraded, not fatal.
func fold(outcomes []outcome) (ranked [][]string, degraded []string, fatal error) {
	for _, result := range outcomes {
		switch {
		// The one failure that is not the deployment's, and it cannot be
		// degraded: every mode binds the same attribute filter, so a mode the
		// expression broke is a mode all of them broke, and the siblings that
		// appear to have answered answered a different query.
		case errors.Is(result.err, domain.ErrInvalidFilterExpression):
			return nil, nil, result.err
		case result.err != nil:
			degraded = append(degraded, string(result.mode))
		default:
			ranked = append(ranked, result.ids)
		}
	}
	return ranked, degraded, nil
}

// callersFilter re-labels the store's refusal of the caller's own attribute
// filter as domain.ErrInvalidFilterExpression, and leaves every other error as
// it was. Unlabelled, a dropped dot in an expression is a 500 that invites a
// retry of a request that can never succeed.
//
// Both conditions are load-bearing: the query must have carried a filter, and
// the SQLSTATE must be one of the two PostgreSQL raises while turning text into
// a jsonpath — 42601 from the parser, 2201B from a like_regex that will not
// compile. The filter condition is sufficient only because the caller's
// expression is the ONLY text PostgreSQL parses at runtime here; every
// statement in this package is a constant. Adding one that is not breaks this.
//
// The sentinel wraps rather than replaces, so the caller reads its text while
// the operator's log keeps the SQLSTATE.
func callersFilter(query domain.SearchQuery, err error) error {
	if err == nil || len(query.Filters) == 0 {
		return err
	}

	var refusal *pgconn.PgError
	if !errors.As(err, &refusal) {
		return err
	}

	switch refusal.Code {
	case pgerrcode.SyntaxError, pgerrcode.InvalidRegularExpression:
		return fmt.Errorf("%w: %w", domain.ErrInvalidFilterExpression, err)
	default:
		return err
	}
}

// negotiate splits the requested modes into the ranked ones this backend will
// run and the ones to report as missing, and says whether a filter was asked
// for at all.
//
// A filter mode (domain.Capability.Ranked) is neither: it is part of the WHERE
// clause every retriever shares, so asking for it is satisfied by running the
// search, and there is no retriever under its name.
//
// Mirrors memory.Repository.negotiate; the conformance case
// aSpatialOnlyIntentIsAnsweredRatherThanDegraded holds the two to one answer.
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
			// Asked for and unavailable. The negotiation in front of Search
			// should have removed it, so reaching here means the two disagreed
			// — reported, because an ignored mode is one the caller believes
			// ran.
			degraded = append(degraded, string(mode))
		default:
			ranked = append(ranked, mode)
		}
	}
	return ranked, filtering, degraded
}

// retrieve fans the ranked modes out and waits for all of them — a barrier, not
// a race, because the fusion needs every list. The shared deadline bounds it.
func (s *SearchRepository) retrieve(
	ctx context.Context, query domain.SearchQuery, modes []domain.Capability, scope domain.Scope,
) []outcome {
	// Every mode here has a retriever: negotiate already moved the ones this
	// backend cannot run into the degraded list.
	outcomes := make([]outcome, len(modes))
	for index, mode := range modes {
		outcomes[index].mode = mode
	}

	var waiting sync.WaitGroup
	for index := range outcomes {
		waiting.Add(1)
		go func(slot *outcome) {
			defer waiting.Done()
			// Each goroutine writes only its own element, so no mutex.
			ids, err := s.retrievers[slot.mode].Retrieve(ctx, query, scope)
			slot.ids, slot.err = ids, callersFilter(query, err)
		}(&outcomes[index])
	}
	waiting.Wait()
	return outcomes
}

// pageOf slices the fused list, clamping rather than panicking. Past-the-depth
// is already refused above; this is the short tail, where offset is inside the
// cap but past what matched.
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
