package publish

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/geo"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// MaxCatalogWalkDepth bounds how far into a published document the geometry walk
// descends. A bound, not a hope: the walk is recursive over publisher-supplied
// JSON (implementation-plan.md §Geospatial Design).
const MaxCatalogWalkDepth = 32

// How many shapes one catalog may contribute to the index is
// config.Geo.MaxGeometriesPerCatalog, carried in as maxGeometries below rather
// than fixed here: the budget is a property of a deployment's catalogs, not of
// the protocol. The geometry over it is reported as a partial fault, never
// dropped in silence (implementation-plan.md §Geospatial Design).

// geometryTypes is the RFC 7946 set, all seven of which are indexed.
var geometryTypes = map[string]bool{
	beckn.GeometryPoint:              true,
	beckn.GeometryMultiPoint:         true,
	beckn.GeometryLineString:         true,
	beckn.GeometryMultiLineString:    true,
	beckn.GeometryPolygon:            true,
	beckn.GeometryMultiPolygon:       true,
	beckn.GeometryGeometryCollection: true,
}

// ExtractGeometries finds every geographic shape in a merged catalog and says
// where it was and who owns it.
//
// The MERGED catalog rather than the patch, called from derive inside the write
// transaction: a patch that never mentioned a geo field must not erase the
// geometries the stored document still implies. The walk recognises GeoJSON by
// shape, not by field name (implementation-plan.md §Publish — How It Works).
func ExtractGeometries(
	catalogIndex int, merged domain.Catalog, maxGeometries int,
) ([]domain.Geometry, []domain.Fault) {
	walk := &catalogWalk{maxGeometries: maxGeometries}
	root := []segment{{name: "catalogs"}, {index: catalogIndex}}

	// Three entry points rather than one, because a domain.Catalog is a struct
	// and not a JSON document. The depths are the ones a uniform walk over
	// $['catalogs'][i] would have reached, so the bound means the same thing here.
	walk.node(merged.Provider(), extend(root, segment{name: "provider"}), 1, nil)

	for j, resource := range merged.Resources {
		at := extend(extend(root, segment{name: "resources"}), segment{index: j})
		owners := []string{resource.ID}

		walk.node(resource.Descriptor(), extend(at, segment{name: "descriptor"}), 3, owners)
		walk.node(resource.ResourceAttributes(), extend(at, segment{name: "resourceAttributes"}), 3, owners)
	}

	for k, offer := range merged.Offers {
		at := extend(extend(root, segment{name: "offers"}), segment{index: k})

		// An empty ResourceIDs means CATALOG-WIDE, which is the same thing an
		// empty Owners means. Passing it straight through is the whole mapping.
		walk.node(offer.Document, at, 2, offer.ResourceIDs)
	}

	return walk.found, walk.faults
}

// catalogWalk accumulates as it descends. Faults travel beside the finds rather
// than short-circuiting: one bad geometry costs one geometry.
type catalogWalk struct {
	found  []domain.Geometry
	faults []domain.Fault

	// maxGeometries is config.Geo.MaxGeometriesPerCatalog, fixed for the walk.
	maxGeometries int
}

// node visits one JSON value. Ownership is decided by the caller and carried
// down unchanged — a shape's owner is where it SITS, so no key name below here
// can reassign it.
func (w *catalogWalk) node(raw json.RawMessage, at []segment, depth int, owners []string) {
	if depth > MaxCatalogWalkDepth || len(raw) == 0 {
		return
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		if kind, ok := geometryType(object); ok {
			w.collect(raw, kind, at, owners)
			// Do NOT descend. A GeometryCollection's members are PART of this
			// geometry, not separate finds.
			return
		}
		for _, key := range sortedRawKeys(object) {
			w.node(object[key], extend(at, segment{name: key}), depth+1, owners)
		}
		return
	}

	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil {
		return
	}
	for i, child := range array {
		w.node(child, extend(at, segment{index: i}), depth+1, owners)
	}
}

