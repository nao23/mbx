// Package testutil has helpers shared by the tests.
package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteConfig writes yamlText, dedented so it can be indented with the test code, as default.yaml in a new
// config dir and points MBX_CONFIG_DIR at it. Without directories in yamlText, the file gets
// directories: [/], so it's used everywhere. It returns the config dir.
func WriteConfig(t *testing.T, yamlText string) string {
	t.Helper()
	dir := t.TempDir()
	text := Dedent(yamlText)
	if !strings.HasPrefix(text, "directories:") && !strings.Contains(text, "\ndirectories:") {
		text = "directories: [/]\n" + text
	}
	if err := os.WriteFile(filepath.Join(dir, "default.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MBX_CONFIG_DIR", dir)
	return dir
}

// Dedent removes the indentation common to the non-blank lines, the blank first line,
// and the closing backquote's indentation.
func Dedent(s string) string {
	lines := strings.Split(strings.TrimPrefix(s, "\n"), "\n")
	prefix, first := "", true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first {
			prefix, first = indent, false
		}
		for !strings.HasPrefix(indent, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
		} else {
			lines[i] = strings.TrimPrefix(line, prefix)
		}
	}
	return strings.Join(lines, "\n")
}
