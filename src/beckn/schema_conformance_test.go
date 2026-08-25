package beckn

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The pinned CI fixture. beckn.yaml itself is fetched at boot from
// VALIDATION_SPEC_URL and is deliberately not baked into the image — a second
// source of truth at runtime is the thing this plan avoids. A committed copy in
// tests/ is a different object: it is the version this build's structs were
// written against, and the only way to notice that the two have parted company.
const specFixture = "../../tests/testdata/beckn-v2.0.0.yaml"

// binding ties one wire struct to the node in the specification that defines
// it. Some nodes are named components; some — publishDirectives.items, the
// stats object — are declared inline, which is why the target is a path rather
// than a component name.
type binding struct {
	name  string
	value any
	path  []string
}

func schemaPath(rest ...string) []string {
	return append([]string{"components", "schemas"}, rest...)
}

// Every struct in this package that claims to be a wire shape. A type that
// crosses the network and is missing from this list is untested by the walk,
// so adding a wire struct without adding it here is the drift the walk exists
// to catch — which TestEveryWireStructIsBound asserts directly.
func bindings() []binding {
	return []binding{
		{"Context", Context{}, schemaPath("Context")},
		{"Catalog", Catalog{}, schemaPath("Catalog")},
		{"Resource", Resource{}, schemaPath("Resource")},
		{"Offer", Offer{}, schemaPath("Offer")},
		{"Attributes", Attributes{}, schemaPath("Attributes")},
		{"TimePeriod", TimePeriod{}, schemaPath("TimePeriod")},
		{"GeoJSONGeometry", GeoJSONGeometry{}, schemaPath("GeoJSONGeometry")},
		{"Intent", Intent{}, schemaPath("Intent")},
		{"Filters", Filters{}, schemaPath("Intent", "properties", "filters")},
		{"SpatialConstraint", SpatialConstraint{}, schemaPath("SpatialConstraint")},

		{"CatalogPublishAction", CatalogPublishAction{}, schemaPath("CatalogPublishAction")},
		{"PublishDirective", PublishDirective{}, schemaPath(
			"CatalogPublishAction", "properties", "publishDirectives", "items")},
		{"ResourceDirective", ResourceDirective{}, schemaPath(
			"CatalogPublishAction", "properties", "publishDirectives", "items",
			"properties", "resourceDirectives", "items")},
		{"Extends", Extends{}, schemaPath(
			"CatalogPublishAction", "properties", "publishDirectives", "items",
			"properties", "resourceDirectives", "items", "properties", "extends")},

		{"CatalogOnPublishAction", CatalogOnPublishAction{}, schemaPath("CatalogOnPublishAction")},
		{"CatalogProcessingResult", CatalogProcessingResult{}, schemaPath("CatalogProcessingResult")},
		{"CatalogStats", CatalogStats{}, schemaPath(
			"CatalogProcessingResult", "properties", "stats")},

		{"DiscoverAction", DiscoverAction{}, schemaPath("DiscoverAction")},
		{"OnDiscoverAction", OnDiscoverAction{}, schemaPath("OnDiscoverAction")},

		{"Error", Error{}, schemaPath("Error")},
		{"ErrorDetails", ErrorDetails{}, schemaPath("Error", "properties", "details")},
	}
}

// deviation is one mismatch between a struct and its schema node. Key is stable
// — "extra:Error.type" — so the allowlist is not keyed by a sentence that a
// reworded message would silently invalidate; Detail is what a failure prints.
type deviation struct {
	key    string
	detail string
}

// allowed is the whole of what this service's wire structs may do that the
// v2.0.0 schema does not say. Each entry names the conflict that decided it.
//
// Nothing goes in here to make a test pass. A deviation the walk reports and
// this map does not hold means the struct is wrong — the schema is the
// contract, and widening the allowlist is how a service ships a body its own
// consumers reject.
//
// C4 and C5 are documented conflicts too, but neither lands here, because in
// both the plan resolved the conflict by following the schema: C4 keeps
// Attributes' JSON-LD pair scalar against a reference implementation that
// accepts arrays, and C5 drops `category` against a PRD that assumed one. A
// property-name walk against beckn.yaml cannot see either, so they are pinned
// by TestAttributesKeepsTheScalarJSONLDPair and TestSpecDeclaresNoCategoryProperty
// instead — as assertions that hold, rather than as allowlist entries that
// would quietly match nothing.
var allowed = map[string]string{
	// C1: the PRD's five error categories have no home in a body the spec
	// closed with additionalProperties:false. They travel as the
	// X-Beckn-Error-Type header and the error_type log field; this key is
	// written only when ERROR_INCLUDE_LEGACY_TYPE is true, for v1-era clients.
	"extra:Error.type": "C1 — legacy error category, off by default",
}

func TestWireStructsMatchTheSpecification(t *testing.T) {
	spec := loadSpec(t)

	var found []deviation
	for _, b := range bindings() {
		found = append(found, deviations(t, b, resolve(t, spec, b.path))...)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].key < found[j].key })

	seen := make(map[string]bool, len(found))
	for _, d := range found {
		seen[d.key] = true
		if _, ok := allowed[d.key]; !ok {
			t.Errorf("undocumented deviation from %s:\n  %s", specFixture, d.detail)
		}
	}

	// The allowlist is checked in both directions. An entry that no longer
	// corresponds to a real deviation is a documented exception to a rule that
	// has since changed, and leaving it behind is how the list grows into
	// something nobody trusts.
	for key, why := range allowed {
		if !seen[key] {
			t.Errorf("allowlist entry %q (%s) matches no deviation; the struct or the spec has changed", key, why)
		}
	}
}

