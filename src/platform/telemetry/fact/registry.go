package fact

// The keys, in the order the registry array indexes them.
//
// Grouped by where the fact comes from rather than alphabetically, because the
// grouping is what makes a missing row visible: a reviewer reading the publish
// block notices the count that is not there, and cannot notice it in a sorted
// list. numKeys closes the block and sizes the array, so a key added here
// without a row is the completeness test's first failure rather than a zero
// Definition four projections silently drop.
const (
	// The Resource, set once at boot and stamped on every signal.
	ResourceEID Key = iota
	ResourceProducer
	ResourceDomain
	ResourceServiceName
	ResourceNetworkID

	// The spec's mandatory span profile.
	SenderID
	RecipientID
	SpanUUID
	ObservedTimeUnixNano
	HTTPMethod
	HTTPHost
	HTTPRoute
	HTTPStatusCode
	HTTPScheme
	HTTPFlavor

	// Ours: correlators and classification, true for the whole request.
	RequestID
	BecknAction
	BecknVersion
	BecknNetworkID
	BecknTransactionID
	BecknMessageID
	BecknReceiverID
	BecknSchemaContext
	BecknSchemaType
	ErrorType
	DurationMS

	// The error event, projected from the fault logNack already holds.
	ErrorEventType
	ErrorCode
	ErrorMessage
	ErrorPath

	// discover: request_info.
	IntentKinds
	IntentFilterType
	IntentSpatialOps
	IntentScoped

	// discover: retrieval_info.
	RetrievalModesRun
	RetrievalModesDegraded
	RetrievalEmbeddingMs

	// discover: response_info.
	ResultCatalogCount
	ResultProviderIDs
	ResultEmpty

	// publish: request_info.
	PublishProviderIDs
	PublishCatalogCount
	PublishResourceCount
	PublishOfferCount
	PublishUpdateModes
	PublishCatalogTypes
	PublishVisibleTo
	PublishValidityPresent

	numKeys
)

// The bounds on the two caller-supplied lists. Named rather than repeated so
// the pair that has to stay parallel is bounded by one number, not by two that
// happen to match today.
const (
	maxSchemaEntries = 16
	maxSchemaRunes   = 256
	maxProviderIDs   = 16
)

// The closed value sets. A Bounded key's Values is what turns the claim into
// something a projection can enforce, so each of these is sourced from the
// declaration it mirrors rather than retyped from the design document.
var (
	// src/platform/errors/beckn_error.go:21-25.
	errorTypes = []string{"CONTEXT", "CORE", "DOMAIN", "POLICY", "SYSTEM"}
	// src/domain/query.go:192-196.
	retrievalModes = []string{"lexical", "fuzzy", "semantic", "spatial", "jsonpath"}
	// src/domain/catalog.go:310,315.
	updateModes = []string{"FULL", "MERGE"}
	// src/beckn/actions.go:90-91.
	catalogTypes = []string{"REGULAR", "MASTER"}
	// The four intent shapes src/discover/intent_mapper.go reads off the envelope.
	intentKinds = []string{"textSearch", "filters", "spatial", "mediaSearch"}
	// A bool's value set, written out so Bounded means the same thing on every row.
	boolValues = []string{"false", "true"}
)

