package wizard

import (
	"bytes"
	"errors"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Change sets the value at Path (keys from the top) in a config file.
type Change struct {
	Path  []string
	Value any
}

func (c Change) String() string {
	var n yaml.Node
	if err := n.Encode(c.Value); err != nil {
		return strings.Join(c.Path, ".")
	}
	setStyle(&n, true)
	out, _ := yaml.Marshal(&n)
	return strings.Join(c.Path, ".") + ": " + strings.TrimSpace(string(out))
}

// apply makes the changes to a config file's text, keeping its comments and the keys it doesn't change.
// Maps and short lists written are in flow style ({keyring: x}, [a, b]). A header replaces the comment
// at the top. Top-level keys in top go at the top when added, in that order; others go at the end.
func apply(data []byte, changes []Change, header string, top []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 { // an empty file
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if header != "" {
		// yaml.v3 puts the top comment on the document, the map or the first key
		m := doc.Content[0]
		doc.HeadComment, m.HeadComment = "# "+strings.ReplaceAll(header, "\n", "\n# "), ""
		if len(m.Content) > 0 {
			m.Content[0].HeadComment = ""
		}
	}
	for _, c := range changes {
		if err := set(doc.Content[0], c.Path, c.Value, top); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return respace(data, buf.Bytes()), nil
}

// respace puts back the blank lines yaml.v3 drops: one above each top-level key (with the comments right
// above it) that had one in before, and above keys added to a file that separates its keys so.
func respace(before, after []byte) []byte {
	spaced, any := map[string]bool{}, false
	lines := strings.Split(string(before), "\n")
	for i, line := range lines {
		if key, ok := topKey(line); ok {
			start := blockStart(lines, i)
			spaced[key] = start > 0 && strings.TrimSpace(lines[start-1]) == ""
			any = any || spaced[key]
		}
	}
	lines = strings.Split(string(after), "\n")
	var out []string
	for i, line := range lines {
		key, ok := topKey(line)
		was, existed := spaced[key]
		if ok && (was || !existed && any) {
			start := blockStart(lines, i)
			if start > 0 && strings.TrimSpace(lines[start-1]) != "" {
				at := len(out) - (i - start) // before the comments already copied
				out = append(out[:at], append([]string{""}, out[at:]...)...)
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// topKey returns the key a top-level line sets.
func topKey(line string) (string, bool) {
	if line == "" || strings.ContainsAny(line[:1], " #-") {
		return "", false
	}
	key, _, ok := strings.Cut(line, ":")
	return key, ok
}

// blockStart returns the first of the comment lines above line i (blank lines between them included), or i.
func blockStart(lines []string, i int) int {
	j := i
	for j > 0 && (strings.HasPrefix(lines[j-1], "#") || strings.TrimSpace(lines[j-1]) == "") {
		j--
	}
	for j < i && strings.TrimSpace(lines[j]) == "" {
		j++
	}
	return j
}

// set sets path in the map m, adding the maps on the way that are missing. A missing key goes at the end,
// or when in top, at the top after the keys before it in top.
func set(m *yaml.Node, path []string, value any, top []string) error {
	if m.Kind == yaml.ScalarNode && m.Tag == "!!null" { // a key with nothing after it
		*m = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: m.HeadComment, LineComment: m.LineComment}
	}
	if m.Kind != yaml.MappingNode {
		return errors.New(path[0] + "'s parent isn't a map")
	}
	key := path[0]
	var v *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v = m.Content[i+1]
			break
		}
	}
	if v == nil {
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: m.Style & yaml.FlowStyle}
		pair := []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v}
		at := len(m.Content)
		if i := slices.Index(top, key); i >= 0 {
			at = 0
			for at < len(m.Content) && slices.Contains(top[:i], m.Content[at].Value) {
				at += 2
			}
		}
		m.Content = append(m.Content[:at], append(pair, m.Content[at:]...)...)
	}
	if len(path) > 1 {
		return set(v, path[1:], value, nil)
	}

	var n yaml.Node
	if err := n.Encode(value); err != nil {
		return err
	}
	setStyle(&n, m.Style&yaml.FlowStyle != 0)
	n.HeadComment, n.LineComment, n.FootComment = v.HeadComment, v.LineComment, v.FootComment
	*v = n
	return nil
}

// setStyle makes maps and short lists flow style, or everything when flow (inside a flow map).
func setStyle(n *yaml.Node, flow bool) {
	switch n.Kind {
	case yaml.MappingNode:
		n.Style = yaml.FlowStyle
	case yaml.SequenceNode:
		width := 0
		for _, item := range n.Content {
			width += len(item.Value) + 2
		}
		if flow || width <= 60 {
			n.Style = yaml.FlowStyle
		}
	}
}
