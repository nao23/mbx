package mcpgateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

// toolPolicy is which tools of a claude.ai connector are blocked in its claude.ai settings.
type toolPolicy struct {
	permissions       map[string]string // tool -> effective_max_permission (allow / ask / blocked)
	defaultPermission string            // for tools not in permissions
}

func (p toolPolicy) blocks(tool string) bool {
	if perm, ok := p.permissions[tool]; ok {
		return perm == "blocked"
	}
	return p.defaultPermission == "blocked"
}

func (p toolPolicy) empty() bool {
	return len(p.blockedNames()) == 0 && p.defaultPermission != "blocked"
}

func (p toolPolicy) blockedNames() []string {
	var names []string
	for tool, perm := range p.permissions {
		if perm == "blocked" {
			names = append(names, tool)
		}
	}
	sort.Strings(names)
	return names
}

// findConnector returns the connector's ID and tool permissions.
func findConnector(token, display string) (string, toolPolicy, error) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/mcp_servers?limit=1000", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "mcp-servers-2025-12-04")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", toolPolicy{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return "", toolPolicy{}, fmt.Errorf("list claude.ai connectors: %s: %s", resp.Status, body)
	}
	var list struct {
		Data []struct {
			ID                   string `json:"id"`
			DisplayName          string `json:"display_name"`
			DefaultMaxPermission string `json:"default_max_permission"`
			Tools                []struct {
				Name                   string `json:"name"`
				EffectiveMaxPermission string `json:"effective_max_permission"`
			} `json:"tools"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", toolPolicy{}, err
	}
	for _, s := range list.Data {
		if s.DisplayName != display {
			continue
		}
		p := toolPolicy{permissions: map[string]string{}, defaultPermission: s.DefaultMaxPermission}
		for _, t := range s.Tools {
			p.permissions[t.Name] = t.EffectiveMaxPermission
		}
		return s.ID, p, nil
	}
	return "", toolPolicy{}, fmt.Errorf("no claude.ai connector named %q", display)
}

// blockedCall reports whether a JSON-RPC request (or batch) calls a blocked tool, with its id and name.
func (p toolPolicy) blockedCall(body []byte) (id any, tool string, ok bool) {
	var msgs []map[string]any
	if err := json.Unmarshal(body, &msgs); err != nil {
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			return nil, "", false
		}
		msgs = []map[string]any{m}
	}
	for _, m := range msgs {
		if m["method"] != "tools/call" {
			continue
		}
		params, _ := m["params"].(map[string]any)
		if name, _ := params["name"].(string); p.blocks(name) {
			return m["id"], name, true
		}
	}
	return nil, "", false
}

// filterMessage removes blocked tools from a tools/list result. Anything else is returned unchanged.
func (p toolPolicy) filterMessage(raw []byte) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	changed := false
	filter := func(m map[string]any) {
		result, _ := m["result"].(map[string]any)
		tools, ok := result["tools"].([]any)
		if !ok {
			return
		}
		kept := tools[:0:0]
		for _, t := range tools {
			if tm, _ := t.(map[string]any); tm != nil && p.blocks(fmt.Sprint(tm["name"])) {
				changed = true
				continue
			}
			kept = append(kept, t)
		}
		result["tools"] = kept
	}
	switch m := v.(type) {
	case map[string]any:
		filter(m)
	case []any:
		for _, e := range m {
			if em, ok := e.(map[string]any); ok {
				filter(em)
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

func (p toolPolicy) filterSSE(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		r := bufio.NewReader(body)
		for {
			line, err := r.ReadBytes('\n')
			if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				line = append(append([]byte("data: "), p.filterMessage(bytes.TrimSpace(data))...), '\n')
			}
			if len(line) > 0 {
				if _, werr := pw.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}
