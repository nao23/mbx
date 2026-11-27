package config

import (
	"strings"
	"testing"
)

// A new file takes another file's settings, without its header, version and directories, and loads
func TestNewFileText(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	base := "# header\nversion: 1\n\ndirectories:\n  - ~/a\n  - ~/b\n\n# Keep it\nkeep: true\ndocker:\n  disk: 30G\n"
	got := string(newFileText("/home/u/src/app", []byte(base)))
	for _, want := range []string{`directories: ["~/src/app"]` + "\n", "# Keep it\nkeep: true\ndocker:\n  disk: 30G\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "header") || strings.Contains(got, "~/a") || strings.Count(got, "version:") != 1 {
		t.Errorf("header, version or directories copied:\n%s", got)
	}
	cfg, err := Parse([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Keep || cfg.Docker.Disk != "30G" || len(cfg.Directories) != 1 {
		t.Errorf("parsed = keep %v, disk %v, directories %v", cfg.Keep, cfg.Docker.Disk, cfg.Directories)
	}

	// For the home dir itself, ~ is quoted, as a bare one is null
	if got := string(newFileText("/home/u", nil)); !strings.Contains(got, `directories: ["~"]`) {
		t.Errorf("home dir:\n%s", got)
	}
}
