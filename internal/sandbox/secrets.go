package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/nao23/mbx/internal/agent"
	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/feature"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/secret"
	"github.com/nao23/mbx/internal/ui"
)

// resolveSecrets fetches secret values on the host into this process's environment, which msb inherits.
// The VM gets placeholders. It fetches the secrets want picks by name (run all of them, rotation the
// sandbox's), and returns the names of those it fetched.
func resolveSecrets(r *msb.Runner, cfg *config.Config, features []feature.Feature, want func(env string) bool) ([]string, error) {
	type fetch struct {
		owner     string // the feature or agent, or secret_sources for the user's own
		env, host string // host lists the allowed hosts for the user's own
		source    secret.Source
	}
	var fetches []fetch
	secrets, _ := cfg.Microsandbox["secrets"].(map[string]any)
	for name, v := range secrets {
		if !want(name) {
			continue
		}
		if s, ok := v.(map[string]any); ok && s["value"] != nil {
			continue // value written directly in the config
		}
		src, ok := cfg.SecretSources[name]
		if !ok {
			if os.Getenv(name) == "" {
				return nil, fmt.Errorf("secret %s has no value (add secret_sources.%s)", name, name)
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
		if s := f.Secret; s != nil && want(s.Env) {
			fetches = append(fetches, fetch{f.Name, s.Env, strings.Join(s.Hosts, ", "), s.Source})
		}
	}
	if len(fetches) == 0 {
		return nil, nil
	}
	var fetched []string
	for _, f := range fetches {
		fetched = append(fetched, f.env)
	}

	r.Step(msb.OnHost, "Fetch secret values", "Kept only in this process's environment and inherited by msb; the VM gets placeholders")
	if r.DryRun {
		for _, f := range fetches {
			fmt.Printf("export %s=\"$(%s)\"\n", f.env, f.source.Describe())
		}
		return fetched, nil
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
		// Shown while fetching, so a keyring dialog or a command's login prompt shows what it's for
		var val string
		err := ui.Task(label, func(out io.Writer) (err error) {
			val, err = f.source.Fetch(out)
			return err
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "    "+st.Red(err.Error()))
			if val, err = promptSecret(f.env); err != nil {
				return nil, fmt.Errorf("secret %s: %w", f.env, err)
			}
		}
		os.Setenv(f.env, val)
	}
	return fetched, nil
}

// RotateSecrets fetches the values of the secrets of the sandbox name, or else of the workspace's sandboxes,
// again and swaps them in, running or not, without a restart. Each keeps its hosts; secrets added to or
// removed from the config need the sandbox recreated.
func RotateSecrets(cfg *config.Config, workspace, name string, dryRun bool) error {
	names := []string{name}
	if name == "" {
		list, err := msb.List(labelWorkspace + "=" + workspace)
		if err != nil {
			return err
		}
		names = nil
		for _, sb := range list {
			names = append(names, sb.Name)
		}
		if len(names) == 0 {
			return fmt.Errorf("no sandbox for %s", workspace)
		}
	}
	r := &msb.Runner{DryRun: dryRun}
	for _, name := range names {
		if err := rotateSecrets(r, cfg, name); err != nil {
			return err
		}
	}
	return nil
}

func rotateSecrets(r *msb.Runner, cfg *config.Config, name string) error {
	secrets, err := msb.Secrets(name)
	if err != nil {
		return fmt.Errorf("no sandbox named %s", name)
	}
	// The agents' and features' secrets the sandbox has, whether or not the config enables them now
	inSandbox := map[string][]string{}
	for _, sec := range secrets {
		if sec.FromEnv {
			inSandbox[sec.Env] = sec.Hosts
		}
	}
	var builtins []feature.Feature
	for _, f := range append(agent.AllSecrets(cfg), feature.AllSecrets(cfg)...) {
		if hosts, ok := inSandbox[f.Secret.Env]; ok {
			f.Secret.Hosts = hosts
			builtins = append(builtins, f)
		}
	}
	if len(inSandbox) == 0 {
		fmt.Fprintln(os.Stderr, ui.Err.Dim(name+" has no secrets to rotate"))
		return nil
	}

	fetched, err := resolveSecrets(r, cfg, builtins, func(env string) bool { _, ok := inSandbox[env]; return ok })
	if err != nil {
		return err
	}
	var args, missing []string
	for _, sec := range secrets {
		if !sec.FromEnv {
			continue
		}
		if !slices.Contains(fetched, sec.Env) && os.Getenv(sec.Env) == "" {
			missing = append(missing, sec.Env)
			continue
		}
		args = append(args, "--secret", sec.Env+"@"+strings.Join(sec.Hosts, ","))
	}
	if len(missing) > 0 {
		return fmt.Errorf("no source in the config for %s's %s", name, strings.Join(missing, ", "))
	}

	r.Step(msb.HostToVM, "Swap in the new values", "Each secret keeps its hosts; the VM keeps running")
	return r.Task(ui.Err.Bold(name), append([]string{"modify", name}, args...)...)
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
