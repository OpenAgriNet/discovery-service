package validation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/getkin/kin-openapi/openapi3"
	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// The media type the protocol is defined over. Named once, because a document
// that declares its bodies under some other type is a document this service
// cannot validate against and the boot should say which type it looked for.
const specMediaType = "application/json"

// Fetcher retrieves the spec document from the configured registry URL.
//
// An interface-shaped seam rather than a hard-wired http.Get, because this is
// the one network call the service makes at boot and a test that needs a
// registry outage should not need a listening socket to produce one. The
// configured URL is trusted; nothing on a request path may supply one.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// servedAction is one action this build answers, and the path in the document
// that declares its request body.
type servedAction struct {
	action string
	path   string
}

// servedActions is the action-to-path table the index is built from. A function
// rather than a package var: no package-level mutable state.
//
// `publish` and `catalog/publish` both point at the one path the document
// declares (C2). The service routes only POST /publish; the path here names
// where the schema lives in the spec, not where a caller sends anything.
//
// Response actions — catalog/on_publish, on_discover — are deliberately absent.
// They are shapes this service writes, and indexing one would let a caller POST
// a callback in and have it validate as a request.
func servedActions() []servedAction {
	return []servedAction{
		{action: beckn.ActionDiscover, path: "/discover"},
		{action: beckn.ActionCatalogPublish, path: "/catalog/publish"},
		{action: beckn.ActionPublish, path: "/catalog/publish"},
	}
}

// indexEntry is one action's compiled request schema.
type indexEntry struct {
	// The spelling the document's own `const` declares for this action, which
	// is not always the key it is indexed under: `publish` and `catalog/publish`
	// are one request (C2) and the schema constrains the field to the latter.
	// L1 rewrites the field to this before validating, so both spellings are
	// accepted without the published document being edited to say so.
	canonical string

	schema *openapi3.SchemaRef
}

// SpecIndex is the compiled request schema per action.
//
// Keyed on `context.action` rather than on URL (C2). One route serves two
// action spellings, so a URL key could not tell them apart — and keying on the
// action is what makes protocol version coexistence additive later: a second
// version is a second index, not a change to this one.
//
// Read-only once built. Nothing mutates it after the boot returns, which is why
// it is safe to share across every request without a lock.
type SpecIndex struct {
	byAction map[string]indexEntry
}

// lookup returns the entry for action. Unexported, and so is indexEntry: the
// openapi3 types are an implementation detail of this package, and a schema
// handed out across the boundary is one a caller can validate with in a way
// this package cannot keep consistent with L1.
func (index *SpecIndex) lookup(action string) (indexEntry, bool) {
	entry, found := index.byAction[action]
	return entry, found
}

// NewSpecIndex compiles the document into an index keyed by action.
//
// It refuses a document that does not declare every path this build serves.
// Booting on a spec for some other protocol would produce a service that NACKs
// every request it was deployed to answer — a failure that reads as the
// caller's fault and is not.
func NewSpecIndex(document []byte) (*SpecIndex, error) {
	loader := openapi3.NewLoader()

	// Left off deliberately: this document is read from a configured registry
	// or from disk, and resolving external $refs out of it would turn one
	// trusted URL into whatever that document names.
	loader.IsExternalRefsAllowed = false

	specification, err := loader.LoadFromData(document)
	if err != nil {
		return nil, fmt.Errorf("parse the validation spec: %w", err)
	}

	byAction := make(map[string]indexEntry, len(servedActions()))
	for _, served := range servedActions() {
		schema, err := requestSchema(specification, served.path)
		if err != nil {
			return nil, err
		}
		byAction[served.action] = indexEntry{canonical: canonicalAction(schema, served.action), schema: schema}
	}
	return &SpecIndex{byAction: byAction}, nil
}

// requestSchema finds the JSON request body schema declared at path.
func requestSchema(specification *openapi3.T, path string) (*openapi3.SchemaRef, error) {
	item := specification.Paths.Find(path)
	if item == nil || item.Post == nil {
		return nil, fmt.Errorf("the validation spec declares no POST %s", path)
	}

	body := item.Post.RequestBody
	if body == nil || body.Value == nil {
		return nil, fmt.Errorf("POST %s declares no request body", path)
	}

	media := body.Value.Content.Get(specMediaType)
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		return nil, fmt.Errorf("POST %s declares no %s request schema", path, specMediaType)
	}
	return media.Schema, nil
}

