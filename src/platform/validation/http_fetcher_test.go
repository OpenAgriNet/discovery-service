package validation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const specBody = "openapi: 3.0.3\n"

func TestTheFetcherReturnsWhatTheRegistryServed(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(specBody)); err != nil {
			t.Errorf("write the spec: %v", err)
		}
	}))
	t.Cleanup(registry.Close)

	document, err := HTTPFetcher()(t.Context(), registry.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(document) != specBody {
		t.Errorf("document = %q, want %q", document, specBody)
	}
}

// A registry that answers 404 with an HTML error page would otherwise be
// compiled as a spec, and the boot would fail somewhere inside kin-openapi with
// a parse error rather than at the hop that actually went wrong.
func TestANonSuccessStatusIsAFetchFailureNamingTheStatus(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such spec", http.StatusNotFound)
	}))
	t.Cleanup(registry.Close)

	_, err := HTTPFetcher()(t.Context(), registry.URL)
	if err == nil {
		t.Fatal("fetch accepted a 404 body as a spec document")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %v does not name the status the registry answered with", err)
	}
}

// The one unbounded read at boot. A registry that streams forever — or a
// captive-portal redirect to something that does — must not be allowed to take
// the process's memory with it, and LoadSpecIndex's cache fallback is right
// there to be used instead.
func TestABodyPastTheCeilingIsRefusedRatherThanBuffered(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(strings.Repeat("x", 64))); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(registry.Close)

	if _, err := fetchSpec(t.Context(), registry.URL, 16); err == nil {
		t.Error("fetchSpec accepted a body past its ceiling")
	}
	if _, err := fetchSpec(t.Context(), registry.URL, 64); err != nil {
		t.Errorf("fetchSpec refused a body exactly at its ceiling: %v", err)
	}
}

// The fetch is the boot's one network call, so a cancelled context has to stop
// it — otherwise a shutdown during a slow registry waits out the whole timeout.
//
// The registry here answers, and the assertion is on context.Canceled rather
// than on any error at all: a dial to a closed port fails whether or not the
// context is honoured, so "err != nil" is a test that stays green with the ctx
// plumbing deleted.
func TestACancelledContextStopsTheFetch(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(specBody)); err != nil {
			t.Errorf("write the spec: %v", err)
		}
	}))
	t.Cleanup(registry.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := HTTPFetcher()(ctx, registry.URL)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("fetch on a cancelled context returned %v, want it to carry context.Canceled", err)
	}
}
