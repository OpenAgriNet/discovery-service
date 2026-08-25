package beckn

import (
	"encoding/json"
	"reflect"
	"testing"
)

// `beckn.yaml` declares SpatialConstraint.targets as a oneOf over a string and
// an array of strings, and real senders use both. Whichever arrives, the
// mapper downstream sees one slice — a caller who wrote the scalar form must
// not get a different answer from one who wrapped it in brackets.
func TestTargetsParsesBothWireForms(t *testing.T) {
	cases := map[string]struct {
		body string
		want Targets
	}{
		"the scalar form": {
			`{"op":"S_DWITHIN","targets":"$['availableAt'][*]['geo']"}`,
			Targets{"$['availableAt'][*]['geo']"},
		},
		"the array form": {
			`{"op":"S_DWITHIN","targets":["$['availableAt'][*]['geo']"]}`,
			Targets{"$['availableAt'][*]['geo']"},
		},
		"the array form carrying several pointers": {
			`{"op":"S_DWITHIN","targets":["$['a']","$['b']"]}`,
			Targets{"$['a']", "$['b']"},
		},
		"an empty array": {
			`{"op":"S_DWITHIN","targets":[]}`,
			Targets{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got SpatialConstraint
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.body, err)
			}
			if !reflect.DeepEqual(got.Targets, tc.want) {
				t.Errorf("Targets = %#v, want %#v", got.Targets, tc.want)
			}
		})
	}
}

// A shape the oneOf does not admit is an error rather than an empty slice.
// Silently reading `targets: 12` as "no targets" would widen the query to the
// whole index, which is the failure mode the plan refuses everywhere on the
// spatial path: a caller who asked to be filtered must never be told 200 with
// the world in it.
func TestTargetsRefusesAShapeTheOneOfDoesNotAdmit(t *testing.T) {
	for _, body := range []string{`12`, `{"path":"$"}`, `[1,2]`, `true`} {
		var got Targets
		if err := json.Unmarshal([]byte(body), &got); err == nil {
			t.Errorf("unmarshal %s: accepted, want an error (got %#v)", body, got)
		}
	}
}

// The scalar form is what arrives; the array form is what is stored and sent.
// Marshalling back out as an array is inside the oneOf either way, so the round
// trip is lossless in meaning even though it is not byte-identical.
func TestTargetsMarshalsAsTheArrayForm(t *testing.T) {
	out, err := json.Marshal(Targets{"$['a']"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `["$['a']"]` {
		t.Errorf("marshal = %s, want [\"$['a']\"]", out)
	}
}
