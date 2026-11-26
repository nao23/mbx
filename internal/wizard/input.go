package wizard

import (
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/nao23/mbx/internal/secret"
)

// Input is how a setting's value is taken from the config, shown in the menu, and asked.
type Input interface {
	value(raw any) any // from the value in the config, as decoded from YAML
	show(v any) string
	ask(s *Setting, v any, values Values, fresh bool) (any, error)
}

// Option is one of a Choice's values, with its label.
type Option struct{ Value, Label string }

// Choice picks one of Options: a string.
type Choice struct{ Options []Option }

func (Choice) value(raw any) any { s, _ := raw.(string); return s }
func (Choice) show(v any) string { return v.(string) }
func (c Choice) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := v.(string)
	var options []huh.Option[string]
	for _, o := range c.Options {
		options = append(options, huh.NewOption(o.Label, o.Value))
	}
	err := Ask(s.Title, huh.NewSelect[string]().Description(s.Description).Options(options...).Value(&value))
	return value, err
}

// Toggle is a yes or no: a bool.
type Toggle struct{}

func (Toggle) value(raw any) any { b, _ := raw.(bool); return b }
func (Toggle) show(v any) string { return onOff(v.(bool)) }
func (Toggle) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := v.(bool)
	err := Ask(s.Title, huh.NewConfirm().Description(s.Description).Value(&value))
	return value, err
}

// Text is a line of text: a string.
type Text struct {
	Placeholder string
	Required    bool
}

func (Text) value(raw any) any { s, _ := raw.(string); return s }
func (Text) show(v any) string { return orNone(v.(string)) }
func (t Text) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := v.(string)
	input := huh.NewInput().Description(s.Description).Placeholder(t.Placeholder).Value(&value)
	if t.Required {
		input.Validate(required)
	}
	err := Ask(s.Title, input)
	return strings.TrimSpace(value), err
}

// List is a list typed on a line, comma-separated: a []string.
type List struct{}

func (List) value(raw any) any { return stringList(raw) }
func (List) show(v any) string { return orNone(strings.Join(v.([]string), ", ")) }
func (List) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := strings.Join(v.([]string), ", ")
	err := Ask(s.Title, huh.NewInput().Description(s.Description).Value(&value))
	return split(value), err
}

// Lines is a list typed one item a line: a []string.
type Lines struct{}

func (Lines) value(raw any) any { return stringList(raw) }
func (Lines) show(v any) string { return List{}.show(v) }
func (Lines) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := strings.Join(v.([]string), "\n")
	err := Ask(s.Title, huh.NewText().Description(s.Description).Value(&value))
	return split(value), err
}

// Multi picks any of Options (and of those in the value): a []string. With All set, it's an option too,
// labeled "All (All)", that stands for every option and is picked alone.
type Multi struct {
	Options []string
	All     string
}

func (Multi) value(raw any) any { return stringList(raw) }
func (Multi) show(v any) string { return List{}.show(v) }
func (m Multi) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	value := v.([]string)
	names := slices.Clone(m.Options)
	for _, x := range value {
		if x != m.All && !slices.Contains(names, x) {
			names = append(names, x)
		}
	}
	var options []huh.Option[string]
	if m.All != "" {
		options = append(options, huh.NewOption("All ("+m.All+")", m.All))
	}
	options = append(options, huh.NewOptions(names...)...)
	err := Ask(s.Title, huh.NewMultiSelect[string]().Description(s.Description).Options(options...).Value(&value))
	if m.All != "" && slices.Contains(value, m.All) {
		value = []string{m.All}
	}
	return value, err
}

// Member is whether a list has Item: the list, a []string, shown as on or off.
type Member struct{ Item string }

func (Member) value(raw any) any   { return stringList(raw) }
func (m Member) show(v any) string { return onOff(slices.Contains(v.([]string), m.Item)) }
func (m Member) ask(s *Setting, v any, _ Values, _ bool) (any, error) {
	list := v.([]string)
	in := slices.Contains(list, m.Item)
	if err := Ask(s.Title, huh.NewConfirm().Description(s.Description).Value(&in)); err != nil {
		return list, err
	}
	list = slices.DeleteFunc(slices.Clone(list), func(x string) bool { return x == m.Item })
	if in {
		list = append(list, m.Item)
	}
	return list, nil
}

// Secret is where a secret's value comes from: a secret.Source. Suggest gives the value to start with for
// each kind (env, keychain or command). Asked as part of turning a setting on, it starts on command when
// the source is an unset environment variable and a command is suggested.
type Secret struct {
	Suggest func(v Values) map[string]string
}

func (Secret) value(raw any) any {
	m, _ := raw.(map[string]any)
	str := func(k string) string { s, _ := m[k].(string); return s }
	return secret.Source{Keychain: str("keychain"), Command: str("command"), Env: str("env")}
}

func (Secret) show(v any) string {
	kind, value := sourceKind(v.(secret.Source))
	return kind + ": " + value
}

func (sec Secret) ask(s *Setting, v any, values Values, fresh bool) (any, error) {
	var suggest map[string]string
	if sec.Suggest != nil {
		suggest = sec.Suggest(values)
	}
	kind, value := sourceKind(v.(secret.Source))
	if fresh && kind == "env" && os.Getenv(value) == "" && suggest["command"] != "" {
		kind, value = "command", suggest["command"]
	}
	was := kind
	err := Ask(s.Title, huh.NewSelect[string]().
		Description(s.Description).
		Options(
			huh.NewOption("An environment variable on the host", "env"),
			huh.NewOption(capitalize(secret.Keychain()), "keychain"),
			huh.NewOption("A command's output", "command"),
		).
		Value(&kind))
	if err != nil {
		return v, err
	}
	if kind != was {
		value = suggest[kind]
	}
	input := huh.NewInput().Value(&value).Validate(required)
	switch kind {
	case "env":
		err = Ask("Environment variable", input)
	case "keychain":
		err = Ask("Keychain item (service name)", input.Description("Add it with: "+secret.AddKeychainItem("NAME")))
	default:
		err = Ask("Command", input.Description("Run with sh -c on the host; its output is the value"))
	}
	value = strings.TrimSpace(value)
	switch kind {
	case "env":
		return secret.Source{Env: value}, err
	case "keychain":
		return secret.Source{Keychain: value}, err
	}
	return secret.Source{Command: value}, err
}

func sourceKind(s secret.Source) (kind, value string) {
	switch {
	case s.Keychain != "":
		return "keychain", s.Keychain
	case s.Command != "":
		return "command", s.Command
	}
	return "env", s.Env
}

func required(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("required")
	}
	return nil
}

// stringList takes a list of strings decoded from YAML; nil when empty.
func stringList(raw any) []string {
	var out []string
	switch l := raw.(type) {
	case []string:
		out = l
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// split splits a list typed with commas, spaces or newlines.
func split(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
}

func onOff(b bool) string { return map[bool]string{true: "on", false: "off"}[b] }

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
