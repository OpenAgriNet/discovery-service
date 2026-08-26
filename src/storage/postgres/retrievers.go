package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// predicates is everything the three retrievers and the counter share: the
// scope gate, the schema pair, the whole spatial EXISTS. Four queries, one
// derivation.
//
// It exists in Go because the four sqlc parameter structs are four distinct
// types that happen to hold the same sixteen fields. Deriving each one from the
// SearchQuery separately would be four places for the quantifier XOR or the
// nil-cover rule to drift, and drift there is invisible: each query would still
// return rows, just a different set from its siblings, and the fusion would
// report nothing wrong.
type predicates struct {
	networkID      pgtype.Text
	schemaContexts []string
	schemaTypes    []string

	spatialOp   pgtype.Text
	geoNegate   bool
	matchNegate bool
	targetPaths []string
	minLat      pgtype.Float8
	maxLat      pgtype.Float8
	minLon      pgtype.Float8
	maxLon      pgtype.Float8
	qCover      []int64
	qFull       []int64
	centerLat   pgtype.Float8
	centerLon   pgtype.Float8
	radiusM     pgtype.Float8
}

// sharedPredicates reduces a SearchQuery to the parameters every read query
// binds.
//
// Scope is accepted and deliberately unused for the instant: Postgres's gate
// calls now() and reads the transaction's own clock, which is what makes the
// gate a property of the statement rather than of a timestamp the caller could
// have computed wrong. Scope.Now exists for the backends that have no now()
// (A6). NetworkID, however, IS read from the query, because "" there means
// UNSCOPED — every network — and the parameter must be NULL rather than an
// empty string that matches no visible_to entry.
func sharedPredicates(query domain.SearchQuery) predicates {
	shared := predicates{networkID: nullableText(query.NetworkID)}

	// Emitted as a PAIR of equal-length arrays, index-aligned. Empty stays nil,
	// so the query's `IS NULL` arm fires and no schema predicate is applied at
	// all — an empty schemaContext must return everything rather than nothing.
	for _, filter := range query.Schemas {
		shared.schemaContexts = append(shared.schemaContexts, filter.Context)
		// "" is the sentinel for "any type under this context", read by the
		// query's `st.typ = ''` arm. It cannot be NULL: a NULL inside a text[]
		// makes every comparison against it NULL, which reads as no match.
		shared.schemaTypes = append(shared.schemaTypes, filter.Type)
	}

	if query.Spatial != nil {
		shared.spatial(*query.Spatial, query.TargetPaths)
	}
	return shared
}

// spatial fills the geometry half. Split out because the quantifier decision is
// two lines that decide the meaning of every geo search and belong somewhere
// they can be read on their own.
func (p *predicates) spatial(filter domain.SpatialFilter, targetPaths []string) {
	p.spatialOp = nullableText(string(filter.Op))

	// Three quantifiers out of two flags, XORed against the EXISTS and against
	// the match inside it:
	//   ANY  → f, f →     EXISTS(matches)      at least one targeted shape
	//   NONE → t, f → NOT EXISTS(matches)      not one does
	//   ALL  → t, t → NOT EXISTS(NOT matches)  every one does
	// ALL is NOT EXISTS over the NEGATED predicate and not EXISTS over the
	// conjunction, because "every geometry matches" is only decidable as "none
	// provably fails".
	p.geoNegate = filter.Quantifier == domain.QuantifierNone || filter.Quantifier == domain.QuantifierAll
	p.matchNegate = filter.Quantifier == domain.QuantifierAll

	// Empty means every shape the resource can be found by — its own and its
	// catalog's — so it stays nil and the query emits no path predicate.
	// `g.target_path = ANY($1)` is plain equality, so what is passed here must
	// already be canonicalised: a dot-form filter against a bracket-form stored
	// path is an empty page with nothing anywhere to explain it.
	if len(targetPaths) > 0 {
		p.targetPaths = targetPaths
	}

	if filter.Bounds != nil {
		p.minLat = nullableFloat(filter.Bounds.MinLat)
		p.maxLat = nullableFloat(filter.Bounds.MaxLat)
		p.minLon = nullableFloat(filter.Bounds.MinLon)
		p.maxLon = nullableFloat(filter.Bounds.MaxLon)
	}

	// The two covers are nil TOGETHER — a cover that declined disables the cell
	// predicate entirely and leaves the box to decide — so passing one without
	// the other would put the query in a state it has no branch for.
	p.qCover = cells(filter.CellsCover)
	p.qFull = cells(filter.CellsFull)

	// Populated only for Point-to-Point S_DWITHIN. A centre on any other
	// operator would silently narrow that operator's answer to a radius nobody
	// asked it to apply.
	if filter.Center != nil {
		p.centerLat = nullableFloat(filter.Center.Lat)
		p.centerLon = nullableFloat(filter.Center.Lon)
		p.radiusM = nullableFloat(filter.RadiusM)
	}
}

