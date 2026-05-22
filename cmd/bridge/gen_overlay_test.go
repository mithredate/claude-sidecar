package main

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Local helpers — keep cmd/bridge tests independent of the overlay package's
// test-only types.

type composeDoc struct {
	Services map[string]composeService `yaml:"services"`
}
type composeService struct {
	Image   string `yaml:"image"`
	Volumes []any  `yaml:"volumes"`
}

func parseOverlay(t *testing.T, b []byte) composeDoc {
	t.Helper()
	var doc composeDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("emitted YAML failed to parse: %v\noutput:\n%s", err, b)
	}
	return doc
}

func volumeContains(vols []any, needle string) bool {
	for _, v := range vols {
		if s, ok := v.(string); ok && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func TestRunGenOverlay_ReadsSpecFromStdinAndWritesComposeToStdout(t *testing.T) {
	input := []byte(`
image: "test-image"
project:
  host_path: /host/foo
  name: foo
  shadow_paths:
    - .env
`)
	var stdout, stderr bytes.Buffer
	code := runGenOverlay(bytes.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runGenOverlay returned %d, stderr: %s", code, stderr.String())
	}
	doc := parseOverlay(t, stdout.Bytes())
	svc, ok := doc.Services["claude-sidecar"]
	if !ok {
		t.Fatalf("expected claude-sidecar service in output, got: %v", doc.Services)
	}
	if svc.Image != "test-image" {
		t.Errorf("image: got %q want %q", svc.Image, "test-image")
	}
	if !volumeContains(svc.Volumes, "/dev/null:/workspaces/foo/.env") {
		t.Errorf("expected shadow mount in output, got: %v", svc.Volumes)
	}
}

func TestRunGenOverlay_MalformedSpecReturnsNonZeroAndExplains(t *testing.T) {
	input := []byte("this: is: not: valid: yaml: [[")
	var stdout, stderr bytes.Buffer
	code := runGenOverlay(bytes.NewReader(input), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit on malformed input, got 0; stdout: %s", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Errorf("expected stderr message explaining the parse failure")
	}
}
