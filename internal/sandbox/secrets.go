package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/feature"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/ui"
)

// resolveSecrets fetches secret values on the host into this process's environment, which msb inherits.
// The VM gets placeholders.
func resolveSecrets(r *msb.Runner, cfg *config.Config, features []feature.Feature) error {
	type fetch struct {
		owner     string // the feature or agent, or secret_sources for the user's own
		env, host string // host lists the allowed hosts for the user's own
		source    secret.Source
	}
	var fetches []fetch
	secrets, _ := cfg.Microsandbox["secrets"].(map[string]any)
	for name, v := range secrets {
		if s, ok := v.(map[string]any); ok && s["value"] != nil {
			continue // value written directly in the config
		}
		src, ok := cfg.SecretSources[name]
		if !ok {
			if os.Getenv(name) == "" {
				return fmt.Errorf("secret %s has no value (add secret_sources.%s)", name, name)
			}
			continue
		}
		var hosts []string
		if s, ok := v.(map[string]any); ok {
			allow, _ := s["allow"].([]any)
			for _, h := range allow {
				hosts = append(hosts, fmt.Sprint(h))
			}
		}
		fetches = append(fetches, fetch{"secret_sources", name, strings.Join(hosts, ", "), src})
	}
	for _, f := range features {
		if s := f.Secret; s != nil {
			fetches = append(fetches, fetch{f.Name, s.Env, s.Host, s.Source})
		}
	}
	if len(fetches) == 0 {
		return nil
	}

	r.Step(msb.OnHost, "Fetch secret values", "Kept only in this process's environment and inherited by msb; the VM gets placeholders")
	if r.DryRun {
		for _, f := range fetches {
			fmt.Printf("export %s=\"$(%s)\"\n", f.env, f.source.Describe())
		}
		return nil
	}
	st := ui.Err
	fmt.Fprintln(os.Stderr, "  "+st.Dim("Fetched on the host; the VM gets only placeholders"))
	ownerWidth, envWidth := 0, 0
	for _, f := range fetches {
		ownerWidth, envWidth = max(ownerWidth, len(f.owner)), max(envWidth, len(f.env))
	}
	for _, f := range fetches {
		label := st.Blue(ui.Pad(f.owner, ownerWidth)) + "  " + st.Bold(ui.Pad(f.env, envWidth)) + "  " + st.Dim(f.source.Label())
		if f.host != "" {
			label += "  " + st.Cyan("→ "+f.host)
		}
		// Shown while fetching, so a keychain dialog or a command's login prompt shows what it's for
		var val string
		err := ui.Task(label, func(out io.Writer) (err error) {
			val, err = f.source.Fetch(out)
			return err
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "    "+st.Red(err.Error()))
			if val, err = promptSecret(f.env); err != nil {
				return fmt.Errorf("secret %s: %w", f.env, err)
			}
		}
		os.Setenv(f.env, val)
	}
	return nil
}

// promptSecret asks for the value on the terminal after fetching failed. The input isn't echoed or stored.
func promptSecret(env string) (string, error) {
	st := ui.Err
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("couldn't fetch it (see above)")
	}
	fmt.Fprintf(os.Stderr, "    %s Enter %s %s ", st.Yellow("?"), st.Bold(env), st.Dim("(hidden, this run only; empty to abort):"))
	in, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	val := strings.TrimSpace(string(in))
	if val == "" {
		return "", errors.New("couldn't fetch it, and none was entered")
	}
	fmt.Fprintf(os.Stderr, "    %s %s\n", st.OK(), st.Dim("using the value entered"))
	return val, nil
}
