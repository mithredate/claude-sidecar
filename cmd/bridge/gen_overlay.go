package main

import (
	"fmt"
	"io"
	"strings"

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
	claudeVolumes := []any{
		fmt.Sprintf("%s:/workspaces/%s", spec.Project.HostPath, spec.Project.Name),
		"claude-home:/home/claude",
		fmt.Sprintf("%s/.credentials.json:/run/seed/.credentials.json:ro", spec.Project.HostPath),
	}
	for _, p := range spec.Project.ShadowPaths {
		claudeVolumes = append(claudeVolumes, shadowEntry("/workspaces/"+spec.Project.Name, p))
	}
	for _, em := range spec.ExtraMounts {
		mountRoot := "/workspaces/" + em.Name
		claudeVolumes = append(claudeVolumes, fmt.Sprintf("%s:%s", em.HostPath, mountRoot))
		for _, p := range em.ShadowPaths {
			claudeVolumes = append(claudeVolumes, shadowEntry(mountRoot, p))
		}
	}
	workspaceRoot := "/workspaces/" + spec.Project.Name

	dockerHost := "tcp://claude-sidecar-proxy:2375"
	if spec.HostNetwork {
		// In host netns, service-name DNS doesn't resolve. Reach the proxy
		// through the loopback port published by claude-sidecar-proxy.
		dockerHost = "tcp://127.0.0.1:12375"
	}

	claudeService := map[string]any{
		"image":       spec.Image,
		"volumes":     claudeVolumes,
		"working_dir": workspaceRoot,
		"stdin_open":  true,
		"tty":         true,
		"environment": map[string]any{
			"SIDECAR_CONFIG_DIR": workspaceRoot + "/.sidecar",
			"DOCKER_HOST":        dockerHost,
		},
		"depends_on": []string{"claude-sidecar-proxy"},
	}
	if spec.HostNetwork {
		claudeService["network_mode"] = "host"
	}

	proxyService := map[string]any{
		"image": "tecnativa/docker-socket-proxy",
		"environment": map[string]any{
			"CONTAINERS": 1,
			"EXEC":       1,
			"POST":       1,
		},
		"volumes": []string{
			"/var/run/docker.sock:/var/run/docker.sock:ro",
		},
	}
	if spec.HostNetwork {
		proxyService["ports"] = []string{"127.0.0.1:12375:2375"}
	}

	doc := map[string]any{
		"services": map[string]any{
			"claude-sidecar":       claudeService,
			"claude-sidecar-proxy": proxyService,
		},
		"volumes": map[string]any{
			"claude-home": map[string]any{
				"name":     "claude-sidecar-home",
				"external": true,
			},
		},
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer enc.Close()
	return enc.Encode(doc)
}

// shadowEntry returns a Compose volume entry that hides the given path. For
// files (no trailing '/'), returns a short-form string mounting /dev/null. For
// directories (trailing '/'), returns a long-form tmpfs mount (Docker rejects
// /dev/null over a directory). The trailing slash is stripped from the target.
func shadowEntry(containerMountRoot, relPath string) any {
	isDir := strings.HasSuffix(relPath, "/")
	relPath = strings.TrimRight(relPath, "/")
	target := containerMountRoot + "/" + relPath
	if isDir {
		return map[string]any{
			"type":   "tmpfs",
			"target": target,
		}
	}
	return "/dev/null:" + target
}
