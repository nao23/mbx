package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultsYAML []byte

// Lists appended to instead of replaced
var appendPaths = map[string]bool{"microsandbox_config.network.allow": true}

// replaced reports whether a map is replaced whole instead of merged, so two secret sources don't mix
func replaced(path, key string) bool {
	return key == "secret_source" || path == "secret_sources"
}

// Load layers one config file over the defaults: file if not empty, else the one for the workspace (see
// Choose), or none. Config files aren't layered over each other.
func Load(workspace, file string) (*Config, error) {
	path, why := file, "given with --config"
	if file == "" {
		var matched string
		var err error
		if path, matched, err = Choose(workspace); err != nil {
			return nil, err
		}
		why = "its directories has " + matched
		if path == "" {
			why = "no config file's directories has " + workspace + ": the defaults alone"
		}
	} else if _, err := os.Stat(file); err != nil {
		return nil, err
	}

	m := map[string]any{}
	if path != "" {
		var err error
		if m, err = readConfigFile(path); err != nil {
			return nil, err
		}
	}
	cfg, err := layer(m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.dir, cfg.path, cfg.why = Dir(), path, why
	if path != "" {
		cfg.dir = filepath.Dir(path)
	}

	return cfg, nil
}

// Parse layers a config file's text over the defaults, as Load does.
func Parse(data []byte) (*Config, error) {
	m, err := parseYAML(data)
	if err != nil {
		return nil, err
	}
	return layer(m)
}

func layer(m map[string]any) (*Config, error) {
	merged, err := parseYAML(defaultsYAML)
	if err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	return decode(merge(merged, m, ""))
}

// Choose picks the config file for the workspace from the config dir's *.yaml: the one whose directories
// has the workspace or the deepest of its parents (matched), or none (an empty path). Every file lists
// some (directories: ["~"] makes one for anywhere under the home dir that no other file covers). A directory
// in two files is an error, whichever the workspace
func Choose(workspace string) (path, matched string, err error) {
	dir := Dir()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return "", "", err
	}
	listedIn := map[string]string{} // directory -> the file listing it
	deepest := ""
	for _, f := range files {
		m, err := readConfigFile(f)
		if err != nil {
			return "", "", err
		}
		dirs := StringList(m["directories"])
		if len(dirs) == 0 {
			return "", "", fmt.Errorf(`%s: directories: list the directories it's used in ("~" for anywhere under the home dir)`, f)
		}
		for _, d := range dirs {
			if d == "<nil>" {
				return "", "", fmt.Errorf(`%s: directories: a bare ~ is null in YAML; write "~" for the home dir`, f)
			}
			d = filepath.Clean(ExpandHome(d))
			if !filepath.IsAbs(d) {
				return "", "", fmt.Errorf("%s: directories: %s isn't an absolute path (or one starting with ~)", f, d)
			}
			if other, ok := listedIn[d]; ok && other != f {
				return "", "", fmt.Errorf("directories: %s is in both %s and %s", d, other, f)
			}
			listedIn[d] = f
			if rel, err := filepath.Rel(d, workspace); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") &&
				len(d) > len(deepest) {
				deepest = d
			}
		}
	}
	if deepest != "" {
		return listedIn[deepest], deepest, nil
	}
	return "", "", nil
}

// readConfigFile reads a config file. A missing one is empty
func readConfigFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func decode(merged map[string]any) (*Config, error) {
	// Back to YAML text, as KnownFields (rejecting unknown keys) works only when decoding text
	buf, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	cfg := Config{Agent: "claude"}
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseYAML(data []byte) (map[string]any, error) {
	m := map[string]any{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// merge layers maps key by key; scalars and lists are replaced. Maps, not Configs, as a struct can't tell
// an omitted key from a zero value
func merge(dst, src map[string]any, path string) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, sv := range src {
		if sv == nil {
			continue
		}
		p := k
		if path != "" {
			p = path + "." + k
		}
		dv, ok := out[k]
		dm, dIsMap := dv.(map[string]any)
		sm, sIsMap := sv.(map[string]any)
		switch {
		case ok && dIsMap && sIsMap && !replaced(path, k):
			out[k] = merge(dm, sm, p)
		case ok && appendPaths[p]:
			out[k] = AppendUnique(StringList(dv), StringList(sv)...)
		default:
			out[k] = sv
		}
	}
	return out
}
