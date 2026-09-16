package discover

import (
	"testing"

	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// typed's default arm is what TestEveryCodeTheMapperMintsIsTyped's AST walk
// guarantees is unreachable from a real mapper fault: every code the mapper
// mints is named in the switch, so the only way here is a code this file does
// not know about — a fault in THIS file rather than in the request, and a 500
// rather than a guessed family (typed's own comment explains why).
//
// package discover, not discover_test: refusal is unexported, and this is the
// one branch nothing exported can reach.
func TestAFaultCodeTheSwitchDoesNotKnowIsInternal(t *testing.T) {
	err := refusal([]domain.Fault{{Path: "$", Code: "XYZ_NOT_A_REAL_CODE", Message: "boom"}})

	fault := apperrors.FromError(err)
	if fault == nil {
		t.Fatalf("refusal(%q) = %v, want an AppError", "XYZ_NOT_A_REAL_CODE", err)
	}
	if fault.Code != beckn.CodeNetworkInternalError {
		t.Errorf("code = %q, want %q", fault.Code, beckn.CodeNetworkInternalError)
	}
}
