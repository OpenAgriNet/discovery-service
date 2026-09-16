package httpx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

const publishBody = `{
  "context": {
    "action": "publish", "version": "2.0.0",
    "messageId": "2f6b3f7e-4c1a-4a5e-9d3f-6b1f0d2a7c11",
    "transactionId": "8c1b2a4d-3e5f-4a6b-8c9d-0e1f2a3b4c5d",
    "timestamp": "2026-01-01T00:00:00Z",
    "networkId": "mahavistar",
    "schemaContext": ["https://beckn.org/Agri#SeedLot"]
  },
  "message": {
    "catalogs": [{"id": "cat-1", "resources": [{"id": "r-1"}]}]
  }
}`

func TestParseEnvelopeReadsContextAndMessage(t *testing.T) {
	env, err := ParseEnvelope[beckn.CatalogPublishAction]([]byte(publishBody))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}

	if env.Context.Action != beckn.ActionPublish {
		t.Errorf("Context.Action = %q, want %q", env.Context.Action, beckn.ActionPublish)
	}
	if env.Context.NetworkID != "mahavistar" {
		t.Errorf("Context.NetworkID = %q, want mahavistar", env.Context.NetworkID)
	}
	// schemaContext is a Context field, not an Intent one — Intent is
	// additionalProperties:false, so the reference implementation's placement
	// inside message.intent is not a shape this service can accept.
	if got := env.Context.SchemaContext; len(got) != 1 || got[0] != "https://beckn.org/Agri#SeedLot" {
		t.Errorf("Context.SchemaContext = %v, want one Agri#SeedLot entry", got)
	}
	if len(env.Message.Catalogs) != 1 || env.Message.Catalogs[0].ID != "cat-1" {
		t.Errorf("Message.Catalogs = %#v, want one catalog cat-1", env.Message.Catalogs)
	}
}

// The spec is a moving target and this service is one hop in a chain it does
// not own. An unknown key is what a v2.0.x sender looks like from here, and
// DisallowUnknownFields would reject it in the decoder — before the L1
// validator, which is the component that actually knows which schemas close
// additionalProperties and which do not, ever sees the body.
func TestParseEnvelopeKeepsAnUnknownFieldFromFailingTheParse(t *testing.T) {
	body := `{
	  "context": {"action": "discover", "version": "2.0.0", "futureKey": "ignored"},
	  "message": {"intent": {"textSearch": "wheat seeds"}, "futureBlock": {"a": 1}},
	  "topLevelFuture": true
	}`

	env, err := ParseEnvelope[beckn.DiscoverAction]([]byte(body))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Context.Action != beckn.ActionDiscover {
		t.Errorf("Context.Action = %q, want %q", env.Context.Action, beckn.ActionDiscover)
	}
	if env.Message.Intent.TextSearch != "wheat seeds" {
		t.Errorf("Intent.TextSearch = %q, want wheat seeds", env.Message.Intent.TextSearch)
	}
}

// The two shapes of unreadable. Both are transport-level failures — the body
// could not be read at all — which is the one case the plan reserves a NACK
// for, so the parser must name them rather than hand back a zero envelope.
func TestParseEnvelopeRefusesABodyItCannotRead(t *testing.T) {
	cases := map[string]string{
		"an empty body":     ``,
		"malformed JSON":    `{"context":`,
		"a JSON array":      `[]`,
		"a context scalar":  `{"context": "publish"}`,
		"a message scalar":  `{"context": {}, "message": 7}`,
		"trailing garbage":  `{"context": {}, "message": {}} {}`,
		"a bare JSON value": `"publish"`,
		// json.Decode treats null as a no-op against a struct, so this is the
		// one non-object that returns a zero envelope and no error at all.
		"a JSON null":        `null`,
		"a padded JSON null": "  null\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEnvelope[beckn.DiscoverAction]([]byte(body)); err == nil {
				t.Fatalf("ParseEnvelope(%q): accepted, want an error", body)
			} else if !strings.Contains(err.Error(), "envelope") {
				t.Errorf("error %v does not name the envelope", err)
			}
		})
	}
}