// registry is THE ONE TABLE. Every projection reads it; nothing else decides
// how a fact is spelled.
//
// Rows are written keyed by their Key rather than positionally, so reordering
// the const block above cannot silently reassign a row to a different fact.
var registry = [numKeys]Definition{
	// ---- Resource ------------------------------------------------------------
	//
	// eid is the one Resource attribute the projections vary: API for spans,
	// METRIC for metrics and AUDIT — not LOG — for log records
	// (otel-specification.md:271, :446, :599). That is why it is a row with a
	// projection rule rather than a literal in Init.
	ResourceEID: {
		Name:        "ResourceEID",
		SpanKey:     "eid",
		Signals:     Resource,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Bounded,
		Values:      []string{"API", "METRIC", "AUDIT"},
		Visibility:  Public,
		Required:    true,
		Note: "The signal is called LOG/AUDIT and its eid is AUDIT. onix gets this " +
			"right (otelsetup.go:122,143,159) and it is the kind of detail a second " +
			"implementation guesses wrong.",
	},
	ResourceProducer: {
		Name:        "ResourceProducer",
		SpanKey:     "producer",
		Signals:     Resource,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Required:    true,
		Note: "This deployment's registered subscriber id, an FQDN from APP_SUBSCRIBER_ID " +
			"— which nothing sets today. NOT service.name: producer says which " +
			"participant this is and differs per deployment, service.name says what " +
			"software this is and does not.",
	},
	ResourceDomain: {
		Name:        "ResourceDomain",
		SpanKey:     "domain",
		Signals:     Resource,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Bounded,
		Values:      []string{"Agriculture"},
		Visibility:  Public,
		Required:    true,
		Note:        "The sector. Not the network, not the entity type.",
	},
	ResourceServiceName: {
		Name:        "ResourceServiceName",
		SpanKey:     "service.name",
		Signals:     Resource,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      []string{"discovery-service"},
		Visibility:  Public,
		Note: "ClickStack's grouping column, and a constant. Not Required: it is " +
			"ours and a deployment that has not set it should still boot.",
	},
	ResourceNetworkID: {
		Name:        "ResourceNetworkID",
		SpanKey:     "network.id",
		Signals:     Resource,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note:        "APP_NETWORK_ID — mahavistar, bharatvistar (C8). Our key, not the spec's.",
	},

	// ---- The spec's mandatory span profile -----------------------------------
	//
	// sender.id is Required by the spec and optional here: envelope_rules.go:98-104
	// deliberately excludes senderId, because participant identity was parked with
	// Task 6. So a valid request can carry no caller identity, and one that carries
	// it carries a string the caller chose — hence both flags, and neither
	// collapsing into the other.
	SenderID: {
		Name:        "SenderID",
		SpanKey:     "sender.id",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		AbsentFlag:  "sender.unidentified",
		PresentFlag: "sender.unverified",
		Note: "context.senderId when present, and unverified whenever present. " +
			"Never synthesise one to buy admission past onix's filter/network_traces " +
			"(node/otel-collector-bap/config.yaml:29-33): missing from a dashboard is " +
			"recoverable, poisoning the network's only cross-participant identity " +
			"join is not.",
	},
	RecipientID: {
		Name:        "RecipientID",
		SpanKey:     "recipient.id",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		AbsentFlag:  "recipient.unidentified",
		Note: "APP_SUBSCRIBER_ID, the same value as producer. Ours, never the " +
			"caller's receiverId — a caller can address anyone and this has to say " +
			"who answered. Constant on every span this binary emits, because we only " +
			"ever receive, so none of onix's direction machinery applies. Never fall " +
			"back to context.receiverId: the controllers echo it, so the fallback " +
			"would make the misaddressing query silently always return nothing.",
	},
	SpanUUID: {
		Name:        "SpanUUID",
		SpanKey:     "span_uuid",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note:        "Generated per span by a SpanProcessor's OnStart (23a).",
	},
	ObservedTimeUnixNano: {
		Name:        "ObservedTimeUnixNano",
		SpanKey:     "observedTimeUnixNano",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Unix nanos as a string: the spec's prose says ISO, its field name and " +
			"examples say nanos, and the name wins. Set in the middleware just before " +
			"span.End() — OnEnd receives a ReadOnlySpan and cannot set attributes.",
	},
	HTTPMethod: {
		Name:        "HTTPMethod",
		SpanKey:     "http.method",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Bounded,
		Values:      []string{"POST"},
		Visibility:  Public,
		Note: "Bounded by the router, not by the protocol: router.go:94-95 mounts " +
			"POST /publish and POST /discover, and the two GET probes emit no span. " +
			"A new verb widens this row before it widens the mux.",
	},
	HTTPHost: {
		Name:        "HTTPHost",
		SpanKey:     "http.host",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
	},
	HTTPRoute: {
		Name:        "HTTPRoute",
		SpanKey:     "http.route",
		Signals:     Span,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Bounded,
		Values:      []string{"/discover", "/publish"},
		Visibility:  Public,
		Note: "The route TEMPLATE from r.Pattern with the method prefix stripped — " +
			"the pattern reads \"POST /discover\" and http.method already carries the " +
			"verb. The spec's prose says URL; we differ deliberately, and a reader who " +
			"\"fixes\" this toward the spec starts shipping query strings.",
	},
	HTTPStatusCode: {
		Name:        "HTTPStatusCode",
		SpanKey:     "http.status_code",
		SpanAliases: []Alias{{Key: "http.status.code", AsString: true}},
		LogKey:      "status",
		Signals:     Span | Log,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Both spellings, because the spec disagrees with itself on the type: " +
			"its structure declares Int, all three of its examples emit a string. We " +
			"follow the examples for http.status.code and keep the int on " +
			"http.status_code for ClickStack. One value, written once, so they cannot " +
			"drift — which is the whole difference between this and a duplicated duration.",
	},
	HTTPScheme: {
		Name:        "HTTPScheme",
		SpanKey:     "http.scheme",
		Signals:     Span,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      []string{"http", "https"},
		Visibility:  Public,
	},
	HTTPFlavor: {
		Name:        "HTTPFlavor",
		SpanKey:     "http.flavor",
		Signals:     Span,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      []string{"1.0", "1.1", "2.0"},
		Visibility:  Public,
	},

	// ---- Ours: correlators and classification --------------------------------
	RequestID: {
		Name:        "RequestID",
		LogKey:      "request_id",
		Signals:     Log,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Log only. It is this process's handle on a request and nothing " +
			"outside reads it; span_uuid is the span's identity and traceId the " +
			"trace's, so a third id on the span would be a third thing to join on.",
	},
	BecknAction: {
		Name:        "BecknAction",
		SpanKey:     "beckn.action",
		LogKey:      "action",
		Signals:     Span | Log,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      []string{"discover", "publish"},
		Visibility:  Public,
		Note: "Normalised, not verbatim: catalog/publish is accepted on the wire " +
			"(beckn/actions.go:26-27) and both spellings resolve to one handler, so " +
			"both record publish. Emitting context.action as sent would split every " +
			"publish query in two. No Label bit until Task 25 declares an instrument " +
			"that consumes one; rate/errors/duration by action come from the " +
			"collector's spanmetrics connector, not from a counter here.",
	},
	BecknVersion: {
		Name:        "BecknVersion",
		SpanKey:     "beckn.version",
		Signals:     Span,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "beckn.Version is const 2.0.0 today, but this is context.version as " +
			"the caller sent it and Context has no required list (C6), so an envelope " +
			"can omit it. Unbounded rather than Bounded on one value: the row would " +
			"otherwise have to be edited on the day it is least likely to be read.",
	},
	BecknNetworkID: {
		Name:        "BecknNetworkID",
		SpanKey:     "beckn.networkId",
		Signals:     Span,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "The network the CALLER named, beside network.id which is the one this " +
			"deployment serves. Two keys because the two disagreeing is the interesting case.",
	},
	BecknTransactionID: {
		Name:        "BecknTransactionID",
		SpanKey:     "beckn.transactionId",
		SpanAliases: []Alias{{Key: "transaction_id"}},
		LogKey:      "transaction_id",
		Signals:     Span | Log,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "I1. onix's network collector rewrites trace_id from an attribute " +
			"named literally transaction_id (otel-collector-network/config.yaml:23-25), " +
			"and onix injects traceparent nowhere (SpanKindClient appears in no file " +
			"there), so while propagation is missing this alias is the only thing " +
			"joining our span to the adapter's. Renaming two attributes in one service " +
			"is cheaper than renaming one in three adapters and every collector config.",
	},
	BecknMessageID: {
		Name:        "BecknMessageID",
		SpanKey:     "beckn.messageId",
		SpanAliases: []Alias{{Key: "message_id"}},
		LogKey:      "message_id",
		Signals:     Span | Log,
		Kind:        KindString,
		Layer:       CrossLayer,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "I1, and a searchable tag rather than a join: onix's collector " +
			"deliberately does NOT map it onto span_id, because several nodes emit " +
			"spans for one Beckn message and identical span ids would corrupt the trace.",
	},
	BecknReceiverID: {
		Name:        "BecknReceiverID",
		SpanKey:     "beckn.receiverId",
		Signals:     Span,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "context.receiverId when present — the caller's CLAIM about who it " +
			"addressed. Emitted beside recipient.id rather than into it: the two " +
			"disagreeing is a caller addressing a participant that is not us, which " +
			"is worth a query and impossible to ask if one field holds both.",
	},
	BecknSchemaContext: {
		Name:           "BecknSchemaContext",
		SpanKey:        "beckn.schemaContext",
		Signals:        Span,
		Kind:           KindStrings,
		Layer:          Local,
		Cardinality:    Unbounded,
		Visibility:     Public,
		MaxEntries:     maxSchemaEntries,
		MaxRunes:       maxSchemaRunes,
		TruncationFlag: "beckn.schemaTruncated",
		Note: "On discover this is the seeker's predicate, not decoration: " +
			"mapSchemaContext reads it off the envelope, splits each entry on # and " +
			"the repository turns it into a schema clause. Bounded in size because it " +
			"is attacker-controlled and export is always-on and unsampled — a @context " +
			"entry is a URI the caller wrote, with room for arbitrary text in its " +
			"query and fragment, and a deny-list over KEYS cannot see that. Omit " +
			"entirely when the field was absent: absent means no predicate at all, " +
			"which is a larger bucket than a seeker who sent an empty array.",
	},
	BecknSchemaType: {
		Name:           "BecknSchemaType",
		SpanKey:        "beckn.schemaType",
		Signals:        Span,
		Kind:           KindStrings,
		Layer:          Local,
		Cardinality:    Unbounded,
		Visibility:     Public,
		MaxEntries:     maxSchemaEntries,
		MaxRunes:       maxSchemaRunes,
		TruncationFlag: "beckn.schemaTruncated",
		Note: "The #fragment of each @context entry, '' where an entry named no " +
			"type. Parallel and same-length with BecknSchemaContext, and truncated by " +
			"the same two bounds so it stays that way — flattening them into two " +
			"independent sets would count cross-matches no request made, and " +
			"truncating one alone turns a correct pairing into silently wrong pairs. " +
			"Shares beckn.schemaTruncated deliberately: one cut, one flag.",
	},
	ErrorType: {
		Name:        "ErrorType",
		SpanKey:     "error_type",
		LogKey:      "error_type",
		Signals:     Span | Log,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      errorTypes,
		Visibility:  Public,
		Note: "The C1 category, on the SPAN. Also on the error event as ErrorEventType, " +
			"so a facilitator can filter a span set without unpacking events.",
	},
	DurationMS: {
		Name:        "DurationMS",
		LogKey:      "duration_ms",
		Signals:     Log,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Log only, and deliberately not a span attribute: response time is the " +
			"span's own end - start, stored as a native Duration column and charted " +
			"from there. A duration attribute would be a second copy free to disagree " +
			"with the first. The log line has no such column, which is why it keeps one.",
	},

	// ---- The error event -----------------------------------------------------
	ErrorEventType: {
		Name:        "ErrorEventType",
		SpanKey:     "type",
		Signals:     Span,
		Event:       ErrorEvent,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      errorTypes,
		Visibility:  Public,
		Note: "beckn.Error.Type on the error EVENT. A second key rather than an " +
			"Alias of ErrorType because an alias is one value under two keys in one " +
			"place, and these sit in two places — the span and its event — which the " +
			"Definition holds one of.",
	},
	ErrorCode: {
		Name:        "ErrorCode",
		SpanKey:     "code",
		LogKey:      "error_code",
		Signals:     Span | Log,
		Event:       ErrorEvent,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "beckn.Error.Code, e.g. NET_CATALOG_SOURCE_UNAVAILABLE. Unbounded " +
			"because DOM_ codes are relayed from downstream systems and are the one " +
			"case the spec names as legitimately non-canonical.",
	},
	ErrorMessage: {
		Name:        "ErrorMessage",
		SpanKey:     "msg",
		Signals:     Span,
		Event:       ErrorEvent,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "beckn.Error.Message. Public because these are our own strings, but " +
			"note that Visibility is closed-world over KEYS and the risk here is in " +
			"the VALUE: Task 26's conformance test regexes the deny-list across the " +
			"serialised bytes of the facilitator projection, which is what covers this.",
	},
	ErrorPath: {
		Name:        "ErrorPath",
		SpanKey:     "path",
		Signals:     Span,
		Event:       ErrorEvent,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note:        "beckn.ErrorDetails.Path, e.g. $.message.publishDirectives[1].",
	},

	// ---- discover: request_info ----------------------------------------------
	//
	// Fired after correlate() reads the envelope. The SHAPE of the question,
	// never its content: the textSearch string, filters.expression and any
	// coordinate are on the never-emitted list.
	IntentKinds: {
		Name:        "IntentKinds",
		SpanKey:     "intent.kinds",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      intentKinds,
		Visibility:  Public,
	},
	IntentFilterType: {
		Name:        "IntentFilterType",
		SpanKey:     "intent.filter_type",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindString,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Filters.Type — the grammar NAMED, never the expression. Unbounded " +
			"although filter_parser.go:20 answers only \"jsonpath\": this fires at " +
			"intake, so an unrecognised grammar reaches it before it is refused, and " +
			"who is asking for a grammar we do not serve is the question worth having.",
	},
	IntentSpatialOps: {
		Name:        "IntentSpatialOps",
		SpanKey:     "intent.spatial_ops",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Each SpatialConstraint.op verbatim. Unbounded for the same reason as " +
			"intent.filter_type — domain.SpatialOp declares nine and a caller can send " +
			"a tenth, which intent_mapper.go:366 refuses after this has fired.",
	},
	IntentScoped: {
		Name:        "IntentScoped",
		SpanKey:     "intent.scoped",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindBool,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      boolValues,
		Visibility:  Public,
		Note:        "Whether networkId was supplied.",
	},

	// ---- discover: retrieval_info --------------------------------------------
	RetrievalModesRun: {
		Name:        "RetrievalModesRun",
		SpanKey:     "retrieval.modes_run",
		Signals:     Span,
		Event:       RetrievalInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      retrievalModes,
		Visibility:  Public,
	},
	RetrievalModesDegraded: {
		Name:        "RetrievalModesDegraded",
		SpanKey:     "retrieval.modes_degraded",
		Signals:     Span,
		Event:       RetrievalInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      retrievalModes,
		Visibility:  Public,
		Note:        "The X-Beckn-Degraded list (C11).",
	},
	RetrievalEmbeddingMs: {
		Name:         "RetrievalEmbeddingMs",
		SpanKey:      "retrieval.embedding_ms",
		Signals:      Span,
		Event:        RetrievalInfo,
		Kind:         KindFloat64,
		Layer:        Local,
		Cardinality:  Unbounded,
		Visibility:   Public,
		ZeroIsAbsent: true,
		Note: "Present only when an embedding was computed — absent under " +
			"EMBEDDING_PROVIDER=noop, which is every Phase 1 deployment (A5). It does " +
			"not contradict the no-duration-attribute rule: that refuses a second copy " +
			"of a duration the span already reports, and this is a phase the span " +
			"reports nowhere. Without it the request_info → retrieval_info delta " +
			"bundles a network hop to Ollama and the storage call into one number.",
	},

	// ---- discover: response_info ---------------------------------------------
	ResultCatalogCount: {
		Name:        "ResultCatalogCount",
		SpanKey:     "result.catalog_count",
		Signals:     Span,
		Event:       ResponseInfo,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "No ZeroIsAbsent here, deliberately: zero is the answer, not the " +
			"absence of one. See ResultEmpty.",
	},
	ResultProviderIDs: {
		Name:           "ResultProviderIDs",
		SpanKey:        "result.provider_ids",
		Signals:        Span,
		Event:          ResponseInfo,
		Kind:           KindStrings,
		Layer:          Local,
		Cardinality:    Unbounded,
		Visibility:     Public,
		MaxEntries:     maxProviderIDs,
		TruncationFlag: "result.providersTruncated",
		Note: "The DISTINCT provider-node ids of what was returned — whose data " +
			"answered. Read off Catalog.BppID; OAN does not use bap/bpp terminology, " +
			"so no attribute here repeats it, and the struct field keeps that spelling " +
			"only because Catalog closes with additionalProperties: false. Distinct " +
			"and bounded: a discover answering with 200 catalogs from one provider " +
			"emits one id, or the attribute becomes a page-size measurement wearing an " +
			"identity's name. It answers how many providers SERVED, never how many exist.",
	},
	ResultEmpty: {
		Name:        "ResultEmpty",
		SpanKey:     "result.empty",
		Signals:     Span,
		Event:       ResponseInfo,
		Kind:        KindBool,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      boolValues,
		Visibility:  Public,
		Note: "The most valuable signal this service gives the network: somebody " +
			"asked and nobody serves it. Its cost is real — it says unmet demand " +
			"happened, not what for, and that half is recovered in ClickStack where " +
			"the query text may go because the data does not leave.",
	},

	// ---- publish: request_info -----------------------------------------------
	//
	// Fired at intake, BEFORE the A1 MASTER refusal. After it,
	// publish.catalog_types would read REGULAR on every span that exists; at
	// intake it answers who is trying to publish master data to a network that
	// refuses it, and the error event on the same span carries the refusal.
	PublishProviderIDs: {
		Name:           "PublishProviderIDs",
		SpanKey:        "publish.provider_ids",
		Signals:        Span,
		Event:          RequestInfo,
		Kind:           KindStrings,
		Layer:          Local,
		Cardinality:    Unbounded,
		Visibility:     Public,
		MaxEntries:     maxProviderIDs,
		TruncationFlag: "publish.providersTruncated",
		Note: "Who added the source. The same name stem as result.provider_ids on " +
			"purpose: both read the same struct field, so one name for one concept " +
			"makes the join between who published it and whose data answered obvious " +
			"instead of something a reader has to work out.",
	},
	PublishCatalogCount: {
		Name:        "PublishCatalogCount",
		SpanKey:     "publish.catalog_count",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
	},
	PublishResourceCount: {
		Name:        "PublishResourceCount",
		SpanKey:     "publish.resource_count",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
	},
	PublishOfferCount: {
		Name:        "PublishOfferCount",
		SpanKey:     "publish.offer_count",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindInt64,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note: "Payload volume. Judgement call recorded in opentelemetry.md: the " +
			"counts reveal catalog size and publish.visible_to reveals distribution, " +
			"which is arguably commercial information. In, because the spec asks for " +
			"volume and OAN's providers are largely public bodies. On a network with " +
			"competing commercial providers, drop both.",
	},
	PublishUpdateModes: {
		Name:        "PublishUpdateModes",
		SpanKey:     "publish.update_modes",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      updateModes,
		Visibility:  Public,
		Note: "A FULL republish deletes what it does not mention, which is why this " +
			"is worth a span attribute rather than only a log line. An omitted " +
			"updateMode arrives as \"\" and resolves to MERGE, so no third value " +
			"reaches here.",
	},
	PublishCatalogTypes: {
		Name:        "PublishCatalogTypes",
		SpanKey:     "publish.catalog_types",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      catalogTypes,
		Visibility:  Public,
		Note: "As sent — MASTER appears even though Phase 1 refuses it. An absent " +
			"directive is REGULAR and is never inferred from content (C9).",
	},
	PublishVisibleTo: {
		Name:        "PublishVisibleTo",
		SpanKey:     "publish.visible_to",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindStrings,
		Layer:       Local,
		Cardinality: Unbounded,
		Visibility:  Public,
		Note:        "Which networks it was published to. Omitted resolves to the request's own (C8).",
	},
	PublishValidityPresent: {
		Name:        "PublishValidityPresent",
		SpanKey:     "publish.validity_present",
		Signals:     Span,
		Event:       RequestInfo,
		Kind:        KindBool,
		Layer:       Local,
		Cardinality: Bounded,
		Values:      boolValues,
		Visibility:  Public,
		Note:        "Whether a validity window was set — the freshness signal.",
	},
}
