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

func TestGenerateOverlay_EmitsSocketProxyAndSharedHomeVolume(t *testing.T) {
	spec := OverlaySpec{
		Image:   "img",
		Project: ProjectMount{HostPath: "/p", Name: "p"},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("GenerateOverlay returned error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())

	if _, ok := doc.Services["claude-sidecar-proxy"]; !ok {
		t.Errorf("expected service 'claude-sidecar-proxy', got: %v", keysOf(doc.Services))
	}

	claudeHome, ok := doc.Volumes["claude-home"]
	if !ok {
		t.Fatalf("expected volume 'claude-home' declared at top-level, got: %v", keysOf(doc.Volumes))
	}
	if claudeHome.Name != "claude-sidecar-home" || !claudeHome.External {
		t.Errorf("claude-home: got name=%q external=%v want name=%q external=true",
			claudeHome.Name, claudeHome.External, "claude-sidecar-home")
	}

	svc := doc.Services["claude-sidecar"]
	if !volumeContains(svc.Volumes, "claude-home:/home/claude") {
		t.Errorf("expected claude-sidecar to mount claude-home, got volumes: %v", svc.Volumes)
	}
}

func TestGenerateOverlay_EmitsCredentialsSeedBindMount(t *testing.T) {
	spec := OverlaySpec{
		Image:   "img",
		Project: ProjectMount{HostPath: "/host/foo", Name: "foo"},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())

	svc := doc.Services["claude-sidecar"]
	wantMount := "/host/foo/.credentials.json:/run/seed/.credentials.json:ro"
	if !volumeContains(svc.Volumes, wantMount) {
		t.Errorf("expected credentials seed bind-mount %q in volumes, got: %v", wantMount, svc.Volumes)
	}
}

func TestGenerateOverlay_ShadowsCurrentProjectFiles(t *testing.T) {
	spec := OverlaySpec{
		Image: "img",
		Project: ProjectMount{
			HostPath:    "/host/foo",
			Name:        "foo",
			ShadowPaths: []string{".env", ".credentials.json"},
		},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())

	svc := doc.Services["claude-sidecar"]
	wantShadows := []string{
		"/dev/null:/workspaces/foo/.env",
		"/dev/null:/workspaces/foo/.credentials.json",
	}
	for _, want := range wantShadows {
		if !volumeContains(svc.Volumes, want) {
			t.Errorf("expected shadow mount %q in volumes, got: %v", want, svc.Volumes)
		}
	}
}

// keysOf returns the keys of a map for nicer test failure output. Generics
// require Go 1.18+; the module uses 1.24, so this is safe.
func keysOf[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
