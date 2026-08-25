package logger

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// observed returns a logger writing into a recorder, so a test can assert on
// the fields an entry carried rather than on formatted output.
func observed(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)

	return zap.New(core), logs
}

// The point of carrying the logger in the context: a function three calls below
// the middleware that set the fields logs them without being handed them.
func TestFieldsSurviveContextPropagation(t *testing.T) {
	base, logs := observed(t)

	ctx := NewContext(context.Background(), base)
	ctx = With(ctx, RequestID("req-1"), TransactionID("txn-1"))

	// A second call one layer down, the way Envelope adds to what RequestID
	// already put there. It must add rather than replace.
	deeper := func(ctx context.Context) {
		ctx = With(ctx, MessageID("msg-1"), Action("search"))
		FromContext(ctx).Info("handled")
	}
	deeper(ctx)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0].ContextMap()
	want := map[string]any{
		"request_id":     "req-1",
		"transaction_id": "txn-1",
		"message_id":     "msg-1",
		"action":         "search",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("field %q = %v, want %v", key, got[key], value)
		}
	}

	// The parent context must be unchanged: With derives, it does not mutate,
	// so one request's fields cannot appear on a sibling's line.
	FromContext(ctx).Info("parent")
	if parent := logs.All()[1].ContextMap(); parent["message_id"] != nil {
		t.Errorf("parent context gained message_id = %v", parent["message_id"])
	}
}

// A no-op logger, never nil: a call site that logs must not have to ask whether
// a middleware ran, and a nil check that is right everywhere but one place is a
// panic in that one place.
func TestFromContextOnABareContextIsANoOpLogger(t *testing.T) {
	log := FromContext(context.Background())
	if log == nil {
		t.Fatal("FromContext returned nil")
	}

	// Must be silent rather than merely non-nil, and must survive the whole
	// API a handler would use on it.
	log.With(RequestID("req-1")).Error("this must go nowhere")

	if enabled := log.Core().Enabled(zapcore.ErrorLevel); enabled {
		t.Error("the no-op logger reports Error as enabled")
	}
}

// With on a context nobody installed a logger into is silence, not a panic:
// the fields have nowhere to go, which is the same silence as logging without
// a logger at all.
func TestWithOnABareContextIsSilentNotAPanic(t *testing.T) {
	ctx := With(context.Background(), RequestID("req-1"))

	if log := FromContext(ctx); log == nil {
		t.Fatal("FromContext returned nil after With on a bare context")
	}
}

func TestNewIsProductionJSONAtTheConfiguredLevel(t *testing.T) {
	built, err := zapConfig(config.Log{Level: "warn"})
	if err != nil {
		t.Fatalf("zapConfig: %v", err)
	}

	if built.Encoding != "json" {
		t.Errorf("encoding = %q, want json", built.Encoding)
	}
	if built.Level.Level() != zapcore.WarnLevel {
		t.Errorf("level = %v, want warn", built.Level.Level())
	}

	// Production sampling drops all but the first few entries sharing a level
	// and message within a second. RequestLogger writes one line per request
	// under one message, so sampling would discard most of the request log at
	// exactly the load worth reading it at.
	if built.Sampling != nil {
		t.Error("sampling is enabled: the request log would be dropped under load")
	}

	log, err := New(config.Log{Level: "warn"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if log.Core().Enabled(zapcore.InfoLevel) {
		t.Error("info is enabled at level warn")
	}
	if !log.Core().Enabled(zapcore.WarnLevel) {
		t.Error("warn is not enabled at level warn")
	}
}

// A level nobody can spell must fail the boot, not silently pick one: the
// alternative is a deployment that believes it set debug and is running at
// info.
func TestNewRejectsALevelItCannotParse(t *testing.T) {
	_, err := New(config.Log{Level: "loud"})
	if err == nil {
		t.Fatal("New accepted level \"loud\"")
	}
	if !strings.Contains(err.Error(), "loud") {
		t.Errorf("error %v does not name the level", err)
	}
}

// The four field names are spelled in one place. Two spellings of one key are
// two fields to the log pipeline, and the mistake is invisible until someone
// queries for the one that is missing.
func TestTheRequestFieldsAreNamedInSnakeCase(t *testing.T) {
	cases := map[string]zap.Field{
		"request_id":     RequestID("a"),
		"transaction_id": TransactionID("a"),
		"message_id":     MessageID("a"),
		"action":         Action("a"),
	}

	for want, field := range cases {
		if field.Key != want {
			t.Errorf("field key = %q, want %q", field.Key, want)
		}
		if field.Type != zapcore.StringType {
			t.Errorf("field %q is type %v, want a string field", want, field.Type)
		}
	}
}
