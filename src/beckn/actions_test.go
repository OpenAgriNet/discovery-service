package beckn_test

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
)

// TestNormalizeActionFoldsOnlyTheTwoSpellingsOfPublish.
//
// The folding half is what beckn.action being Bounded over ["discover","publish"]
// rests on. The leaving-alone half is the part worth a test of its own: a
// normaliser that quietly rewrote `on_discover` too would make a callback
// indistinguishable from the request that caused it, on every signal at once,
// and nothing downstream would report an error — the value would simply be
// wrong.
func TestNormalizeActionFoldsOnlyTheTwoSpellingsOfPublish(t *testing.T) {
	for _, action := range []struct{ given, want string }{
		{beckn.ActionCatalogPublish, beckn.ActionPublish},
		{beckn.ActionPublish, beckn.ActionPublish},
		{beckn.ActionDiscover, beckn.ActionDiscover},
		{beckn.ActionOnDiscover, beckn.ActionOnDiscover},
		{beckn.ActionCatalogOnPublish, beckn.ActionCatalogOnPublish},

		// An action this service does not answer to is reported as it arrived.
		// Mapping it to a known one would put traffic nobody serves into the
		// bucket of traffic somebody does.
		{"search", "search"},

		// Never invented. An envelope with no action reaches the record as the
		// empty string, and the projection omits it rather than writing a guess.
		{"", ""},
	} {
		if got := beckn.NormalizeAction(action.given); got != action.want {
			t.Errorf("NormalizeAction(%q) = %q, want %q", action.given, got, action.want)
		}
	}
}