// collect turns a recognised geometry into a find, a malformed-geometry fault,
// or a budget fault — in that order. Parsing before the budget check is
// deliberate: a malformed shape is the publisher's to fix and must be named as
// such even when the catalog is also over its ceiling.
func (w *catalogWalk) collect(raw json.RawMessage, kind string, at []segment, owners []string) {
	if err := geo.Validate(raw); err != nil {
		w.faults = append(w.faults, domain.Fault{
			Path:    renderPath(at, nil),
			Code:    string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf("malformed %s geometry: %v", kind, err),
		})
		return
	}

	if len(w.found) >= w.maxGeometries {
		w.faults = append(w.faults, domain.Fault{
			Path: renderPath(at, nil),
			Code: string(beckn.CodePolicyGenericError),
			Message: fmt.Sprintf(
				"catalog carries more than %d geometries; this one was not indexed",
				w.maxGeometries),
		})
		return
	}

	w.found = append(w.found, domain.Geometry{
		TargetPath: jsonpath.Canonicalise(renderPath(at, everyIndex)),
		SourcePath: jsonpath.Canonicalise(renderPath(at, catalogIndexOnly)),
		Owners:     owners,
		Type:       kind,
		GeoJSON:    raw,
	})
}

// geometryType reports the RFC 7946 type of an object, and whether it is one at
// all.
//
// BOTH conditions, and the second is what keeps a general walk safe: the type
// name alone would make any object carrying "type": "Point" a geometry, and its
// missing `coordinates` would then turn an unrelated document node into a
// publish partial. A shape that IS a geometry and is broken still faults.
func geometryType(object map[string]json.RawMessage) (string, bool) {
	var kind string
	if err := json.Unmarshal(object["type"], &kind); err != nil {
		return "", false
	}
	if !geometryTypes[kind] {
		return "", false
	}

	member := "coordinates"
	if kind == beckn.GeometryGeometryCollection {
		member = "geometries"
	}
	if !isArray(object[member]) {
		return "", false
	}
	return kind, true
}

// isArray reports whether raw is a JSON array. An explicit null is not one,
// which is what stops "coordinates": null being read as a present member.
func isArray(raw json.RawMessage) bool {
	var array []json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &array) == nil && array != nil
}

// segment is one step of a path: a member name, or an array index when name is
// empty.
//
// Segments rather than a formatted string, so wildcarding an index cannot be
// confused by a member name containing brackets — a rewrite over text would turn
// ['a[0]b'] into ['a[*]b'] and store a path no caller can name.
type segment struct {
	name  string
	index int
}

// extend returns a new path one segment longer. It copies rather than appending
// in place: two siblings sharing a backing array would overwrite each other's
// last segment, storing a geometry under its sibling's path.
func extend(at []segment, next segment) []segment {
	out := make([]segment, len(at)+1)
	copy(out, at)
	out[len(at)] = next
	return out
}

// everyIndex wildcards all of them: the target form, which is what a caller
// writes in a spatial constraint's `targets`.
func everyIndex(int) bool { return true }

// catalogIndexOnly wildcards the first index and no other: the source form. The
// catalog's index belongs to the request, so a republish sending the same catalog
// in a different slot must not rewrite the stored path.
func catalogIndexOnly(ordinal int) bool { return ordinal == 0 }

// renderPath writes a path in canonical bracket form. A nil wildcard keeps every
// index concrete, which is the form a fault carries: it points at exactly the
// value the publisher sent.
func renderPath(at []segment, wildcard func(ordinal int) bool) string {
	var out strings.Builder
	out.WriteByte('$')

	ordinal := 0
	for _, s := range at {
		if s.name != "" {
			out.WriteString("['" + s.name + "']")
			continue
		}
		if wildcard != nil && wildcard(ordinal) {
			out.WriteString("[*]")
		} else {
			out.WriteString("[" + strconv.Itoa(s.index) + "]")
		}
		ordinal++
	}
	return out.String()
}

// sortedRawKeys orders an object's members so the walk is deterministic. Map
// iteration order would reorder the finds and the faults between two runs over
// identical input, which makes a failure unreproducible.
func sortedRawKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
