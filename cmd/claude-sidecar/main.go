// claude-sidecar is the host-side wrapper that orchestrates the claude-sidecar
// container setup for the current project. It reads ~/.claude-sidecar/config.yaml,
// reads .sidecar/shadow files in the current project and any extra-mount repos,
// builds an overlay.Spec, and emits compose.sidecar.yml (in-process — no docker
// round-trip). It then invokes `docker compose -f compose.yml -f compose.sidecar.yml`
// for up/exec operations.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithredate/claude-sidecar/internal/overlay"
)

// Env captures the environment that Run executes against, so tests can inject
// alternative writers and working directories without touching globals.
type Env struct {
	Stdout  io.Writer
	Stderr  io.Writer
	WorkDir string // project root (defaults to current working dir)
}

// Run dispatches the wrapper's subcommands. Returns the process exit code.
func Run(args []string, env Env) int {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}
	if env.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(env.Stderr, "claude-sidecar: getwd: %s\n", err)
			return 1
		}
		env.WorkDir = wd
	}

	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "gen-overlay":
		return cmdGenOverlay(env, args)
	case "up":
		return cmdUp(env, args)
	case "exec":
		return cmdExec(env, args)
	case "":
		return cmdExec(env, []string{"claude"})
	default:
		fmt.Fprintf(env.Stderr, "claude-sidecar: unknown subcommand %q (try: gen-overlay, up, exec)\n", sub)
		return 1
	}
}

func cmdExec(env Env, args []string) int {
	cfg, err := loadConfig(env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: %s\n", err)
		return 1
	}
	// Non-fatal drift check.
	if drift, _ := checkDrift(env.WorkDir, cfg); drift {
		fmt.Fprintln(env.Stderr,
			"claude-sidecar: WARNING shadow drift detected since last `up` — run `claude-sidecar up` to apply")
	}
	composeArgs := append(composeFiles(env.WorkDir),
		append([]string{"exec", "claude-sidecar"}, args...)...)
	return runDockerCompose(env, env.WorkDir, nil, composeArgs)
}

// checkDrift returns true if the current shadow hash differs from the one
// recorded at the last `up`. An absent state file (never up'd) is "no drift".
func checkDrift(projRoot string, cfg config) (bool, error) {
	statePath := filepath.Join(cfgDir(), "state", filepath.Base(projRoot)+".sha")
	prior, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	in, err := collectHashInputs(projRoot, cfg)
	if err != nil {
		return false, err
	}
	current := shadowHash(in)
	return strings.TrimSpace(string(prior)) != current, nil
}

func cmdUp(env Env, args []string) int {
	cfg, err := loadConfig(env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: %s\n", err)
		return 1
	}
	spec, err := buildSpec(cfg, env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: build spec: %s\n", err)
		return 1
	}

	// Write the overlay file.
	overlayPath := filepath.Join(env.WorkDir, "compose.sidecar.yml")
	f, err := os.Create(overlayPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: create overlay: %s\n", err)
		return 1
	}
	if err := overlay.Generate(spec, f); err != nil {
		f.Close()
		fmt.Fprintf(env.Stderr, "claude-sidecar: write overlay: %s\n", err)
		return 1
	}
	f.Close()

	// Record the drift hash so subsequent `exec` calls can detect divergence.
	if err := writeDriftHash(env.WorkDir, cfg); err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: record drift hash: %s\n", err)
		return 1
	}

	// docker compose -f ... up -d (forwarding any extra args the user passed).
	composeArgs := append(composeFiles(env.WorkDir), append([]string{"up", "-d"}, args...)...)
	return runDockerCompose(env, env.WorkDir, nil, composeArgs)
}

// writeDriftHash records the SHA-256 of all relevant shadow contents under
// the config's state/<project>.sha file.
func writeDriftHash(projRoot string, cfg config) error {
	in, err := collectHashInputs(projRoot, cfg)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(cfgDir(), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(stateDir, filepath.Base(projRoot)+".sha")
	return os.WriteFile(path, []byte(shadowHash(in)+"\n"), 0o644)
}

func cmdGenOverlay(env Env, _ []string) int {
	cfg, err := loadConfig(env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: %s\n", err)
		return 1
	}
	spec, err := buildSpec(cfg, env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: build spec: %s\n", err)
		return 1
	}
	if err := overlay.Generate(spec, env.Stdout); err != nil {
		fmt.Fprintf(env.Stderr, "claude-sidecar: generate overlay: %s\n", err)
		return 1
	}
	return 0
}

// buildSpec constructs the overlay.Spec for the current project, including
// any extra-mount repos declared in the user's config (each contributing its
// own .sidecar/shadow declarations).
func buildSpec(cfg config, projRoot string) (overlay.Spec, error) {
	shadows, err := projectShadowPaths(projRoot)
	if err != nil {
		return overlay.Spec{}, err
	}
	spec := overlay.Spec{
		Image:       cfg.Image,
		HostNetwork: cfg.HostNetwork,
		Project: overlay.ProjectMount{
			HostPath:    projRoot,
			Name:        filepath.Base(projRoot),
			ShadowPaths: shadows,
		},
	}
	for _, m := range cfg.ExtraMounts {
		m = expandHome(m)
		emShadows, err := projectShadowPaths(m)
		if err != nil {
			return overlay.Spec{}, fmt.Errorf("extra-mount %s: %w", m, err)
		}
		spec.ExtraMounts = append(spec.ExtraMounts, overlay.ProjectMount{
			HostPath:    m,
			Name:        filepath.Base(m),
			ShadowPaths: emShadows,
		})
	}
	return spec, nil
}

// expandHome expands a leading "~" in p to the user's home directory.
func expandHome(p string) string {
	if len(p) == 0 || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if len(p) > 1 && p[1] == '/' {
		return home + p[1:]
	}
	return p
}

func main() {
	os.Exit(Run(os.Args[1:], Env{}))
}
