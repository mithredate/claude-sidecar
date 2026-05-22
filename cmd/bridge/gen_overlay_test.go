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
	Volumes     []any             `yaml:"volumes"` // mixed short (string) and long (map) forms
	Environment map[string]string `yaml:"environment"`
	WorkingDir  string            `yaml:"working_dir"`
	NetworkMode string            `yaml:"network_mode"`
	DependsOn   []string          `yaml:"depends_on"`
	StdinOpen   bool              `yaml:"stdin_open"`
	TTY         bool              `yaml:"tty"`
	CapAdd      []string          `yaml:"cap_add"`
	Ports       []string          `yaml:"ports"`
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

func volumeContains(vols []any, needle string) bool {
	for _, v := range vols {
		if s, ok := v.(string); ok && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// tmpfsVolumeTargets returns the 'target' of every long-form tmpfs volume.
func tmpfsVolumeTargets(vols []any) []string {
	var out []string
	for _, v := range vols {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] != "tmpfs" {
			continue
		}
		if t, ok := m["target"].(string); ok {
			out = append(out, t)
		}
	}
	return out
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

func TestGenerateOverlay_HostNetworkModeChangesNetworkAndDockerHost(t *testing.T) {
	spec := OverlaySpec{
		Image:       "img",
		HostNetwork: true,
		Project:     ProjectMount{HostPath: "/host/foo", Name: "foo"},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())
	svc := doc.Services["claude-sidecar"]

	if svc.NetworkMode != "host" {
		t.Errorf("network_mode: got %q want %q", svc.NetworkMode, "host")
	}
	if got := svc.Environment["DOCKER_HOST"]; got != "tcp://127.0.0.1:12375" {
		t.Errorf("DOCKER_HOST (host-network): got %q want %q", got, "tcp://127.0.0.1:12375")
	}
	// Socket-proxy must publish to loopback so the host-netns container can reach it.
	proxy := doc.Services["claude-sidecar-proxy"]
	want := "127.0.0.1:12375:2375"
	found := false
	for _, p := range proxy.Ports {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected proxy port publish %q, got: %v", want, proxy.Ports)
	}
}

func TestGenerateOverlay_EmitsEssentialServiceProperties(t *testing.T) {
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

	if svc.WorkingDir != "/workspaces/foo" {
		t.Errorf("working_dir: got %q want %q", svc.WorkingDir, "/workspaces/foo")
	}
	if !svc.StdinOpen {
		t.Errorf("stdin_open should be true")
	}
	if !svc.TTY {
		t.Errorf("tty should be true")
	}
	if got := svc.Environment["SIDECAR_CONFIG_DIR"]; got != "/workspaces/foo/.sidecar" {
		t.Errorf("SIDECAR_CONFIG_DIR: got %q want %q", got, "/workspaces/foo/.sidecar")
	}
	if got := svc.Environment["DOCKER_HOST"]; got != "tcp://claude-sidecar-proxy:2375" {
		t.Errorf("DOCKER_HOST (isolated mode): got %q want %q", got, "tcp://claude-sidecar-proxy:2375")
	}
	found := false
	for _, dep := range svc.DependsOn {
		if dep == "claude-sidecar-proxy" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected depends_on claude-sidecar-proxy, got: %v", svc.DependsOn)
	}
}

func TestGenerateOverlay_BindMountsExtraReposAndAppliesTheirShadows(t *testing.T) {
	spec := OverlaySpec{
		Image:   "img",
		Project: ProjectMount{HostPath: "/host/foo", Name: "foo"},
		ExtraMounts: []ProjectMount{
			{
				HostPath:    "/host/mobile",
				Name:        "mobile",
				ShadowPaths: []string{"firebase.json", "secrets/"},
			},
		},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())
	vols := doc.Services["claude-sidecar"].Volumes

	if !volumeContains(vols, "/host/mobile:/workspaces/mobile") {
		t.Errorf("expected extra-mount bind for mobile, got: %v", vols)
	}
	if !volumeContains(vols, "/dev/null:/workspaces/mobile/firebase.json") {
		t.Errorf("expected file shadow inside extra mount, got: %v", vols)
	}
	gotTmpfs := tmpfsVolumeTargets(vols)
	want := "/workspaces/mobile/secrets"
	found := false
	for _, g := range gotTmpfs {
		if g == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected tmpfs target %q for extra-mount dir shadow, got: %v", want, gotTmpfs)
	}
}

func TestGenerateOverlay_ShadowsCurrentProjectDirsViaTmpfs(t *testing.T) {
	spec := OverlaySpec{
		Image: "img",
		Project: ProjectMount{
			HostPath:    "/host/foo",
			Name:        "foo",
			ShadowPaths: []string{"secrets/", "config/local/"},
		},
	}
	var buf bytes.Buffer
	if err := GenerateOverlay(spec, &buf); err != nil {
		t.Fatalf("error: %v", err)
	}
	doc := parseOverlay(t, buf.Bytes())

	got := tmpfsVolumeTargets(doc.Services["claude-sidecar"].Volumes)
	want := []string{"/workspaces/foo/secrets", "/workspaces/foo/config/local"}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected tmpfs target %q, got tmpfs targets: %v (all volumes: %v)",
				w, got, doc.Services["claude-sidecar"].Volumes)
		}
	}
	// Ensure trailing slash is stripped (no double-slash in target).
	for _, g := range got {
		if strings.HasSuffix(g, "/") {
			t.Errorf("tmpfs target %q should not have trailing slash", g)
		}
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
