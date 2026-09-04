package jsonpath

import "testing"

// isIdentifier's empty-name branch has no reachable caller: dotMember only
// calls it after isMemberName has already rejected an empty name, so this is
// exercised directly against the pure function rather than through Dot.
func TestIsIdentifierOfAnEmptyNameIsFalse(t *testing.T) {
	if isIdentifier("") {
		t.Error("isIdentifier(\"\") = true, want false")
	}
}
