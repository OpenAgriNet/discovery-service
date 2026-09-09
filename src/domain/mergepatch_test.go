package domain

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// sameJSON compares two documents by value rather than by byte, so a case is
// written in whatever key order reads best and still means what it says.
func sameJSON(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("the merge produced something that is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("the expectation is not JSON: %v (%s)", err, want)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// The exhaustive RFC 7396 table. MergePatch is a pure function on two values,
// so there is no excuse for pinning it with anything less — and every row here
// is a rule some reader will eventually find surprising enough to "fix".
func TestMergePatchFollowsRFC7396(t *testing.T) {
	cases := []struct {
		name   string
		target string
		patch  string
		want   string
	}{
		{
			name:   "a key the patch does not mention is kept",
			target: `{"a":1,"b":2}`,
			patch:  `{"a":9}`,
			want:   `{"a":9,"b":2}`,
		},
		{
			name:   "a key the patch sends is replaced",
			target: `{"a":1}`,
			patch:  `{"a":2}`,
			want:   `{"a":2}`,
		},
		{
			name:   "an explicit null deletes the key",
			target: `{"a":1,"b":2}`,
			patch:  `{"b":null}`,
			want:   `{"a":1}`,
		},
		{
			name:   "a null on a key that does not exist is a no-op, not an insert",
			target: `{"a":1}`,
			patch:  `{"missing":null}`,
			want:   `{"a":1}`,
		},
		{
			name:   "a nested object is recursed into, not replaced",
			target: `{"o":{"keep":1,"drop":2}}`,
			patch:  `{"o":{"drop":null,"add":3}}`,
			want:   `{"o":{"keep":1,"add":3}}`,
		},
		{
			// The rule that separates RFC 7396 from a deep merge, and the one
			// that matters most here: `resources` and `offers` are merged by id
			// by MergeCatalog precisely because the RFC would otherwise blow
			// the whole collection away on every patch.
			name:   "an array is replaced wholesale, never element-merged",
			target: `{"xs":[1,2,3]}`,
			patch:  `{"xs":[9]}`,
			want:   `{"xs":[9]}`,
		},
		{
			name:   "a scalar target patched with an object is replaced",
			target: `{"a":"scalar"}`,
			patch:  `{"a":{"now":"an object"}}`,
			want:   `{"a":{"now":"an object"}}`,
		},
		{
			// The mirror of the case above, and the RFC's "if Target is not an
			// Object, Target = {}" branch: a patch object lands on a scalar by
			// discarding it, not by failing.
			name:   "an object patch onto a scalar target discards the scalar",
			target: `"scalar"`,
			patch:  `{"a":1}`,
			want:   `{"a":1}`,
		},
		{
			name:   "a non-object patch replaces the target outright",
			target: `{"a":1}`,
			patch:  `[1,2]`,
			want:   `[1,2]`,
		},
		{
			name:   "a patch of null replaces the target with null",
			target: `{"a":1}`,
			patch:  `null`,
			want:   `null`,
		},
		{
			name:   "an empty patch changes nothing",
			target: `{"a":1,"b":{"c":2}}`,
			patch:  `{}`,
			want:   `{"a":1,"b":{"c":2}}`,
		},
		{
			name:   "recursion reaches a key several levels down",
			target: `{"a":{"b":{"c":1,"d":2}}}`,
			patch:  `{"a":{"b":{"c":9}}}`,
			want:   `{"a":{"b":{"c":9,"d":2}}}`,
		},
		{
			// An absent key whose patch value is an object still creates it,
			// which is how a publisher adds a field they never sent before.
			name:   "a key absent from the target is created",
			target: `{"a":1}`,
			patch:  `{"b":{"c":2}}`,
			want:   `{"a":1,"b":{"c":2}}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := MergePatch(json.RawMessage(testCase.target), json.RawMessage(testCase.patch))

			if !sameJSON(t, got, json.RawMessage(testCase.want)) {
				t.Errorf("MergePatch(%s, %s) = %s, want %s",
					testCase.target, testCase.patch, got, testCase.want)
			}
		})
	}
}

// The caller's stored document is what the audit trail holds and what a
// concurrent reader may still be looking at. A merge that wrote through into it
// would make the two disagree.
func TestMergePatchLeavesItsInputsAlone(t *testing.T) {
	target := json.RawMessage(`{"a":{"b":1}}`)
	patch := json.RawMessage(`{"a":{"b":2}}`)
	targetBefore, patchBefore := string(target), string(patch)

	MergePatch(target, patch)

	if string(target) != targetBefore {
		t.Errorf("target = %s, want it unchanged at %s", target, targetBefore)
	}
	if string(patch) != patchBefore {
		t.Errorf("patch = %s, want it unchanged at %s", patch, patchBefore)
	}
}

// The two decode-failure branches MergePatch's own doc comment names, neither
// reachable from a validated request but both stated as a deliberate contract
// rather than left to whatever json.Unmarshal happens to do.
func TestMergePatchOfUnreadableJSONChangesNothing(t *testing.T) {
	target := json.RawMessage(`{"a":1}`)

	got := MergePatch(target, json.RawMessage(`not json`))
	if !sameJSON(t, got, target) {
		t.Errorf("MergePatch = %s, want the target unchanged when the patch will not decode", got)
	}
}

// An unreadable target reads as "not an object" — the RFC's own branch for a
// scalar — so the patch replaces it wholesale rather than the merge failing.
func TestMergePatchOfAnUnreadableTargetReadsAsNotAnObject(t *testing.T) {
	patch := json.RawMessage(`{"a":1}`)

	got := MergePatch(json.RawMessage(`not json`), patch)
	if !sameJSON(t, got, patch) {
		t.Errorf("MergePatch = %s, want the patch alone — an unreadable target is read as absent", got)
	}
}

// patchedDate's three tri-state branches: unset keeps the stored bound, an
// explicit null clears it to the zero time (the same value an unset column
// reads back as), and a value replaces it.
func TestPatchedDateTriState(t *testing.T) {
	stored := timeMust(t, "2026-01-01T00:00:00Z")
	value := timeMust(t, "2026-06-01T00:00:00Z")

	if got := patchedDate(stored, Nullable[time.Time]{}); !got.Equal(stored) {
		t.Errorf("unset: patchedDate = %v, want the stored bound %v", got, stored)
	}
	if got := patchedDate(stored, Nullable[time.Time]{Set: true, Null: true}); !got.IsZero() {
		t.Errorf("explicit null: patchedDate = %v, want the zero time", got)
	}
	if got := patchedDate(stored, Nullable[time.Time]{Set: true, Value: value}); !got.Equal(value) {
		t.Errorf("value: patchedDate = %v, want %v", got, value)
	}
}

// patchedTimeOfDay's own tri-state, where cleared is nil rather than the zero
// time — 00:00:00 is a real bound and cannot double as the absence.
func TestPatchedTimeOfDayTriState(t *testing.T) {
	stored := &TimeOfDay{Hour: 9}
	value := TimeOfDay{Hour: 17}

	if got := patchedTimeOfDay(stored, Nullable[TimeOfDay]{}); got != stored {
		t.Errorf("unset: patchedTimeOfDay = %v, want the stored pointer %v", got, stored)
	}
	if got := patchedTimeOfDay(stored, Nullable[TimeOfDay]{Set: true, Null: true}); got != nil {
		t.Errorf("explicit null: patchedTimeOfDay = %v, want nil", got)
	}
	if got := patchedTimeOfDay(stored, Nullable[TimeOfDay]{Set: true, Value: value}); got == nil || *got != value {
		t.Errorf("value: patchedTimeOfDay = %v, want %v", got, value)
	}
}

func timeMust(t *testing.T, literal string) time.Time {
	t.Helper()

	instant, err := time.Parse(time.RFC3339, literal)
	if err != nil {
		t.Fatalf("parse %q: %v", literal, err)
	}
	return instant
}
