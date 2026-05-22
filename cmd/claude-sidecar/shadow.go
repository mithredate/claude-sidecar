package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// readShadowFile returns the list of paths declared in the given shadow file.
// Returns an empty slice (and no error) if the file is missing — declaring
// shadows is optional. Blank lines and `# comments` are skipped; surrounding
// whitespace is trimmed; trailing-slash directory markers are preserved.
func readShadowFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// strip inline comment, then trim
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// projectShadowPaths reads <projRoot>/.sidecar/shadow and returns its entries.
func projectShadowPaths(projRoot string) ([]string, error) {
	return readShadowFile(filepath.Join(projRoot, ".sidecar", "shadow"))
}
