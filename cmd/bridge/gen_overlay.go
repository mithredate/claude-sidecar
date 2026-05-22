package main

import (
	"fmt"
	"io"

	"github.com/mithredate/claude-sidecar/internal/overlay"
	"gopkg.in/yaml.v3"
)

// runGenOverlay is the CLI handler for `bridge gen-overlay`: read a YAML
// overlay.Spec from r, write the generated Compose YAML to w. Returns the
// process exit code (0 = ok, 1 = bad input / write error).
func runGenOverlay(r io.Reader, w io.Writer, errW io.Writer) int {
	specBytes, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(errW, "gen-overlay: read stdin: %s\n", err)
		return 1
	}
	var spec overlay.Spec
	if err := yaml.Unmarshal(specBytes, &spec); err != nil {
		fmt.Fprintf(errW, "gen-overlay: parse spec yaml: %s\n", err)
		return 1
	}
	if err := overlay.Generate(spec, w); err != nil {
		fmt.Fprintf(errW, "gen-overlay: generate: %s\n", err)
		return 1
	}
	return 0
}