// nullableText maps "" to SQL NULL.
//
// The distinction is load-bearing in both directions: an empty networkId means
// EVERY network and must reach the query as NULL so the predicate is skipped,
// while "" sent as a value would match no visible_to entry and empty every
// response.
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func nullableFloat(value float64) pgtype.Float8 {
	return pgtype.Float8{Float64: value, Valid: true}
}

// LexicalRetriever answers the full-text mode.
//
// One type per mode rather than one type with a mode parameter, so the modes
// run concurrently without sharing a code path and adding a mode is a new type
// rather than a new arm in a switch every existing mode passes through.
type LexicalRetriever struct {
	queries *gen.Queries
	limit   int32
}

var _ domain.Retriever = (*LexicalRetriever)(nil)

// NewLexicalRetriever builds the mode over a store, capped at limit ids.
//
// The cap is not optional and not a safety net: `discover_tsquery` ORs its
// terms, so "wheat seeds for sale" matches every listing carrying any one of
// those words. The broad query is the ordinary one, and without the cap it
// sends the corpus across the wire for RRF to rank and discard.
func NewLexicalRetriever(store gen.DBTX, limit int) *LexicalRetriever {
	return &LexicalRetriever{queries: gen.New(store), limit: int32(limit)}
}

// Retrieve returns the ids this mode ranks, best first.
//
// The Scope is ignored: this query's gate calls now() and reads visible_to
// itself, so the instant the caller captured is already in the WHERE clause the
// count query shares. It stays in the signature because the backends that have
// no now() — the memory store, and any index with no notion of validity — need
// it, and a port that dropped it would make them unimplementable.
func (l *LexicalRetriever) Retrieve(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) ([]string, error) {
	shared := sharedPredicates(query)
	rows, err := l.queries.LexicalCandidates(ctx, gen.LexicalCandidatesParams{
		NetworkID:      shared.networkID,
		SchemaContexts: shared.schemaContexts,
		SchemaTypes:    shared.schemaTypes,
		SpatialOp:      shared.spatialOp,
		GeoNegate:      shared.geoNegate,
		TargetPaths:    shared.targetPaths,
		MatchNegate:    shared.matchNegate,
		MinLat:         shared.minLat,
		MaxLat:         shared.maxLat,
		MinLon:         shared.minLon,
		MaxLon:         shared.maxLon,
		QCover:         shared.qCover,
		QFull:          shared.qFull,
		CenterLat:      shared.centerLat,
		CenterLon:      shared.centerLon,
		RadiusM:        shared.radiusM,
		QueryText:      nullableText(query.Text),
		RowLimit:       l.limit,
	})
	if err != nil {
		return nil, fmt.Errorf("run the lexical retriever: %w", err)
	}

	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, domain.ResourceKey(row.CatalogID, row.ID))
	}
	return keys, nil
}

// FuzzyRetriever answers the trigram mode.
type FuzzyRetriever struct {
	queries *gen.Queries
	limit   int32
}

var _ domain.Retriever = (*FuzzyRetriever)(nil)

// NewFuzzyRetriever builds the mode over a store, capped at limit ids.
func NewFuzzyRetriever(store gen.DBTX, limit int) *FuzzyRetriever {
	return &FuzzyRetriever{queries: gen.New(store), limit: int32(limit)}
}

