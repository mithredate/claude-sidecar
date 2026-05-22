package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox sets up an isolated test environment:
//   - cfgDir:  fake ~/.claude-sidecar
//   - projDir: fake project (cwd during the test)
//   - binDir:  fake bin/ with a stub `docker` on PATH
// Tests call s.write(...) to fill config/shadow files, then s.run(...) to
// invoke the wrapper's Run() function against the sandbox.
type sandbox struct {
	t        *testing.T
	cfgDir   string
	projDir  string
	binDir   string
	dockerLog string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := t.TempDir()
	s := &sandbox{
		t:         t,
		cfgDir:    filepath.Join(root, "cfg"),
		projDir:   filepath.Join(root, "project"),
		binDir:    filepath.Join(root, "bin"),
		dockerLog: filepath.Join(root, "docker.log"),
	}
	for _, d := range []string{s.cfgDir, s.projDir, s.binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// Stub docker: log each arg on its own line, plus stdin between markers.
	stub := `#!/usr/bin/env bash
{
    for a in "$@"; do printf 'ARG: %s\n' "$a"; done
    if [ ! -t 0 ]; then
        printf 'STDIN_BEGIN\n'
        cat
        printf '\nSTDIN_END\n'
    fi
    printf 'INVOCATION_END\n'
} >> "$DOCKER_LOG"
exit 0
`
	if err := os.WriteFile(filepath.Join(s.binDir, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	return s
}

// write creates a file under the sandbox at the given relative path
// (interpreted relative to either cfgDir, projDir, or an absolute path).
func (s *sandbox) writeCfg(name, content string) {
	s.t.Helper()
	full := filepath.Join(s.cfgDir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatalf("mkdir for %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		s.t.Fatalf("write %s: %v", full, err)
	}
}

func (s *sandbox) writeProj(name, content string) {
	s.t.Helper()
	full := filepath.Join(s.projDir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatalf("mkdir for %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		s.t.Fatalf("write %s: %v", full, err)
	}
}

// run invokes the wrapper's Run() against the sandbox.
func (s *sandbox) run(args ...string) (stdout, stderr string, code int) {
	s.t.Helper()
	s.t.Setenv("CLAUDE_SIDECAR_CFG_DIR", s.cfgDir)
	s.t.Setenv("PATH", s.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.t.Setenv("DOCKER_LOG", s.dockerLog)
	if err := os.Truncate(s.dockerLog, 0); err != nil && !os.IsNotExist(err) {
		// just clear it (may not exist yet)
	}
	_ = os.WriteFile(s.dockerLog, nil, 0o644)

	var outBuf, errBuf bytes.Buffer
	env := Env{
		Stdout:  &outBuf,
		Stderr:  &errBuf,
		WorkDir: s.projDir,
	}
	code = Run(args, env)
	return outBuf.String(), errBuf.String(), code
}

// dockerLog reads the captured docker invocations.
func (s *sandbox) docker() string {
	s.t.Helper()
	b, err := os.ReadFile(s.dockerLog)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestRun_Exec_CallsComposeExecAgainstClaudeSidecar(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", "image: img\nhost_network: false\n")
	s.writeProj("compose.sidecar.yml", "services: {}\n") // already up'd

	_, stderr, code := s.run("exec", "ls", "-la")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{"compose", "exec", "claude-sidecar", "ls", "-la"} {
		if !strings.Contains(s.docker(), "ARG: "+want) {
			t.Errorf("expected docker arg %q, log:\n%s", want, s.docker())
		}
	}
}

func TestRun_Bare_RunsClaudeInsideContainer(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", "image: img\nhost_network: false\n")
	s.writeProj("compose.sidecar.yml", "services: {}\n")

	_, stderr, code := s.run()
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(s.docker(), "ARG: claude") {
		t.Errorf("expected 'claude' to be passed as the command; log:\n%s", s.docker())
	}
	if !strings.Contains(s.docker(), "ARG: claude-sidecar") {
		t.Errorf("expected target service 'claude-sidecar'; log:\n%s", s.docker())
	}
}

func TestRun_Exec_WarnsOnShadowDrift(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", "image: img\nhost_network: false\n")
	s.writeProj("compose.sidecar.yml", "services: {}\n")
	s.writeProj(".sidecar/shadow", ".env\n")
	// Stored hash that doesn't match current shadow:
	stateDir := filepath.Join(s.cfgDir, "state")
	_ = os.MkdirAll(stateDir, 0o755)
	_ = os.WriteFile(filepath.Join(stateDir, filepath.Base(s.projDir)+".sha"),
		[]byte("bogus-prior-hash\n"), 0o644)

	_, stderr, code := s.run("exec", "ls")
	if code != 0 {
		t.Fatalf("exit (drift should still proceed): %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "drift") {
		t.Errorf("expected stderr to mention drift, got: %s", stderr)
	}
}

func TestRun_Exec_SilentWhenNoDrift(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", "image: img\nhost_network: false\n")
	s.writeProj("compose.sidecar.yml", "services: {}\n")
	s.writeProj(".sidecar/shadow", ".env\n")
	// First an `up` to record the matching hash:
	if _, _, code := s.run("up"); code != 0 {
		t.Fatalf("up failed; can't test no-drift path")
	}

	_, stderr, code := s.run("exec", "ls")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	if strings.Contains(stderr, "drift") {
		t.Errorf("expected no drift warning; got stderr: %s", stderr)
	}
}

func TestRun_Up_WritesOverlayFileAndCallsComposeUp(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", `image: img
host_network: false
`)
	s.writeProj("compose.yml", "services: {app: {image: alpine}}\n")

	_, stderr, code := s.run("up")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}

	// compose.sidecar.yml must exist at project root
	overlayPath := filepath.Join(s.projDir, "compose.sidecar.yml")
	if _, err := os.Stat(overlayPath); err != nil {
		t.Errorf("expected %s to exist: %v", overlayPath, err)
	} else {
		b, _ := os.ReadFile(overlayPath)
		if !strings.Contains(string(b), "claude-sidecar:") {
			t.Errorf("compose.sidecar.yml missing claude-sidecar service:\n%s", b)
		}
	}

	// hash file must be written to state dir
	hashPath := filepath.Join(s.cfgDir, "state", filepath.Base(s.projDir)+".sha")
	if _, err := os.Stat(hashPath); err != nil {
		t.Errorf("expected hash file %s to exist: %v", hashPath, err)
	}

	// docker compose -f compose.yml -f compose.sidecar.yml up -d
	log := s.docker()
	for _, want := range []string{"compose", "-f", "compose.yml", "compose.sidecar.yml", "up", "-d"} {
		if !strings.Contains(log, "ARG: "+want) {
			t.Errorf("expected docker arg %q in invocation log:\n%s", want, log)
		}
	}
}

func TestRun_Up_MergesComposeSidecarLocalWhenPresent(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", `image: img
host_network: false
`)
	s.writeProj("compose.sidecar-local.yml", "services: {}\n")

	_, stderr, code := s.run("up")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(s.docker(), "ARG: compose.sidecar-local.yml") {
		t.Errorf("expected compose.sidecar-local.yml in docker args:\n%s", s.docker())
	}
}

func TestRun_GenOverlay_IncludesExtraMountsAndTheirShadows(t *testing.T) {
	s := newSandbox(t)
	// Create the extra-mount repo alongside the project.
	extraDir := filepath.Join(filepath.Dir(s.projDir), "mobile")
	if err := os.MkdirAll(filepath.Join(extraDir, ".sidecar"), 0o755); err != nil {
		t.Fatalf("mkdir extra: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extraDir, ".sidecar", "shadow"),
		[]byte("firebase.json\nsecrets/\n"), 0o644); err != nil {
		t.Fatalf("write extra shadow: %v", err)
	}

	s.writeCfg("config.yaml", `image: img
host_network: false
extra_mounts:
  - `+extraDir+`
`)

	stdout, stderr, code := s.run("gen-overlay")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}

	if !strings.Contains(stdout, extraDir+":/workspaces/mobile") {
		t.Errorf("expected extra-mount bind %q in stdout, got:\n%s",
			extraDir+":/workspaces/mobile", stdout)
	}
	if !strings.Contains(stdout, "/dev/null:/workspaces/mobile/firebase.json") {
		t.Errorf("expected file shadow under extra mount in stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/workspaces/mobile/secrets") || !strings.Contains(stdout, "tmpfs") {
		t.Errorf("expected tmpfs dir shadow under extra mount, got:\n%s", stdout)
	}
}

func TestRun_GenOverlay_HostNetworkTrueFlowsThrough(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", `image: img
host_network: true
`)
	stdout, stderr, code := s.run("gen-overlay")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "network_mode: host") {
		t.Errorf("expected network_mode: host in stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "127.0.0.1:12375") {
		t.Errorf("expected DOCKER_HOST/proxy port 127.0.0.1:12375 in stdout, got:\n%s", stdout)
	}
}

func TestRun_GenOverlay_IncludesCurrentProjectShadowPaths(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", `image: img
host_network: false
`)
	s.writeProj(".sidecar/shadow", `.env
.credentials.json
# a comment, should be filtered

secrets/
`)

	stdout, stderr, code := s.run("gen-overlay")
	if code != 0 {
		t.Fatalf("exit: %d, stderr: %s", code, stderr)
	}
	projName := filepath.Base(s.projDir)
	// File shadows bind /dev/null at the workspace container path, not at the host path.
	wantMounts := []string{
		"/dev/null:/workspaces/" + projName + "/.env",
		"/dev/null:/workspaces/" + projName + "/.credentials.json",
	}
	for _, w := range wantMounts {
		if !strings.Contains(stdout, w) {
			t.Errorf("expected shadow mount %q in stdout, got:\n%s", w, stdout)
		}
	}
	// Dir shadow secrets/ → tmpfs at /workspaces/<name>/secrets
	wantTmpfsTarget := "/workspaces/" + projName + "/secrets"
	if !strings.Contains(stdout, wantTmpfsTarget) || !strings.Contains(stdout, "tmpfs") {
		t.Errorf("expected tmpfs target %q in stdout, got:\n%s", wantTmpfsTarget, stdout)
	}
	// Comment should not leak into the spec as a shadow path:
	if strings.Contains(stdout, "a comment") {
		t.Errorf("comment line leaked into overlay; got:\n%s", stdout)
	}
}

func TestRun_GenOverlay_EmitsValidYamlWithConfiguredImage(t *testing.T) {
	s := newSandbox(t)
	s.writeCfg("config.yaml", `image: ghcr.io/example/sidecar:test
host_network: false
`)

	stdout, stderr, code := s.run("gen-overlay")
	if code != 0 {
		t.Fatalf("exit code: got %d want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ghcr.io/example/sidecar:test") {
		t.Errorf("expected configured image in stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "claude-sidecar:") {
		t.Errorf("expected 'claude-sidecar:' service header in YAML, got:\n%s", stdout)
	}
}
