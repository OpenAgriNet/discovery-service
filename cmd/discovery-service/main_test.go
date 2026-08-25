package main

import (
	"bytes"
	"strings"
	"testing"
)

const modulePath = "github.com/OpenAgriNet/discovery-service"

func TestWriteBuildInfoNamesTheModule(t *testing.T) {
	var buf bytes.Buffer

	if err := writeBuildInfo(&buf); err != nil {
		t.Fatalf("writeBuildInfo: %v", err)
	}

	got := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(got, modulePath+" ") {
		t.Errorf("build line %q does not start with the module path %q", got, modulePath)
	}
}
