package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// config models ~/.claude-sidecar/config.yaml.
type config struct {
	Image       string   `yaml:"image"`
	HostNetwork bool     `yaml:"host_network"`
	ExtraMounts []string `yaml:"extra_mounts"`
}

// cfgDir returns the directory holding config.yaml + state/. Honors
// CLAUDE_SIDECAR_CFG_DIR; defaults to $HOME/.claude-sidecar.
func cfgDir() string {
	if d := os.Getenv("CLAUDE_SIDECAR_CFG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-sidecar")
}

func loadConfig(env Env) (config, error) {
	path := filepath.Join(cfgDir(), "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config{}, fmt.Errorf("config not found at %s — create it (image: ..., host_network: ..., extra_mounts: [...])", path)
		}
		return config{}, fmt.Errorf("read config: %w", err)
	}
	var c config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Image == "" {
		return config{}, fmt.Errorf("config %s: missing required 'image:' field", path)
	}
	return c, nil
}