// deviations compares one struct's JSON tags against its schema node's
// properties, in both directions. An extra tag ships a key the schema does not
// declare; a missing one drops a key the schema does, which for a service that
// stores catalogs and renders them back means losing a publisher's data in
// transit.
func deviations(t *testing.T, b binding, node map[string]any) []deviation {
	t.Helper()

	where := strings.Join(b.path, ".")
	properties, ok := node["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatalf("%s: %s declares no properties", b.name, where)
	}

	tags := jsonTags(t, b)

	var out []deviation
	for tag := range tags {
		if _, ok := properties[tag]; !ok {
			out = append(out, deviation{
				key:    fmt.Sprintf("extra:%s.%s", b.name, tag),
				detail: fmt.Sprintf("%s.%s is not a property of %s", b.name, tag, where),
			})
		}
	}
	for property := range properties {
		if !tags[property] {
			out = append(out, deviation{
				key:    fmt.Sprintf("missing:%s.%s", b.name, property),
				detail: fmt.Sprintf("%s declares %s, and no field of %s carries it", where, property, b.name),
			})
		}
	}
	return out
}

func jsonTags(t *testing.T, b binding) map[string]bool {
	t.Helper()

	typ := reflect.TypeOf(b.value)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s: bound value is %s, want a struct", b.name, typ.Kind())
	}

	tags := make(map[string]bool, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			t.Fatalf("%s.%s: unexported field on a wire struct", b.name, field.Name)
		}

		tag, ok := field.Tag.Lookup("json")
		if !ok {
			t.Fatalf("%s.%s: no json tag; the wire name would default to the Go name",
				b.name, field.Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		tags[name] = true
	}
	return tags
}

func loadSpec(t *testing.T) map[string]any {
	t.Helper()

	body, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(body, &spec); err != nil {
		t.Fatalf("parse %s: %v", specFixture, err)
	}
	if spec["openapi"] == nil {
		t.Fatalf("%s: no openapi key; the fixture is not a specification document", specFixture)
	}
	return spec
}

func resolve(t *testing.T, spec map[string]any, path []string) map[string]any {
	t.Helper()

	node := spec
	for i, step := range path {
		next, ok := node[step]
		if !ok {
			t.Fatalf("%s: no such node (stopped at %q)", strings.Join(path, "."), strings.Join(path[:i+1], "."))
		}
		if node, ok = next.(map[string]any); !ok {
			t.Fatalf("%s: %q is not an object", strings.Join(path, "."), strings.Join(path[:i+1], "."))
		}
	}
	return node
}

// C4. The reference implementation accepts an array here and picks element
// zero; this plan follows the schema, which says scalar and says required. The
// Attributes struct types both as a plain string, so the day the spec relaxes
// them to a oneOf, that struct starts silently narrowing what it can read —
// which is the failure this test is here to make loud.
func TestAttributesKeepsTheScalarJSONLDPair(t *testing.T) {
	node := resolve(t, loadSpec(t), schemaPath("Attributes"))

	properties, ok := node["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Attributes: declares no properties")
	}
	for _, name := range []string{"@context", "@type"} {
		property, declared := properties[name].(map[string]any)
		if !declared {
			t.Fatalf("Attributes.%s: not declared", name)
		}
		if got := property["type"]; got != "string" {
			t.Errorf("Attributes.%s: schema says type %v, the struct reads a scalar string", name, got)
		}
	}

	names, ok := node["required"].([]any)
	if !ok {
		t.Fatalf("Attributes: declares no required list; both members are required by C4")
	}
	required := make(map[string]bool, len(names))
	for _, name := range names {
		required[fmt.Sprint(name)] = true
	}
	if !required["@context"] || !required["@type"] {
		t.Errorf("Attributes: required is %v, want both @context and @type", node["required"])
	}
}

// C5. There is no category field anywhere in v2.0.0 — which is why this service
// has no category column, no category index and no derivation, and why
// stats.categoryCount is answered as a distinct-@type count. A spec that grew
// one would make all four of those decisions wrong at once, so the absence is
// asserted rather than remembered.
func TestSpecDeclaresNoCategoryProperty(t *testing.T) {
	var walk func(node any, where string)
	walk = func(node any, where string) {
		switch typed := node.(type) {
		case map[string]any:
			if properties, ok := typed["properties"].(map[string]any); ok {
				if _, found := properties["category"]; found {
					t.Errorf("%s declares a category property; C5 assumed none exists", where)
				}
			}
			for key, child := range typed {
				walk(child, where+"."+key)
			}
		case []any:
			for i, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", where, i))
			}
		}
	}
	walk(loadSpec(t)["components"], "components")
}

// A wire struct this package exports and the binding table does not name is a
// struct the walk never checks, so the walk would keep passing while the very
// drift it exists to catch went in beside it. The source is parsed rather than
// reflected over because Go cannot enumerate a package's types at run time.
func TestEveryWireStructIsBound(t *testing.T) {
	bound := map[string]bool{}
	for _, b := range bindings() {
		bound[b.name] = true
	}

	for _, name := range exportedStructNames(t) {
		if !bound[name] {
			t.Errorf("%s is an exported struct in package beckn with no entry in bindings(); "+
				"bind it to its schema node, or move it out of the wire package", name)
		}
	}
}

func exportedStructNames(t *testing.T) []string {
	t.Helper()

	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(file os.FileInfo) bool {
		return !strings.HasSuffix(file.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package beckn: %v", err)
	}

	var names []string
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				spec, ok := node.(*ast.TypeSpec)
				if !ok || !spec.Name.IsExported() {
					return true
				}
				if _, isStruct := spec.Type.(*ast.StructType); isStruct {
					names = append(names, spec.Name.Name)
				}
				return true
			})
		}
	}
	sort.Strings(names)
	return names
}
