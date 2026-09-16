package acceptance

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The catalog these scenarios publish, built once so that the round-trip
// assertion and the merge assertion are measured against the same document.
//
// It is deliberately maximal rather than minimal: every member the schema
// allows on a Catalog, a Provider, a Resource and an Offer, because the defect
// this file exists for is a member that is accepted and then silently not
// stored, and a fixture that sends only the members the storage layer happens
// to keep would pass against the very schema it is meant to reject.
func aWholeCatalog() map[string]any {
	return map[string]any{
		"id":     "c-whole",
		"bppId":  "hul.example.com",
		"bppUri": "https://hul.example.com/beckn",
		"descriptor": map[string]any{
			"name":      "HUL Bengaluru Depot",
			"code":      "HUL-BLR",
			"shortDesc": "Wheat and staples, Bengaluru south",
		},
		// Explicit rather than omitted. A9 resets an omitted isActive to its
		// default, so omitting it here would make the round-trip assertion
		// depend on that rule instead of on what was stored.
		"isActive": true,
		"validity": map[string]any{
			"startDate": "2026-01-01T00:00:00Z",
			"endDate":   "2026-12-31T23:59:59Z",
		},
		"provider": map[string]any{
			"id": "p-hul",
			"descriptor": map[string]any{
				"name":      "Hindustan Unilever Limited",
				"shortDesc": "FMCG",
			},
			"availableAt": []any{
				map[string]any{"geo": geoPoint(majestic)},
			},
			"providerAttributes": jsonLD(map[string]any{
				"gstin": "29AAACH1234K1Z5",
			}),
		},
		"resources": []any{
			map[string]any{
				"id": "r-atta",
				"descriptor": map[string]any{
					"name":      "wheat atta 5kg",
					"code":      "SKU-ATTA-5",
					"shortDesc": "Stone ground whole wheat",
				},
				"resourceAttributes": jsonLD(map[string]any{
					"packagedGoodsDeclaration": map[string]any{
						"manufacturerOrPacker": map[string]any{
							"name":    "Hindustan Unilever Limited",
							"address": map[string]any{"city": "Bengaluru", "pincode": "560001"},
						},
						"netQuantity": "5 kg",
						"mrp":         map[string]any{"currency": "INR", "value": 250},
					},
					"certifications": []any{
						map[string]any{"id": "FSSAI-10012", "issuer": "FSSAI"},
						map[string]any{"id": "AGMARK-77", "issuer": "AGMARK"},
					},
					"grade": "A",
				}),
			},
		},
		"offers": []any{
			map[string]any{
				"id":          "o-diwali",
				"descriptor":  map[string]any{"name": "Diwali 10% off", "shortDesc": "Festive"},
				"resourceIds": []any{"r-atta"},
				// Open on both sides. A window that excluded `now` would have
				// this scenario asserting the validity gate — which scenario 21
				// already owns — and reporting its correct answer as a
				// round-trip failure.
				"validity": map[string]any{
					"startDate": "2026-01-01T00:00:00Z",
					"endDate":   "2026-12-31T23:59:59Z",
				},
				"considerations": []any{
					map[string]any{
						"id": "cons-discount",
						"considerationAttributes": jsonLD(map[string]any{
							"discountPct": 10,
						}),
					},
				},
				// No `addOns`, and not by choice. `AddOn` in beckn.yaml is a
				// bare `oneOf: [Resource, Offer]`, and NEITHER branch sets
				// `additionalProperties: false` — so any object carrying an
				// `id` satisfies both and `oneOf` fails on "matches more
				// than one schema". Measured, not assumed: adding one costs
				//
				//   400 SCH_SCHEMA_VALIDATION_FAILED at
				//   $.message.catalogs[0].offers[0].addOns[0]
				//
				// That is a defect in the protocol schema, not in this
				// service, and it makes `addOns` unpublishable by anyone.
				// Storing it verbatim is already covered by the offer
				// document assertion below; it is the L1 gate in front that
				// no fixture can get past.
				"offerAttributes": jsonLD(map[string]any{"channel": "retail"}),
			},
		},
	}
}

