package beckn

import (
	"encoding/json"
	"strings"
	"testing"
)

// Catalog, Resource and Offer share one shape: UnmarshalJSON decodes and
// keeps the bytes it decoded (Raw), MarshalJSON prefers Raw when it has one
// and falls back to marshalling its own fields when it does not (a value
// built in Go rather than decoded — tests and the conformance suite are the
// only callers of that path, per each type's own doc comment). Neither half
// is exercised end to end anywhere else in this repository: publish and
// discover always decode from the wire, so Raw is always present there.

// Syntactically invalid top-level JSON ("not json") never reaches
// UnmarshalJSON at all — encoding/json's own validity scan rejects it before
// dispatching to any custom Unmarshaler — so the case worth pinning is
// syntactically valid JSON that fails to decode into wire's own fields, which
// DOES reach the `if err != nil { return err }` line inside UnmarshalJSON.
func TestCatalogUnmarshalJSONOfATypeMismatchedFieldErrors(t *testing.T) {
	var catalog Catalog
	if err := json.Unmarshal([]byte(`{"id": 123}`), &catalog); err == nil {
		t.Error("UnmarshalJSON of an id that is a number, not a string, returned nil, want an error")
	}
}

// A Catalog built in Go, with no Raw, marshals from its fields — the
// WithoutChildren fallback (Raw absent reads the struct's own members via a
// fresh marshal-then-decode into a map) — and splices Resources/Offers back
// in exactly the way the Raw-present path does.
func TestCatalogMarshalJSONOfAGoBuiltValueMarshalsItsFieldsAndChildren(t *testing.T) {
	active := true
	catalog := Catalog{
		ID: "c1", BppID: "b1", IsActive: &active,
		Resources: []Resource{{ID: "r1"}},
		Offers:    []Offer{{ID: "o1"}},
	}

	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("re-decoding the marshalled catalog: %v", err)
	}
	if decoded["id"] != "c1" || decoded["bppId"] != "b1" || decoded["isActive"] != true {
		t.Errorf("catalog = %s, want id/bppId/isActive from the struct's own fields", encoded)
	}
	if _, present := decoded["resources"]; !present {
		t.Errorf("catalog = %s, want resources spliced in", encoded)
	}
	if _, present := decoded["offers"]; !present {
		t.Errorf("catalog = %s, want offers spliced in", encoded)
	}
}

// A Go-built Catalog with no Resources or Offers omits both keys rather than
// emitting empty arrays — an empty array would claim the publisher sent an
// (empty) resources list, which A17's own reasoning says is not this
// service's claim to make.
func TestCatalogMarshalJSONOfAGoBuiltValueWithNoChildrenOmitsBoth(t *testing.T) {
	encoded, err := json.Marshal(Catalog{ID: "c1"})
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(encoded), "resources") || strings.Contains(string(encoded), "offers") {
		t.Errorf("catalog = %s, want neither key present", encoded)
	}
}

func TestResourceUnmarshalJSONOfATypeMismatchedFieldErrors(t *testing.T) {
	var resource Resource
	if err := json.Unmarshal([]byte(`{"id": 123}`), &resource); err == nil {
		t.Error("UnmarshalJSON of an id that is a number, not a string, returned nil, want an error")
	}
}

// A Resource built in Go, with no Raw, marshals from its three named fields
// alone.
func TestResourceMarshalJSONOfAGoBuiltValueMarshalsItsFields(t *testing.T) {
	resource := Resource{ID: "r1", Descriptor: json.RawMessage(`{"name":"Wheat"}`)}

	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":"r1"`) || !strings.Contains(string(encoded), "Wheat") {
		t.Errorf("resource = %s, want id and descriptor from the struct's own fields", encoded)
	}
}

func TestOfferUnmarshalJSONOfATypeMismatchedFieldErrors(t *testing.T) {
	var offer Offer
	if err := json.Unmarshal([]byte(`{"id": 123}`), &offer); err == nil {
		t.Error("UnmarshalJSON of an id that is a number, not a string, returned nil, want an error")
	}
}

// An Offer built in Go, with no Raw, marshals from its own fields — the other
// half of the 50% MarshalJSON left uncovered, since every other test in this
// package hands Offer a decoded (Raw-carrying) value.
func TestOfferMarshalJSONOfAGoBuiltValueMarshalsItsFields(t *testing.T) {
	offer := Offer{ID: "o1", ResourceIDs: []string{"r1", "r2"}}

	encoded, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("re-decoding the marshalled offer: %v", err)
	}
	if decoded["id"] != "o1" {
		t.Errorf("offer = %s, want id from the struct's own field", encoded)
	}
	ids, ok := decoded["resourceIds"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("offer = %s, want resourceIds [r1 r2]", encoded)
	}
}
