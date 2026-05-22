package main

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// OverlaySpec is the full, pre-resolved description gen-overlay turns into a
// compose.sidecar.yml. The caller (host wrapper) does all filesystem IO and
// constructs this; gen-overlay is a pure transformer.
type OverlaySpec struct {
	Image       string         `yaml:"image"`
	HostNetwork bool           `yaml:"host_network"`
	Project     ProjectMount   `yaml:"project"`
	ExtraMounts []ProjectMount `yaml:"extra_mounts"`
}

// ProjectMount describes one mounted repo (current project or an extra mount).
// ShadowPaths are repo-relative; trailing '/' means directory.
type ProjectMount struct {
	HostPath    string   `yaml:"host_path"`
	Name        string   `yaml:"name"`
	ShadowPaths []string `yaml:"shadow_paths"`
}

// GenerateOverlay writes a Compose YAML fragment describing the claude-sidecar
// setup (services, mounts, shadows) for the given spec.
func GenerateOverlay(spec OverlaySpec, w io.Writer) error {
	doc := map[string]any{
		"services": map[string]any{
			"claude-sidecar": map[string]any{
				"image": spec.Image,
				"volumes": []string{
					fmt.Sprintf("%s:/workspaces/%s", spec.Project.HostPath, spec.Project.Name),
				},
			},
		},
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer enc.Close()
	return enc.Encode(doc)
}
