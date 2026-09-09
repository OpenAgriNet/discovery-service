package httpx

import (
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// 23d's error event, which is the one of the four that is not a controller's.
// Every refusal on every path passes through WriteNack, so this is the single
// place a rejection becomes observable — including the ones the controllers
// never see, like a body over the size ceiling or a rate-limit refusal.

// nacked serves one rejection and gives back the facts it produced, plus the
// response so the header and the attribute can be compared.
func nacked(t *testing.T, err error) (*fact.Record, *httptest.ResponseRecorder) {
	t.Helper()

	ctx, record := fact.New(loggerContext(zap.NewNop()))
	recorder := httptest.NewRecorder()
	WriteNack(ctx, recorder, config.Errors{}, messageID, err)

	return record, recorder
}

func observed(t *testing.T, record *fact.Record, key fact.Key) string {
	t.Helper()

	observation, found := record.Lookup(key)
	if !found {
		t.Fatalf("%s was never observed", fact.Of(key).Name)
	}
	return observation.Text
}

// TestTheErrorEventTypeIsTheHeaderByteForByte — the acceptance criterion at
// opentelemetry.md:1140.
//
// An equality against the header rather than against a literal, deliberately.
// A literal would pass while both moved together, and the whole value of this
// attribute is that a facilitator filtering a span set gets the same partition
// a caller branching on X-Beckn-Error-Type gets. The two drifting apart is
// invisible from either side alone.
func TestTheErrorEventTypeIsTheHeaderByteForByte(t *testing.T) {
	for _, err := range []error{
		apperrors.Schema(beckn.CodeSchemaValidationFailed, "not a discover action").At("$.message"),
		apperrors.Network(beckn.CodeNetworkCatalogSourceUnavailable, "no store"),
		apperrors.Internal(),
	} {
		record, recorder := nacked(t, err)

		header := recorder.Header().Get(HeaderErrorType)
		if header == "" {
			t.Fatalf("%v set no %s, so this test would pass vacuously", err, HeaderErrorType)
		}
		if got := observed(t, record, fact.ErrorEventType); got != header {
			t.Errorf("the error event's type is %q and %s is %q; they are one category",
				got, HeaderErrorType, header)
		}
	}
}

// TestTheErrorEventCarriesTheCodeMessageAndPathTheCallerWasGiven.
//
// The COERCED fault's fields, which are the ones that go into the body. The
// original error is deliberately not among them: a driver's text names a host,
// a port or a query, the body carries the coerced message for exactly that
// reason, and a span leaves this machine — so the reason applies twice over.
func TestTheErrorEventCarriesTheCodeMessageAndPathTheCallerWasGiven(t *testing.T) {
	record, _ := nacked(t, apperrors.
		Schema(beckn.CodeSchemaInvalidFormat, "limit is not a whole number").
		At("$.limit"))

	for _, want := range []struct {
		key   fact.Key
		value string
	}{
		{fact.ErrorCode, string(beckn.CodeSchemaInvalidFormat)},
		{fact.ErrorMessage, "limit is not a whole number"},
		{fact.ErrorPath, "$.limit"},
	} {
		if got := observed(t, record, want.key); got != want.value {
			t.Errorf("%s = %q, want %q", fact.Of(want.key).SpanKey, got, want.value)
		}
	}
}

// TestAFaultThatNamesNoFieldCarriesNoPath.
//
// Absent, not empty. "$.message" is what a fault about the action as a whole
// points at, so the empty string is not the root of anything — writing it would
// put every fault that names no field at a location that reads like one.
func TestAFaultThatNamesNoFieldCarriesNoPath(t *testing.T) {
	record, _ := nacked(t, apperrors.Internal())

	if observation, found := record.Lookup(fact.ErrorPath); found {
		t.Errorf("the error event carries path=%q for a fault that names no field",
			observation.Text)
	}
}

// TestARejectionOnAChainWithNoRecordStillAnswers.
//
// The probes chain mounts RequestID and Recover only and allocates no record
// (router.go:158-163), so a panic in /healthz reaches WriteNack with nothing in
// context. It must answer 500 rather than panic a second time inside the
// recovery that was answering the first. The acceptance and dbtest suites call
// controllers with no middleware at all and depend on the same property.
func TestARejectionOnAChainWithNoRecordStillAnswers(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteNack(loggerContext(zap.NewNop()), recorder, config.Errors{}, messageID, apperrors.Internal())

	if recorder.Code != 500 {
		t.Errorf("status = %d with no record in context, want 500", recorder.Code)
	}
}