// A scalar `targets` reaches the mapper as a one-element slice even when it
// arrives nested three levels down inside a real request body. The oneOf is
// handled by the type, so nothing on the discover path has to branch on it.
func TestParseEnvelopeCarriesTheScalarTargetsFormThroughTheIntent(t *testing.T) {
	body := `{
	  "context": {"action": "discover", "version": "2.0.0"},
	  "message": {"intent": {"spatial": [{
	      "op": "S_DWITHIN",
	      "targets": "$['catalogs'][*]['provider']['availableAt'][*]['geo']",
	      "geometry": {"type": "Point", "coordinates": [77.5946, 12.9716]},
	      "distanceMeters": 10000
	  }]}}
	}`

	env, err := ParseEnvelope[beckn.DiscoverAction]([]byte(body))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}

	spatial := env.Message.Intent.Spatial
	if len(spatial) != 1 {
		t.Fatalf("Intent.Spatial = %#v, want one constraint", spatial)
	}
	if got := spatial[0].Targets; len(got) != 1 ||
		got[0] != "$['catalogs'][*]['provider']['availableAt'][*]['geo']" {
		t.Errorf("Targets = %v, want the one scalar pointer as a slice", got)
	}
	if spatial[0].Geometry == nil || spatial[0].Geometry.Type != "Point" {
		t.Errorf("Geometry = %#v, want a Point", spatial[0].Geometry)
	}
	// A pointer, so mapIntent can tell "sent 10000" from "not sent" and raise
	// the partial fault the plan requires when distanceMeters rides along with
	// an operator that ignores it.
	if spatial[0].DistanceMeters == nil || *spatial[0].DistanceMeters != 10000 {
		t.Errorf("DistanceMeters = %v, want 10000", spatial[0].DistanceMeters)
	}
}

// Absence has to survive the parse to reach the merge (A8). encoding/json gives
// nil for a key that was not sent and a non-nil `null` for one that was, and
// under MERGE that is the difference between keeping a publisher's provider
// document and deleting it.
func TestParseEnvelopeKeepsAbsenceDistinctFromAnExplicitNull(t *testing.T) {
	body := `{
	  "context": {"action": "publish", "version": "2.0.0"},
	  "message": {"catalogs": [
	    {"id": "absent"},
	    {"id": "explicit-null", "provider": null, "validity": null, "isActive": false}
	  ]}
	}`

	env, err := ParseEnvelope[beckn.CatalogPublishAction]([]byte(body))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}

	absent, explicit := env.Message.Catalogs[0], env.Message.Catalogs[1]
	if absent.Provider != nil {
		t.Errorf("absent Provider = %s, want nil", absent.Provider)
	}
	if string(explicit.Provider) != "null" {
		t.Errorf("explicit Provider = %s, want null", explicit.Provider)
	}
	if absent.Validity != nil {
		t.Errorf("absent Validity = %#v, want nil", absent.Validity)
	}
	// isActive has a declared default of true (A9), so the mapper resolves it —
	// but only if the wire type can say the publisher sent false rather than
	// nothing at all.
	if absent.IsActive != nil {
		t.Errorf("absent IsActive = %v, want nil", *absent.IsActive)
	}
	if explicit.IsActive == nil || *explicit.IsActive {
		t.Errorf("explicit IsActive = %v, want false", explicit.IsActive)
	}
}

// C11, from the other end: OnDiscoverAction is additionalProperties:false with
// `catalogs` as its only property, so the serialised response has exactly one
// key. The degraded list travels as the X-Beckn-Degraded header. Asserted on
// the bytes rather than on the struct, because it is the bytes a strict
// consumer validates.
func TestOnDiscoverActionSerialisesExactlyOneKey(t *testing.T) {
	out, err := json.Marshal(beckn.OnDiscoverAction{Catalogs: []beckn.Catalog{{ID: "cat-1"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(out, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("OnDiscoverAction serialised %d keys (%s), want exactly catalogs", len(keys), out)
	}
	if _, ok := keys["catalogs"]; !ok {
		t.Errorf("OnDiscoverAction serialised %s, want the catalogs key", out)
	}
}