// canonicalAction reads the spelling the schema constrains `context.action` to,
// falling back to the action it is indexed under where the schema names none.
//
// The const sits behind an allOf — the document composes the shared Context
// with a one-field override — so the branches are walked rather than the
// property being read straight off. Reading it here rather than hard-coding the
// mapping means a spec that renames an action renames it in one place.
func canonicalAction(schema *openapi3.SchemaRef, indexedAs string) string {
	contextSchema := schema.Value.Properties["context"]
	if contextSchema == nil || contextSchema.Value == nil {
		return indexedAs
	}

	for _, branch := range append(openapi3.SchemaRefs{contextSchema}, contextSchema.Value.AllOf...) {
		if branch.Value == nil {
			continue
		}
		action := branch.Value.Properties["action"]
		if action == nil || action.Value == nil {
			continue
		}
		if declared, ok := action.Value.Const.(string); ok && declared != "" {
			return declared
		}
	}
	return indexedAs
}

// LoadSpecIndex builds the index the service boots with: the registry first,
// the on-disk cache as the fallback, and a refusal to start if neither yields a
// document that compiles.
//
// Refusing is the point. L1 configured on and silently validating nothing is a
// service reporting healthy while accepting every body it was deployed to
// refuse, and the deployment that hits it is the one where the registry was
// down — precisely when nobody is reading start-up logs for a warning.
func LoadSpecIndex(ctx context.Context, cfg config.Validation, fetch Fetcher) (*SpecIndex, error) {
	index, fetchErr := loadFromRegistry(ctx, cfg, fetch)
	if fetchErr == nil {
		return index, nil
	}

	// Loud, because the service is about to run against a document that may be
	// a protocol release behind, and nothing later in the request path can tell
	// that this happened.
	logger.FromContext(ctx).Warn("validation spec fetch failed, falling back to the cache",
		zap.String("spec_url", cfg.SpecURL), zap.String("cache_path", cfg.SpecCachePath), zap.Error(fetchErr))

	index, cacheErr := loadFromCache(cfg.SpecCachePath)
	if cacheErr != nil {
		// Both named, because an operator's next move differs depending on which
		// one they can reach.
		return nil, fmt.Errorf("load the validation spec: from %s: %w; from the cache at %s: %w",
			cfg.SpecURL, fetchErr, cfg.SpecCachePath, cacheErr)
	}
	return index, nil
}

// loadFromRegistry fetches the document, compiles it, and caches it on the way
// past — in that order. Caching before compiling would let one bad response
// from the registry overwrite the copy the next boot depends on.
func loadFromRegistry(ctx context.Context, cfg config.Validation, fetch Fetcher) (*SpecIndex, error) {
	if cfg.SpecURL == "" {
		return nil, errors.New("no spec URL is configured")
	}
	if fetch == nil {
		return nil, errors.New("no fetcher was supplied")
	}

	document, err := fetch(ctx, cfg.SpecURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	index, err := NewSpecIndex(document)
	if err != nil {
		return nil, err
	}

	// A warning rather than a failure: this boot has a compiled index and can
	// serve. It is the *next* boot that loses its fallback, and refusing to
	// start a healthy service over that would turn a degraded disk into an
	// outage.
	if err := writeCache(cfg.SpecCachePath, document); err != nil {
		logger.FromContext(ctx).Warn("validation spec cache could not be written",
			zap.String("cache_path", cfg.SpecCachePath), zap.Error(err))
	}
	return index, nil
}

func loadFromCache(path string) (*SpecIndex, error) {
	if path == "" {
		return nil, errors.New("no cache path is configured")
	}

	document, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return NewSpecIndex(document)
}

// writeCache replaces the cached document through a temporary file in the same
// directory, so a boot interrupted mid-write leaves the previous copy intact
// rather than a truncated one the next boot cannot compile.
func writeCache(path string, document []byte) error {
	if path == "" {
		return errors.New("no cache path is configured")
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create the cache directory: %w", err)
	}

	// Created 0600 by os.CreateTemp, which is the mode this file wants: it holds
	// a public document, but it is one the boot trusts, and a cache anyone can
	// write is a way to feed this service a schema of someone else's choosing.
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create the cache file: %w", err)
	}

	name := temporary.Name()
	if err := writeAndClose(temporary, document); err != nil {
		// The rename never happened, so nothing else is going to clear this up.
		// A cleanup that fails too is worth saying out loud: it means the
		// directory is in a state — full, read-only — that the next boot will
		// hit as well.
		if removeErr := os.Remove(name); removeErr != nil {
			return fmt.Errorf("%w; the partial file at %s could not be removed: %w", err, name, removeErr)
		}
		return err
	}
	return os.Rename(name, path)
}

// writeAndClose closes the file whatever the write did. A close error is
// reported only when the write itself succeeded: on a full disk both fail, and
// the write is the one that says what went wrong.
func writeAndClose(file *os.File, document []byte) error {
	_, writeErr := file.Write(document)
	closeErr := file.Close()

	if writeErr != nil {
		return fmt.Errorf("write the cache file: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close the cache file: %w", closeErr)
	}
	return nil
}
