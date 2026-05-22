package main

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeDoc is a loose model of the Compose YAML we emit. It's deliberately
// permissive so tests parse-then-assert rather than string-match — surviving
// formatter or key-order changes.
type composeDoc struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]composeVolume  `yaml:"volumes"`
}
type composeService struct {
	Image       string            `yaml:"image"`
	Volumes     []string          `yaml:"volumes"`
	Environment map[string]string `yaml:"environment"`
	WorkingDir  string            `yaml:"working_dir"`
	NetworkMode string            `yaml:"network_mode"`
	DependsOn   []string          `yaml:"depends_on"`
}
type composeVolume struct {
	Name     string `yaml:"name"`
	External bool   `yaml:"external"`
}

func parseOverlay(t *testing.T, b []byte) composeDoc {
	t.Helper()
	var doc composeDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("emitted YAML failed to parse: %v\noutput:\n%s", err, b)
	}
	return doc
}

func volumeContains(vols []string, needle string) bool {
	for _, v := range vols {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

func TestGenerateOverlay_EmitsClaudeSidecarServiceWithProjectMount(t *testing.T) {
	spec := OverlaySpec{
		Image: "ghcr.io/mithredate/claude-sidecar:latest",
		Project: ProjectMount{
			HostPath: "/Users/me/projects/foo",
			Name:     "foo",
		},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("GenerateOverlay returned error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())

	svc, ok := doc.Services["claude-sidecar"]
	if !ok {
		t.Fatalf("expected service 'claude-sidecar', got services: %v", doc.Services)
	}
	if svc.Image != "ghcr.io/mithredate/claude-sidecar:latest" {
		t.Errorf("image: got %q want %q", svc.Image, "ghcr.io/mithredate/claude-sidecar:latest")
	}
	if !volumeContains(svc.Volumes, "/Users/me/projects/foo:/workspaces/foo") {
		t.Errorf("expected project bind-mount in volumes, got: %v", svc.Volumes)
	}
}