// What a publisher sends comes back.
//
// The narrowest statement of the requirement, and the one the shredded schema
// could not meet: `catalogs` held `provider` and six scalar columns, so a
// catalog's `descriptor`, `bppId`, `bppUri` and `validity` had nowhere to live
// and a response could not carry them. `descriptor` is in the schema's own
// `required` list, so the service was accepting a field it could never return.
//
// Compared as whole documents rather than field by field on purpose. An
// assertion that named the members it checked would keep passing as the next
// protocol revision adds ones it does not name — which is precisely how the
// four missing members went unnoticed for twenty tasks.
func TestAPublishedCatalogComesBackWhole(t *testing.T) {
	svc := newService(t)

	published := aWholeCatalog()
	svc.publishCatalogs(t, published)

	assertJSON(t, svc.discoverOneRaw(t, text("wheat")), published, "the discovered catalog")
}

// And a MERGE moves the leaf it names and nothing else.
//
// This is the assertion the shredded schema could not make at all: to show that
// an untouched member survived a republish, the member has to have been stored
// in the first place, and `bppId`, `bppUri` and the catalog descriptor were
// not. A merge test written against that schema could only ever compare the
// columns that existed, so it would have reported "nothing else moved" while
// four members were being dropped on every single publish.
func TestAMergeMovesOnlyTheLeafItNames(t *testing.T) {
	svc := newService(t)
	svc.publishCatalogs(t, aWholeCatalog())

	// One leaf, four levels down, with no directive at all — which is MERGE.
	//
	// `descriptor` and `provider` are resent unchanged, and `isActive` is
	// resent true, because neither is optional here. The schema's required list
	// is [id, descriptor, provider] and L1 runs BEFORE the merge, so a truly
	// minimal patch is a 400 rather than a merge — and A9 resets an omitted
	// isActive rather than keeping it. What this scenario is about is the
	// members it does NOT resend: bppId, bppUri, validity and the whole offer.
	whole := aWholeCatalog()
	svc.publishCatalogs(t, map[string]any{
		"id":         "c-whole",
		"descriptor": whole["descriptor"],
		"provider":   whole["provider"],
		"isActive":   true,
		"resources": []any{
			map[string]any{
				"id": "r-atta",
				"resourceAttributes": map[string]any{
					"@context": schemaContext,
					"@type":    schemaType,
					"packagedGoodsDeclaration": map[string]any{
						"netQuantity": "10 kg",
					},
				},
			},
		},
	})

	want := aWholeCatalog()
	resources, ok := want["resources"].([]any)
	if !ok || len(resources) == 0 {
		t.Fatalf("the fixture has no resources array: %T", want["resources"])
	}
	resource, ok := resources[0].(map[string]any)
	if !ok {
		t.Fatalf("the fixture's first resource is %T, want an object", resources[0])
	}
	child(t, child(t, resource, "resourceAttributes"), "packagedGoodsDeclaration")["netQuantity"] = "10 kg"

	assertJSON(t, svc.discoverOneRaw(t, text("wheat")), want, "the merged catalog")
}

// child descends one object member of a fixture, reporting through the test
// rather than panicking: a fixture that stopped having the shape a case assumes
// is a fixture problem, and a nil map dereference names the wrong line.
func child(t *testing.T, holder map[string]any, member string) map[string]any {
	t.Helper()

	nested, ok := holder[member].(map[string]any)
	if !ok {
		t.Fatalf("fixture member %q is %T, want an object", member, holder[member])
	}
	return nested
}

// discoverOneRaw is discover returning the single catalog it found as the bytes
// the caller received.
//
// Raw rather than decoded through beckn.Catalog, because that type is this
// service's own view of the protocol: a member the struct forgot would be
// dropped by the decoder and the comparison would then be against a document
// that had already lost it. The bytes are what the publisher gets back.
func (s *service) discoverOneRaw(t *testing.T, intent map[string]any) json.RawMessage {
	t.Helper()

	answer := s.discoverResponse(t, intent)
	if answer.status != http.StatusOK {
		t.Fatalf("POST /discover = %d, want 200\nbody: %s", answer.status, answer.body)
	}

	var body struct {
		Message struct {
			Catalogs []json.RawMessage `json:"catalogs"`
		} `json:"message"`
	}
	answer.decode(t, &body)

	if len(body.Message.Catalogs) != 1 {
		t.Fatalf("discover found %d catalogs, want 1\nbody: %s",
			len(body.Message.Catalogs), answer.body)
	}
	return body.Message.Catalogs[0]
}
