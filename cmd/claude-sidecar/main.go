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
	default:
		fmt.Fprintf(env.Stderr, "claude-sidecar: unknown subcommand %q (try: gen-overlay)\n", sub)
		return 1
	}
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

// buildSpec constructs the overlay.Spec for the current project. Extra-mount
// shadow reading is layered in by subsequent behaviors.
func buildSpec(cfg config, projRoot string) (overlay.Spec, error) {
	shadows, err := projectShadowPaths(projRoot)
	if err != nil {
		return overlay.Spec{}, err
	}
	return overlay.Spec{
		Image:       cfg.Image,
		HostNetwork: cfg.HostNetwork,
		Project: overlay.ProjectMount{
			HostPath:    projRoot,
			Name:        filepath.Base(projRoot),
			ShadowPaths: shadows,
		},
	}, nil
}

func main() {
	os.Exit(Run(os.Args[1:], Env{}))
}
