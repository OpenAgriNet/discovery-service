// Package config loads the service configuration from four layers — struct
// tags, the reviewed repository defaults, an optional deployment file, and the
// process environment — and refuses to start on a value it cannot use.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"gopkg.in/yaml.v3"

	// The attribute registry, not the telemetry package: validateOTel reads the
	// `domain` row's values and the `producer` row's key spelling, so the boot
	// refusal and the Resource cannot disagree. fact imports only the standard
	// library — tests/architecture pins that — so no SDK reaches this layer.
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The two YAML layers, relative to the working directory. The image copies
// config/common.yaml beside the binary; instance.yaml is mounted there by the
// deployment, or absent.
const (
	commonPath   = "config/common.yaml"
	instancePath = "config/instance.yaml"
)

const (
	envTag        = "env"
	sliceSeparate = ","
)

// Config is the whole of the service's configuration. Nothing reads it through
// a package-level variable: it is built once in main and passed down.
type Config struct {
	App         App
	Server      Server
	Database    Database
	Search      Search
	Embeddings  Embeddings
	RateLimit   RateLimit
	Log         Log
	Validation  Validation
	Auth        Auth
	OTel        OTel
	Replication Replication
	Errors      Errors
	Ext         Ext
	Geo         Geo
}

// App identifies the deployment itself.
type App struct {
	// The network this deployment serves. No default, because there is no
	// repo-wide answer, and publish falls back to it to fill an empty
	// publishDirectives.visibleTo (C8). Discover has no such fallback: an omitted
	// networkId there searches every network.
	Network string `env:"APP_NETWORK_ID"`

	// This deployment's registered subscriber id — an FQDN such as
	// discovery.oan.example.org. It becomes the Resource's `producer` and every
	// span's `recipient.id`: who actually answered, never the caller's claim about
	// who it addressed.
	//
	// No default, because nothing issues it yet and an invented participant id is
	// worse than an absent one — it reaches the network's only cross-participant
	// identity join looking like a real answer. Required only when the exporter is
	// on; see validateOTel.
	Subscriber string `env:"APP_SUBSCRIBER_ID"`

	// The sector this deployment serves — the Resource's `domain`. Not the network
	// (that is Network) and not the entity type.
	//
	// Checked against the registry's declared values rather than a literal here, so
	// the sector list grows in one place. Required only when the exporter is on:
	// nothing but telemetry reads it today.
	Domain string `env:"APP_DOMAIN"`

	// Every daily validity window is interpreted here. Validated with
	// time.LoadLocation at startup, so a typo fails the boot rather than silently
	// shifting every window by hours.
	DefaultTimezone string `env:"APP_DEFAULT_TIMEZONE" envDefault:"Asia/Kolkata"`
}

