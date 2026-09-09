package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
	"github.com/OpenAgriNet/discovery-service/src/storage/postgres/gen"
)

// predicates is everything the retrievers share: the scope gate, the schema
// pair, the whole spatial EXISTS. One derivation, because sqlc emits a distinct
// parameter struct per query and deriving each separately would be one place
// per query for the quantifier XOR or the nil-cover rule to drift — invisibly,
// since every query would still return rows, just a different set.
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

	attributeFilter pgtype.Text
}

// sharedPredicates reduces a SearchQuery to the parameters every read query
// binds. NetworkID becomes NULL when unscoped — "" would match no visible_to
// entry and empty every response.
func sharedPredicates(query domain.SearchQuery) predicates {
	shared := predicates{networkID: nullableText(query.NetworkID)}

	// A PAIR of equal-length, index-aligned arrays. Empty stays nil so the
	// query's `IS NULL` arm fires and no schema predicate applies at all.
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

	// One expression, already rooted at `$.catalogs` and bound as a PARAMETER —
	// `@filter::jsonpath`, never a fragment of SQL (A18). Only the first is
	// bound: combining two would mean concatenating jsonpath text.
	if len(query.Filters) > 0 {
		shared.attributeFilter = nullableText(query.Filters[0].Expression)
	}
	return shared
}

// spatial fills the geometry half.
func (p *predicates) spatial(filter domain.SpatialFilter, targetPaths []string) {
	p.spatialOp = nullableText(string(filter.Op))

	// Three quantifiers out of two flags, XORed against the EXISTS and against
	// the match inside it:
	//
	//	ANY  → f, f →     EXISTS(matches)      at least one targeted shape
	//	NONE → t, f → NOT EXISTS(matches)      not one does
	//	ALL  → t, t → NOT EXISTS(NOT matches)  every one does
	//
	// ALL negates the inner predicate rather than conjoining, because "every
	// geometry matches" is only decidable as "none provably fails".
	p.geoNegate = filter.Quantifier == domain.QuantifierNone || filter.Quantifier == domain.QuantifierAll
	p.matchNegate = filter.Quantifier == domain.QuantifierAll

	// Empty means every shape the resource can be found by, so it stays nil and
	// the query emits no path predicate. `g.target_path = ANY($1)` is plain
	// equality, so these must arrive canonicalised — a dot-form filter against
	// a bracket-form stored path is an empty page with nothing to explain it.
	if len(targetPaths) > 0 {
		p.targetPaths = targetPaths
	}

	if filter.Bounds != nil {
		p.minLat = nullableFloat(filter.Bounds.MinLat)
		p.maxLat = nullableFloat(filter.Bounds.MaxLat)
		p.minLon = nullableFloat(filter.Bounds.MinLon)
		p.maxLon = nullableFloat(filter.Bounds.MaxLon)
	}

	// Nil TOGETHER: a declined cover disables the cell predicate and leaves the
	// box to decide. One without the other is a state the query has no branch
	// for.
	p.qCover = cells(filter.CellsCover)
	p.qFull = cells(filter.CellsFull)

	// Point-to-Point S_DWITHIN only. A centre on any other operator would
	// silently narrow it to a radius nobody asked for.
	if filter.Center != nil {
		p.centerLat = nullableFloat(filter.Center.Lat)
		p.centerLon = nullableFloat(filter.Center.Lon)
		p.radiusM = nullableFloat(filter.RadiusM)
	}
}

// nullableText maps "" to SQL NULL, so the predicate is skipped rather than
// matched against an empty string.
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func nullableFloat(value float64) pgtype.Float8 {
	return pgtype.Float8{Float64: value, Valid: true}
}

// LexicalRetriever answers the full-text mode. One type per mode, so adding a
// mode is a new type rather than a new arm in a shared switch.
type LexicalRetriever struct {
	queries *gen.Queries
	limit   int32
}

var _ domain.Retriever = (*LexicalRetriever)(nil)

// NewLexicalRetriever builds the mode over a store, capped at limit ids. The
// cap is not a safety net: `discover_tsquery` ORs its terms, so a broad query
// matches most of the corpus and the cap is what keeps it off the wire.
func NewLexicalRetriever(store gen.DBTX, limit int) *LexicalRetriever {
	return &LexicalRetriever{queries: gen.New(store), limit: int32(limit)}
}

