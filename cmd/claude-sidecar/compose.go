package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// composeFiles returns the list of -f arguments to pass to `docker compose`
// for the given project: the user's compose.yml/.yaml if present, the
// generated compose.sidecar.yml, plus the optional compose.sidecar-local.yml.
func composeFiles(projRoot string) []string {
	var out []string
	for _, n := range []string{"compose.yml", "compose.yaml"} {
		if _, err := os.Stat(filepath.Join(projRoot, n)); err == nil {
			out = append(out, "-f", n)
		}
	}
	out = append(out, "-f", "compose.sidecar.yml")
	if _, err := os.Stat(filepath.Join(projRoot, "compose.sidecar-local.yml")); err == nil {
		out = append(out, "-f", "compose.sidecar-local.yml")
	}
	return out
}

// runDockerCompose execs `docker compose <fileArgs> <args>` with cwd at projRoot.
func runDockerCompose(env Env, projRoot string, fileArgs []string, args []string) int {
	all := append([]string{"compose"}, fileArgs...)
	all = append(all, args...)
	cmd := exec.Command("docker", all...)
	cmd.Dir = projRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(env.Stderr, "claude-sidecar: docker compose: %s\n", err)
		return 1
	}
	return 0
}

// shadowHash returns a SHA-256 hex digest of all shadow file contents for the
// given spec, including a marker per extra-mount. Stable input ordering, so
// equal specs hash equal.
func shadowHash(spec hashInputs) string {
	h := sha256.New()
	h.Write([]byte(spec.projectShadow))
	for _, m := range spec.extras {
		fmt.Fprintf(h, "EXTRA:%s\n", m.path)
		h.Write([]byte(m.shadow))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashInputs is the deterministic content fed into shadowHash.
type hashInputs struct {
	projectShadow string
	extras        []extraHash
}
type extraHash struct {
	path   string
	shadow string
}

func collectHashInputs(projRoot string, cfg config) (hashInputs, error) {
	var in hashInputs
	if b, err := os.ReadFile(filepath.Join(projRoot, ".sidecar", "shadow")); err == nil {
		in.projectShadow = string(b)
	} else if !os.IsNotExist(err) {
		return in, err
	}
	for _, m := range cfg.ExtraMounts {
		m = expandHome(m)
		var s string
		if b, err := os.ReadFile(filepath.Join(m, ".sidecar", "shadow")); err == nil {
			s = string(b)
		} else if !os.IsNotExist(err) {
			return in, err
		}
		in.extras = append(in.extras, extraHash{path: m, shadow: s})
	}
	return in, nil
}