// Server holds the HTTP listener's settings.
type Server struct {
	Port int `env:"SERVER_PORT" envDefault:"8080"`

	// How long SIGTERM waits for in-flight requests before the listener is
	// closed on them.
	ShutdownTimeout time.Duration `env:"SERVER_SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// The ceiling on a request body, in bytes (C14). Enforced by the Envelope
	// middleware, the only thing in the service that reads a body and runs before
	// RateLimit — so without it nothing bounds what an unauthenticated caller can
	// make the process allocate.
	//
	// A knob rather than a constant because the size is a property of a
	// deployment's catalogs, not of the protocol.
	MaxRequestBodyBytes int64 `env:"SERVER_MAX_REQUEST_BODY_BYTES" envDefault:"10485760"`
}

// Database holds the connection string and the pool's bounds.
type Database struct {
	// A secret: it arrives from the environment and appears in neither YAML
	// file, which is why the environment layer sits on top of both (TRD §8).
	URL string `env:"DATABASE_URL"`

	// Sized by the concurrency model, not guessed. Discover runs its retrieval
	// modes concurrently (A2), so one in-flight discover holds one connection per
	// enabled mode:
	//
	//	MaxConns >= (enabled modes) x (expected in-flight discovers)
	//
	// bounded above by the server's max_connections less whatever else shares it.
	// Under pgxpool's own max(4, numCPU) the performance scenario's sixteen
	// concurrent discovers would queue in pool.Acquire() and measure the queue
	// rather than the query.
	MaxConns int32 `env:"DATABASE_MAX_CONNS" envDefault:"32"`

	// A warm-start knob only: idle backends cost the server memory to save a
	// connection handshake, so this stays small where MaxConns does not.
	MinConns int32 `env:"DATABASE_MIN_CONNS" envDefault:"4"`

	// Applies the embedded migrations at boot. Off by default: a process that
	// rewrites the schema as it starts is a decision an operator makes, and
	// `make migrate` is the other way to make it.
	AutoMigrate bool `env:"DATABASE_AUTO_MIGRATE" envDefault:"false"`
}

// Search bounds the read path. The three numeric bounds below are three
// different quantities and the names say which: two bound a page, one bounds a
// retrieval mode's candidate list.
type Search struct {
	// The page size a request that names no limit gets.
	DefaultPageSize int `env:"SEARCH_DEFAULT_PAGE_SIZE" envDefault:"20"`

	// The ceiling a request's limit is clamped to — clamped rather than
	// refused, because the caller still gets the results they asked about.
	MaxPageSize int `env:"SEARCH_MAX_PAGE_SIZE" envDefault:"100"`

	// How many ids one retrieval mode may return into fusion. Also the reachable
	// pagination depth: a request whose offset + limit passes it is refused, since
	// slicing past the end of the fused list would answer with an empty page that
	// reads exactly like the end of the results.
	MaxCandidatesPerMode int `env:"SEARCH_MAX_CANDIDATES_PER_MODE" envDefault:"500"`

	// The largest S_DWITHIN radius a caller may ask for.
	MaxRadiusMeters int `env:"SEARCH_MAX_RADIUS_METERS" envDefault:"200000"`

	// One deadline for the whole concurrent retrieval (A2). Separate from
	// Embeddings.WriteDeadline because inference on a publish and a fan-out of
	// queries on a discover fail at different speeds (A3).
	ReadDeadline time.Duration `env:"SEARCH_READ_DEADLINE" envDefault:"2s"`

	// What happens when a requested retrieval mode is missing. False names it in
	// the X-Beckn-Degraded header and returns what the other modes found; true
	// makes the same request a 400. False by default because Phase 1 ships
	// EMBEDDING_PROVIDER=noop, so semantic is missing on every fresh deployment
	// and refusing would break the common case (C11).
	FailOnUnavailableMode bool `env:"SEARCH_FAIL_ON_UNAVAILABLE_MODE" envDefault:"false"`
}

// Embeddings configures the Embedder seam — one struct for both paths (A3).
type Embeddings struct {
	// noop (the default — semantic search is deferred, A5), hashing (CI),
	// ollama, or fixture.
	Provider string `env:"EMBEDDING_PROVIDER" envDefault:"noop"`

	Model    string `env:"EMBEDDING_MODEL" envDefault:"nomic-embed-text"`
	Endpoint string `env:"EMBEDDING_ENDPOINT" envDefault:"http://localhost:11434"`

	// The vector width the column and the HNSW index are built for. A vector of
	// any other length is refused before pgvector sees it.
	Dimensions int `env:"EMBEDDING_DIMENSIONS" envDefault:"768"`

	// Bounds one Embed call on the publish path. Named for the side it serves,
	// because Search.ReadDeadline bounds the other one (A3).
	WriteDeadline time.Duration `env:"EMBEDDING_WRITE_DEADLINE" envDefault:"2s"`
}

// RateLimit is the per-caller token bucket on the protocol routes (A4).
type RateLimit struct {
	RPS   int `env:"RATE_LIMIT_RPS" envDefault:"20"`
	Burst int `env:"RATE_LIMIT_BURST" envDefault:"40"`
}

// Log configures the structured logger.
type Log struct {
	Level string `env:"LOG_LEVEL" envDefault:"info"`
}

// Validation switches the L1 schema layer and locates the protocol spec.
//
// One layer, not two: L2 extended @context/@type validation is the adapter's and
// is configured there, so this struct does not name it.
type Validation struct {
	EnableL1Schema bool `env:"VALIDATION_ENABLE_L1_SCHEMA" envDefault:"true"`

	// beckn.yaml is fetched at boot rather than baked into the image, so a
	// deployment names the document it validates against.
	//
	// The default is a TAG, and that is the whole reason there can be one. This
	// field carried no default until 2026-09-09, on the argument that which spec
	// a network trusts is not a repository-wide decision — true of a branch,
	// which would let an upstream merge change the validator under a running
	// deployment without a deploy. refs/tags/core-v2.0.0-lts cannot move, so the
	// default names one immutable document and a network that trusts a different
	// one still overrides it. tests/testdata/beckn-v2.0.0.yaml is byte-identical
	// to it, so the fetch path and an air-gapped mount agree.
	//
	// The cost is that boot now makes a network call before falling back to
	// SpecCachePath. LoadSpecIndex already warns loudly and falls back, so an
	// offline boot is slower and noisier, not broken.
	SpecURL string `env:"VALIDATION_SPEC_URL" envDefault:"https://raw.githubusercontent.com/beckn/protocol-specifications-v2/refs/tags/core-v2.0.0-lts/api/v2.0.0/beckn.yaml"`

	// Where the fetched spec is cached, and what an air-gapped deployment
	// mounts in place of the fetch.
	SpecCachePath string `env:"VALIDATION_SPEC_CACHE_PATH" envDefault:".cache/beckn/beckn.yaml"`
}

// Auth switches signature verification, which is deferred with nothing behind
// it — see validateAuth for why the only value this build accepts is false.
type Auth struct {
	EnableSignatureVerification bool `env:"AUTH_ENABLE_SIGNATURE_VERIFICATION" envDefault:"false"`
}

// OTel configures the telemetry exporter.
type OTel struct {
	// none (the default, so a collector-less deployment still boots) or otlp.
	Exporter string `env:"OTEL_EXPORTER" envDefault:"none"`

	// Read from the OTel SDK's own variable rather than an invented one: a
	// deployment that already exports traces has this set.
	Endpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

// Replication configures publish's write fan-out seam (A7).
type Replication struct {
	// The stores a committed catalog is announced to. Empty — the Phase 1 value —
	// is the only state this build acts on: Task 20 constructs `NoopReplicator`
	// unconditionally (src/app/container.go) and nothing else reads this field.
	// A named target neither replicates anywhere nor fails the boot yet — it is
	// accepted and silently ignored, which is a gap, not a feature: the second
	// store that reads it still needs to land, and until it does this knob is
	// forward-declared rather than enforced. Do not rely on a named target
	// doing anything, or on setting one being refused.
	Targets []string `env:"REPLICATION_TARGETS"`
}

// Errors shapes the error body, not the error handling (C1).
type Errors struct {
	// The spec's Error is {code, message, details} with additionalProperties:
	// false, so the five PRD categories travel in X-Beckn-Error-Type instead. true
	// re-injects type into the body for v1-style clients that require it, which is
	// a deliberate spec violation and therefore off by default.
	IncludeLegacyType bool `env:"ERROR_INCLUDE_LEGACY_TYPE" envDefault:"false"`
}

// Ext configures where L2's extended schemas come from.
type Ext struct {
	// The SSRF boundary. A registry URL configured by an operator is trusted and
	// is fetched; a @context URL that arrived in a request body is not, and while
	// this is false it cannot be.
	AllowNetworkFetch bool `env:"EXT_ALLOW_NETWORK_FETCH" envDefault:"false"`
}

// Geo configures the H3 index the spatial path is built on.
type Geo struct {
	// r8 is ~0.74 km2 per cell, ~531 m average edge, ~1.1 km MAYBE band.
	// Configuration rather than a constant because the accuracy-against-storage
	// trade is a property of a deployment's data. Every stored cover is at this
	// resolution, so changing it means reindexing.
	ResolutionCells int `env:"GEO_RESOLUTION_CELLS" envDefault:"8"`

	// How many shapes one catalog may contribute to the index. Over it the extra
	// finds come back as PARTIAL faults naming their paths — never a silent drop.
	//
	// Configuration rather than a constant because the budget is a property of a
	// deployment's catalogs, not of the protocol: a catalog of one polygon per
	// state and a catalog of one point per mandi sit orders of magnitude apart,
	// and only the operator knows which one they publish. The cost it stands in
	// for is cells, not shapes — this many geometries times MaxIndexCoverCells is
	// the worst case a single publish can ask the index to hold.
	MaxGeometriesPerCatalog int `env:"GEO_MAX_GEOMETRIES_PER_CATALOG" envDefault:"256"`
}

// Load reads the four layers in precedence order and validates the result.
// Lowest first: the envDefault tags, config/common.yaml, config/instance.yaml,
// then the process environment.
func Load() (Config, error) {
	return load(commonPath, instancePath, envMap(os.Environ()))
}

// Defaults returns the floor: every field as its envDefault tag declares it, with
// no file and no environment read. Not validated, because the floor deliberately
// carries no network id and no database URL.
func Defaults() (Config, error) {
	return parse(map[string]string{})
}

// load is Load with its inputs as parameters, which is what the layer tests
// drive. The environment is passed rather than read so a test asserts against its
// own fixture and not against whatever the test runner exports.
func load(common, instance string, environment map[string]string) (Config, error) {
	// One env.Parse, not one per layer: it applies every envDefault tag whose
	// variable is absent, so a second pass over a populated struct would reset each
	// field the environment does not name. Handing it the YAML values as
	// environment entries keeps the tags as the floor.
	overrides := map[string]string{}
	if err := overlay(common, false, overrides); err != nil {
		return Config{}, err
	}
	if err := overlay(instance, true, overrides); err != nil {
		return Config{}, err
	}
	// A blank variable cannot express an empty value — env.Parse reads a
	// present-but-blank entry as absent and applies the envDefault tag — so
	// copying it over the YAML layer would erase a reviewed value and resurrect
	// the tag default. Skipping it leaves the layer below intact.
	for name, value := range environment {
		if value != "" {
			overrides[name] = value
		}
	}

	cfg, err := parse(overrides)
	if err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parse(environment map[string]string) (Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environment}); err != nil {
		return Config{}, fmt.Errorf("parse configuration: %w", err)
	}
	return cfg, nil
}

// overlay folds one YAML document into overrides, keyed by each matched field's
// env tag. A key matching no field is a startup failure: a typo must not
// silently do nothing.
func overlay(path string, optional bool, overrides map[string]string) error {
	// G304 is waived, not worked around: the only paths that reach here are the two
	// constants above and the fixtures the layer tests write. There is no
	// user-supplied path to sanitise.
	document, err := os.ReadFile(path) //nolint:gosec // see above
	if errors.Is(err, fs.ErrNotExist) && optional {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(document, &doc); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return collect(reflect.TypeFor[Config](), doc, path, "", overrides)
}

func collect(group reflect.Type, doc map[string]any, path, prefix string, overrides map[string]string) error {
	for key, value := range doc {
		field, ok := fieldNamed(group, key)
		if !ok {
			return fmt.Errorf("config %s: unknown key %q", path, prefix+key)
		}
		if err := collectField(field, value, path, prefix+key, overrides); err != nil {
			return err
		}
	}
	return nil
}

func collectField(field reflect.StructField, value any, path, key string, overrides map[string]string) error {
	if field.Type.Kind() == reflect.Struct {
		block, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("config %s: key %q is a group and takes a nested block, not a value", path, key)
		}
		return collect(field.Type, block, path, key+".", overrides)
	}

	name, ok := field.Tag.Lookup(envTag)
	if !ok {
		return fmt.Errorf("config %s: key %q names field %s, which declares no %s tag", path, key, field.Name, envTag)
	}
	if list, ok := value.([]any); ok {
		// A list is answerable only where the field is one: joined into a scalar
		// field it becomes a string nobody asked for, and empty it renders blank
		// and erases the layer below exactly as `key: ""` does.
		if field.Type.Kind() != reflect.Slice {
			return fmt.Errorf("config %s: key %q takes a value, not a list", path, key)
		}
		// Here empty IS a value: it clears what the layer below set. env.Parse
		// never sets a blank, so the field keeps its zero value.
		if len(list) == 0 {
			overrides[name] = ""
			return nil
		}
	}
	text, err := scalar(value)
	if err != nil {
		return fmt.Errorf("config %s: key %q: %w", path, key, err)
	}
	overrides[name] = text
	return nil
}

// fieldNamed matches a YAML key to a field case-insensitively, which is what
// lets the files read as camelCase against PascalCase fields.
func fieldNamed(group reflect.Type, key string) (reflect.StructField, bool) {
	for i := range group.NumField() {
		if field := group.Field(i); strings.EqualFold(field.Name, key) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// scalar renders a YAML leaf as the env parser reads it. A sequence becomes the
// comma-separated form env already understands for slices.
//
// A blank string is refused rather than written through: env.Parse ignores a
// blank, so the envDefault tag comes back where a field has one and the zero value
// stands where it does not, and neither is what the file said. Refusing costs only
// a spelling indistinguishable from omitting the key.
//
// An empty sequence is refused here too. The one place it means something is as
// the whole value of a slice field, and collectField — the only caller that knows
// the field's type — answers that before reaching this function.
func scalar(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", errors.New("key has no value")
	case string:
		if typed == "" {
			return "", errors.New("key has an empty value: omit the key to take the layer below")
		}
		return typed, nil
	case map[string]any:
		return "", errors.New("key takes a value, not a nested block")
	case []any:
		if len(typed) == 0 {
			return "", errors.New("key has an empty list: omit the key to take the layer below")
		}
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text, err := scalar(item)
			if err != nil {
				return "", err
			}
			items = append(items, text)
		}
		return strings.Join(items, sliceSeparate), nil
	default:
		return fmt.Sprint(value), nil
	}
}

// envMap turns os.Environ's KEY=VALUE lines into the map env.ParseWithOptions
// takes, so the environment is a value the loader is handed rather than a global
// it reads.
func envMap(lines []string) map[string]string {
	environment := make(map[string]string, len(lines))
	for _, line := range lines {
		if key, value, ok := strings.Cut(line, "="); ok {
			environment[key] = value
		}
	}
	return environment
}

// validate fails startup on a configuration the service cannot honour, reporting
// every problem at once: an operator fixing a bad file should not have to restart
// once per mistake.
func validate(cfg Config) error {
	problems := errors.Join(
		validateApp(cfg.App),
		validateServer(cfg.Server),
		validateDatabase(cfg.Database),
		validateSearch(cfg.Search),
		validateEmbeddings(cfg.Embeddings),
		validateRateLimit(cfg.RateLimit),
		validateGeo(cfg.Geo),
		validateAuth(cfg.Auth),
		validateOTel(cfg.OTel, cfg.App),
	)
	if problems != nil {
		return fmt.Errorf("invalid configuration: %w", problems)
	}
	return nil
}

// validateAuth refuses a boot that sets the flag, because Phase 1 has nothing
// behind it: the Ed25519 primitives are not built and the Signature middleware is
// not written, so there is no code path to switch on. A flag named for a security
// control that silently does nothing is worse than no flag. Deleting this check
// is not how signature verification is turned on; building the slot's contents is.
func validateAuth(auth Auth) error {
	return require(!auth.EnableSignatureVerification,
		"auth.enableSignatureVerification is true (AUTH_ENABLE_SIGNATURE_VERIFICATION) and Phase 1 has nothing behind it: "+
			"signature verification is deferred, so the flag would report a control that is not running")
}

// The exporters this build has code behind. Exported because telemetry.Init
// switches on the value, and a second spelling of "otlp" in that switch is a
// deployment that configures an exporter and gets none.
const (
	// ExporterNone builds a tracer provider that records nothing and reaches no
	// network. The default, so a deployment with no collector still boots.
	ExporterNone = "none"

	// ExporterOTLP exports over gRPC to OTEL_EXPORTER_OTLP_ENDPOINT — the OTel
	// default for that variable, and what ClickStack's collector accepts.
	ExporterOTLP = "otlp"
)

// validateOTel refuses a boot that would export an unattributable stream.
//
// The asymmetry is the point: `none` demands nothing, because a Phase 1
// deployment with no collector must not answer for telemetry it does not emit.
// `otlp` demands the whole Resource identity, because a span reaching a
// facilitator with an empty `producer` lands under no participant — worse than no
// span, since an empty string in a grouping column reads as data rather than as
// an absence.
//
// Domain is checked against the registry rather than a literal; see
// TestTheDomainIsCheckedAgainstTheRegistry.
func validateOTel(otel OTel, app App) error {
	known := otel.Exporter == ExporterNone || otel.Exporter == ExporterOTLP
	if !known {
		return fmt.Errorf("otel.exporter %q is not an exporter this build has (OTEL_EXPORTER): %s or %s",
			otel.Exporter, ExporterNone, ExporterOTLP)
	}
	if otel.Exporter == ExporterNone {
		return nil
	}

	domain := fact.Of(fact.ResourceDomain)
	return errors.Join(
		require(otel.Endpoint != "",
			"otel.exporter is %s with no endpoint (OTEL_EXPORTER_OTLP_ENDPOINT): "+
				"there is nowhere to export to", ExporterOTLP),
		require(app.Subscriber != "",
			"otel.exporter is %s and app.subscriber is empty (APP_SUBSCRIBER_ID): "+
				"it is the Resource's %s and every span's recipient.id, so without it "+
				"the whole stream is attributed to no participant",
			ExporterOTLP, domainProducerKey()),
		require(slices.Contains(domain.Values, app.Domain),
			"app.domain %q is not a sector this build declares (APP_DOMAIN): one of %v. "+
				"Every OAN component must emit the identical string or grouping splits "+
				"across the network, so a typo fails the boot",
			app.Domain, domain.Values),
	)
}

// domainProducerKey is the attribute name `producer`, read off the registry so the
// refusal above cannot name a key the Resource does not set.
func domainProducerKey() string {
	return fact.Of(fact.ResourceProducer).SpanKey
}

// validateGeo bounds the resolution to the 0-15 H3 defines, so an out-of-range
// value fails the boot rather than the first cover that reaches h3.
func validateGeo(geo Geo) error {
	return errors.Join(
		require(geo.ResolutionCells >= 0 && geo.ResolutionCells <= 15,
			"geo.resolutionCells %d is not an H3 resolution (GEO_RESOLUTION_CELLS): H3 defines 0 through 15", geo.ResolutionCells),
		// Zero rather than negative is the reachable mistake, and it is the worse
		// one: every publish would come back PARTIAL with every geometry faulted,
		// which reads as a data problem and is a configuration one.
		require(geo.MaxGeometriesPerCatalog > 0,
			"geo.maxGeometriesPerCatalog %d is not positive (GEO_MAX_GEOMETRIES_PER_CATALOG): a catalog that may hold no geometry is undiscoverable by place",
			geo.MaxGeometriesPerCatalog),
	)
}

func validateApp(app App) error {
	_, err := time.LoadLocation(app.DefaultTimezone)
	if err != nil {
		err = fmt.Errorf("app.defaultTimezone %q is not a loadable location: %w", app.DefaultTimezone, err)
	}
	return errors.Join(
		require(app.Network != "", "app.network is required (APP_NETWORK_ID): it fills an empty publishDirectives.visibleTo"),
		err,
	)
}

func validateServer(server Server) error {
	return errors.Join(
		require(server.Port > 0 && server.Port < 65536, "server.port %d is not a port", server.Port),
		require(server.ShutdownTimeout > 0, "server.shutdownTimeout %s is not positive", server.ShutdownTimeout),
		// Zero reads as "no limit" to anyone skimming the YAML and means "refuse
		// everything" to http.MaxBytesReader.
		require(server.MaxRequestBodyBytes > 0,
			"server.maxRequestBodyBytes %d is not positive (SERVER_MAX_REQUEST_BODY_BYTES): "+
				"zero is not \"unlimited\", it refuses every request with a body", server.MaxRequestBodyBytes),
	)
}

func validateDatabase(database Database) error {
	return errors.Join(
		require(database.URL != "", "database.url is required (DATABASE_URL) and belongs in the environment, not a file"),
		require(database.MinConns >= 0, "database.minConns %d is negative", database.MinConns),
		require(database.MaxConns > 0, "database.maxConns %d is not positive", database.MaxConns),
		require(database.MaxConns >= database.MinConns,
			"database.maxConns %d is below database.minConns %d", database.MaxConns, database.MinConns),
	)
}

func validateSearch(search Search) error {
	return errors.Join(
		require(search.DefaultPageSize > 0, "search.defaultPageSize %d is not positive", search.DefaultPageSize),
		require(search.MaxPageSize >= search.DefaultPageSize,
			"search.maxPageSize %d is below search.defaultPageSize %d — a ceiling under the value it clamps",
			search.MaxPageSize, search.DefaultPageSize),
		require(search.MaxCandidatesPerMode >= search.MaxPageSize,
			"search.maxCandidatesPerMode %d is below search.maxPageSize %d — a candidate pool smaller than one page cannot fill it",
			search.MaxCandidatesPerMode, search.MaxPageSize),
		require(search.MaxRadiusMeters > 0, "search.maxRadiusMeters %d is not positive", search.MaxRadiusMeters),
		require(search.ReadDeadline > 0, "search.readDeadline %s is not positive", search.ReadDeadline),
	)
}

func validateEmbeddings(embeddings Embeddings) error {
	return errors.Join(
		require(embeddings.Provider != "", "embeddings.provider is empty"),
		require(embeddings.Dimensions > 0, "embeddings.dimensions %d is not positive", embeddings.Dimensions),
		require(embeddings.WriteDeadline > 0, "embeddings.writeDeadline %s is not positive", embeddings.WriteDeadline),
	)
}

func validateRateLimit(rateLimit RateLimit) error {
	return errors.Join(
		require(rateLimit.RPS > 0, "rateLimit.rps %d is not positive", rateLimit.RPS),
		require(rateLimit.Burst >= rateLimit.RPS,
			"rateLimit.burst %d is below rateLimit.rps %d — a bucket smaller than one second's refill",
			rateLimit.Burst, rateLimit.RPS),
	)
}

// require reports a problem when the condition does not hold, so a validator
// reads as the list of invariants it enforces.
func require(ok bool, format string, args ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(format, args...)
}