// Retrieve returns the ids this mode ranks, best first.
//
// The Scope is ignored: this query's gate calls now() and reads visible_to
// itself. It stays in the signature for the backends that have no now() (A6),
// which a port without it could not implement.
func (l *LexicalRetriever) Retrieve(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) ([]string, error) {
	shared := sharedPredicates(query)
	rows, err := l.queries.LexicalCandidates(ctx, gen.LexicalCandidatesParams{
		NetworkID:       shared.networkID,
		SchemaContexts:  shared.schemaContexts,
		SchemaTypes:     shared.schemaTypes,
		SpatialOp:       shared.spatialOp,
		GeoNegate:       shared.geoNegate,
		TargetPaths:     shared.targetPaths,
		MatchNegate:     shared.matchNegate,
		MinLat:          shared.minLat,
		MaxLat:          shared.maxLat,
		MinLon:          shared.minLon,
		MaxLon:          shared.maxLon,
		QCover:          shared.qCover,
		QFull:           shared.qFull,
		CenterLat:       shared.centerLat,
		CenterLon:       shared.centerLon,
		RadiusM:         shared.radiusM,
		AttributeFilter: shared.attributeFilter,
		QueryText:       nullableText(query.Text),
		RowLimit:        l.limit,
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
		NetworkID:       shared.networkID,
		SchemaContexts:  shared.schemaContexts,
		SchemaTypes:     shared.schemaTypes,
		SpatialOp:       shared.spatialOp,
		GeoNegate:       shared.geoNegate,
		TargetPaths:     shared.targetPaths,
		MatchNegate:     shared.matchNegate,
		MinLat:          shared.minLat,
		MaxLat:          shared.maxLat,
		MinLon:          shared.minLon,
		MaxLon:          shared.maxLon,
		QCover:          shared.qCover,
		QFull:           shared.qFull,
		CenterLat:       shared.centerLat,
		CenterLon:       shared.centerLon,
		RadiusM:         shared.radiusM,
		AttributeFilter: shared.attributeFilter,
		QueryText:       nullableText(query.Text),
		RowLimit:        f.limit,
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

// SemanticRetriever answers the vector mode. It holds an Embedder because the
// query must be embedded by the SAME provider the corpus was: two providers are
// two unrelated spaces, and the distances between them mean nothing.
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

// Retrieve embeds the query text and returns the nearest ids. An embedder that
// fails fails the MODE, not the search — Search records it in Degraded. The
// Scope is ignored for the reason on LexicalRetriever.Retrieve.
func (s *SemanticRetriever) Retrieve(
	ctx context.Context, query domain.SearchQuery, _ domain.Scope,
) ([]string, error) {
	vector, err := queryVector(ctx, s.embedder, query.Text)
	if err != nil {
		return nil, err
	}

	shared := sharedPredicates(query)
	rows, err := s.queries.SemanticCandidates(ctx, gen.SemanticCandidatesParams{
		NetworkID:       shared.networkID,
		SchemaContexts:  shared.schemaContexts,
		SchemaTypes:     shared.schemaTypes,
		SpatialOp:       shared.spatialOp,
		GeoNegate:       shared.geoNegate,
		TargetPaths:     shared.targetPaths,
		MatchNegate:     shared.matchNegate,
		MinLat:          shared.minLat,
		MaxLat:          shared.maxLat,
		MinLon:          shared.minLon,
		MaxLon:          shared.maxLon,
		QCover:          shared.qCover,
		QFull:           shared.qFull,
		CenterLat:       shared.centerLat,
		CenterLon:       shared.centerLon,
		RadiusM:         shared.radiusM,
		AttributeFilter: shared.attributeFilter,
		QueryVector:     vector,
		RowLimit:        s.limit,
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

// queryVector embeds text, or answers nil when there is nothing to embed.
//
// A nil embedder or an empty text is nil and NO error: the query reads a NULL
// vector as "this mode contributes no rows", which is true for a geo-only
// intent and for the default configuration, where semantic is off (A5).
//
// The dimension guard runs here rather than at the statement, because
// pgvector's own width check fires inside the query and would report a storage
// failure for a provider misconfiguration three layers up.
func queryVector(ctx context.Context, embedder embeddings.Embedder, text string) (*pgvector.Vector, error) {
	if embedder == nil || text == "" {
		return nil, nil
	}

	started := time.Now()
	values, err := embedder.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed the query text: %w", err)
	}

	// Only when a vector came back: retrieval.embedding_ms is absent rather
	// than 0 when no embedding ran (opentelemetry.md:716). logger.Millis, so
	// this and duration_ms round the same way. fact is what keeps the OTel SDK
	// out of src/storage (A23).
	if len(values) > 0 {
		fact.ObserveFloat64(ctx, fact.RetrievalEmbeddingMs, logger.Millis(time.Since(started)))
	}

	if err := embeddings.CheckDimensions(values, embedder.Dimensions()); err != nil {
		return nil, fmt.Errorf("embed the query text: %w", err)
	}
	return embedding(values), nil
}