// Retrieve returns the ids this mode ranks, most similar first. The Scope is
// ignored for the reason given on LexicalRetriever.Retrieve.
func (f *FuzzyRetriever) Retrieve(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) ([]string, error) {
	shared := sharedPredicates(query)
	rows, err := f.queries.FuzzyCandidates(ctx, gen.FuzzyCandidatesParams{
		NetworkID:      shared.networkID,
		SchemaContexts: shared.schemaContexts,
		SchemaTypes:    shared.schemaTypes,
		SpatialOp:      shared.spatialOp,
		GeoNegate:      shared.geoNegate,
		TargetPaths:    shared.targetPaths,
		MatchNegate:    shared.matchNegate,
		MinLat:         shared.minLat,
		MaxLat:         shared.maxLat,
		MinLon:         shared.minLon,
		MaxLon:         shared.maxLon,
		QCover:         shared.qCover,
		QFull:          shared.qFull,
		CenterLat:      shared.centerLat,
		CenterLon:      shared.centerLon,
		RadiusM:        shared.radiusM,
		QueryText:      nullableText(query.Text),
		RowLimit:       f.limit,
	})
	if err != nil {
		return nil, fmt.Errorf("run the fuzzy retriever: %w", err)
	}

	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, domain.ResourceKey(row.CatalogID, row.ID))
	}
	return keys, nil
}

// SemanticRetriever answers the vector mode.
//
// It holds an Embedder because the query side has to embed the caller's text
// with the SAME provider the write side embedded the corpus with; two providers
// produce two unrelated spaces and the distances between them mean nothing.
type SemanticRetriever struct {
	queries  *gen.Queries
	embedder embeddings.Embedder
	limit    int32
}

var _ domain.Retriever = (*SemanticRetriever)(nil)

// NewSemanticRetriever builds the mode over a store and a query-side embedder.
func NewSemanticRetriever(store gen.DBTX, embedder embeddings.Embedder, limit int) *SemanticRetriever {
	return &SemanticRetriever{queries: gen.New(store), embedder: embedder, limit: int32(limit)}
}

// Retrieve embeds the query text and returns the nearest ids.
//
// An embedder that fails fails the MODE rather than the search: the error
// reaches Search, which records it in Degraded and fuses what the other modes
// returned. The Scope is ignored for the reason given on
// LexicalRetriever.Retrieve.
func (s *SemanticRetriever) Retrieve(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) ([]string, error) {
	vector, err := queryVector(ctx, s.embedder, query.Text)
	if err != nil {
		return nil, err
	}

	shared := sharedPredicates(query)
	rows, err := s.queries.SemanticCandidates(ctx, gen.SemanticCandidatesParams{
		NetworkID:      shared.networkID,
		SchemaContexts: shared.schemaContexts,
		SchemaTypes:    shared.schemaTypes,
		SpatialOp:      shared.spatialOp,
		GeoNegate:      shared.geoNegate,
		TargetPaths:    shared.targetPaths,
		MatchNegate:    shared.matchNegate,
		MinLat:         shared.minLat,
		MaxLat:         shared.maxLat,
		MinLon:         shared.minLon,
		MaxLon:         shared.maxLon,
		QCover:         shared.qCover,
		QFull:          shared.qFull,
		CenterLat:      shared.centerLat,
		CenterLon:      shared.centerLon,
		RadiusM:        shared.radiusM,
		QueryVector:    vector,
		RowLimit:       s.limit,
	})
	if err != nil {
		return nil, fmt.Errorf("run the semantic retriever: %w", err)
	}

	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, domain.ResourceKey(row.CatalogID, row.ID))
	}
	return keys, nil
}

// queryVector embeds text for whichever side is asking, or answers nil when
// there is nothing to embed.
//
// Shared by the semantic retriever and the counter, and that sharing is the
// point: the two must bind the SAME vector for the same request, or Total would
// describe a pool the page was not drawn from.
//
// A nil embedder or an empty text is nil and no error. Both queries read a NULL
// vector as "this mode contributes no rows", which is exactly true for a
// geo-only intent and for the default configuration, where semantic is off
// (A5). Failing instead would take down every page whose count guards did not
// fire, on the deployment this service actually ships as.
//
// The dimension guard runs HERE rather than at the statement, because
// pgvector's own width check fires inside the query and reports a storage
// failure for what is a provider misconfiguration three layers up — the
// read-side twin of the guard on the publish path.
func queryVector(ctx context.Context, embedder embeddings.Embedder, text string) (*pgvector.Vector, error) {
	if embedder == nil || text == "" {
		return nil, nil
	}

	values, err := embedder.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed the query text: %w", err)
	}
	if err := embeddings.CheckDimensions(values, embedder.Dimensions()); err != nil {
		return nil, fmt.Errorf("embed the query text: %w", err)
	}
	return embedding(values), nil
}
