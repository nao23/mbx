// mbx runs a coding agent (or a shell) in a microsandbox microVM.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/mcpgateway"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/sandbox"
	"github.com/nao23/mbx/internal/ui"
)

// exitCode carries the agent's exit code out of a command, so mbx exits with it.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mbx",
		Short: "Run a coding agent (or a shell) in a microsandbox microVM",
		Long: dedent(`
			mbx runs a coding agent (or a shell) in a microsandbox microVM, with the current directory mounted
			at the same path as on the host.

			Config files are in ~/.config/mbx (MBX_CONFIG_DIR overrides it). mbx uses the one whose directories
			lists the current directory or its deepest parent; with none, it uses the defaults alone. --config
			gives one instead.
		`),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Check that the host commands the command runs are there, before it does anything
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if names := cmd.Annotations[requires]; names != "" {
				return msb.Require(strings.Split(names, ",")...)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&configFile, "config", "", "the config file to use, instead of the one for the current directory")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w (see %s --help)", err, cmd.CommandPath())
	})
	root.AddCommand(
		newConfigCmd(),
		newRunCmd(),
		newLsCmd(),
		newStopCmd(),
		newRmCmd(),
		newSecretCmd(),
		newBuildCmd(),
		newMCPCmd(),
		newMCPGatewayCmd(),
	)
	return root
}

// configFile is --config: the config file to use instead of choosing one by directory
var configFile string

// requires is the annotation listing the host commands a command runs (comma-separated), checked before
// it runs. run needs docker only to build the default image, so it checks that itself.
const requires = "requires"

var (
	needsMsb       = map[string]string{requires: "msb"}
	needsMsbDocker = map[string]string{requires: "msb,docker"}
)

// dedent lets help text be indented with the code: it drops the blank lines around s,
// and the indent of its first line from every line.
func dedent(s string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	indent := lines[0][:len(lines[0])-len(strings.TrimLeft(lines[0], "\t"))]
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, indent)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \t\n")
}

// examples formats commands for a command's Example, indented as cobra's help expects.
func examples(cmds ...string) string {
	return "  " + strings.Join(cmds, "\n  ")
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the config files",
		Args:  cobra.NoArgs,
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the config files with their directories, marking the one the current directory uses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			return config.List(workspace)
		},
	}
	var newFile bool
	edit := &cobra.Command{
		Use:   "edit [--new] [NAME]",
		Short: "Open a config file in the editor, and check it",
		Long: dedent(`
			Open a config file in the editor ($VISUAL, $EDITOR, else vi), and check it when the editor exits.

			Which file:
			  (none)    the one the current directory uses
			  NAME      NAME.yaml in the config dir (.yaml may be left out)
			  --config  the given file

			With --new, create NAME (default: the directory's name) for the current directory alone first,
			copying the settings of the file it uses now.
		`),
		Example: examples(
			"mbx config edit",
			"mbx config edit work",
			"mbx config edit --new",
		),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			if (name != "" || newFile) && configFile != "" {
				return errors.New("--config can't go with NAME or --new")
			}
			return config.Edit(workspace, name, configFile, newFile)
		},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			files, _ := config.Files()
			var names []string
			for _, f := range files {
				names = append(names, strings.TrimSuffix(filepath.Base(f.Path), ".yaml"))
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
	}
	defaults := &cobra.Command{
		Use:   "defaults",
		Short: "Show the built-in defaults: every setting, with a description",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := os.Stdout.Write(config.Defaults())
			return err
		},
	}
	edit.Flags().BoolVar(&newFile, "new", false, "make a config file for the current directory first")
	cmd.AddCommand(ls, edit, defaults)
	return cmd
}

type runOptions struct {
	name, agent              string
	keep, dryRun, showConfig bool
	rest                     []string // args after --
}

// setArgs takes the agent (claude or shell) from the first argument, and ARGS from those after --.
func (o *runOptions) setArgs(args []string) {
	o.agent, o.rest = args[0], args[1:]
}

// completeAgent completes claude or shell as the first argument.
func completeAgent(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return []string{"claude", "shell"}, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveDefault
}

func newRunCmd() *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:         "run claude|shell [flags] [-- ARGS...]",
		Annotations: needsMsb,
		Short:       "Start claude or a shell in a sandbox for the current directory",
		Long: dedent(`
			Create a sandbox for the current directory, mounted at the same path as on the host, and start
			claude or a shell in it.

			The sandbox is removed on exit unless --keep is given. If a sandbox with the same name exists, run
			connects to it instead and leaves it running on exit.

			ARGS after -- go to claude. For shell, they replace the configured command (default: bash).
		`),
		Example: examples(
			"mbx run claude",
			`mbx run claude -- -p "summarize this repo"`,
			"mbx run shell -- make test",
			"mbx run claude --dry-run",
			"mbx run shell --show-config",
		),
		ValidArgsFunction: completeAgent,
		// Only the agent comes before --, so a stray word isn't passed to claude as a prompt
		Args: func(cmd *cobra.Command, args []string) error {
			before := args
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				before = args[:dash]
			}
			switch {
			case len(before) == 0:
				return errors.New("which agent to start: mbx run claude or mbx run shell")
			case before[0] != "claude" && before[0] != "shell":
				return fmt.Errorf("unknown agent %q (claude or shell); arguments for it go after --", before[0])
			case len(before) > 1:
				return fmt.Errorf("unexpected arguments %q; arguments for the agent go after --", before[1:])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.setArgs(args)
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, sb, err := setup(o, workspace)
			if err != nil {
				return err
			}
			if o.showConfig {
				out, err := yaml.Marshal(cfg)
				if err != nil {
					return err
				}
				path, why := cfg.File()
				if path == "" {
					path = "none"
				}
				fmt.Printf("# Config file: %s\n#   (%s)\n\n%s", path, why, out)
				return nil
			}
			code, err := sb.Run(o.rest)
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCode(code)
			}

			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.name, "name", "", "sandbox name (default: <agent>-<dir>)")
	fs.BoolVar(&o.keep, "keep", false, "keep the sandbox on exit")
	fs.BoolVar(
		&o.dryRun,
		"dry-run",
		false,
		"print the commands it would run, in order, without running anything (no secrets are fetched)",
	)
	fs.BoolVar(&o.showConfig, "show-config", false, "print the merged config, with the options applied, and exit")
	cmd.MarkFlagsMutuallyExclusive("dry-run", "show-config")
	return cmd
}

// setup applies the command-line options over the config.
func setup(o runOptions, workspace string) (*config.Config, *sandbox.Sandbox, error) {
	cfg, err := config.Load(workspace, configFile)
	if err != nil {
		return nil, nil, err
	}
	if o.agent != "" {
		cfg.Agent = o.agent
	}
	if cfg.Agent == "shell" && len(cfg.Command) == 0 && len(o.rest) == 0 {
		return nil, nil, errors.New("shell requires command in the config (or a command after --)")
	}
	if o.keep {
		cfg.Keep = true
	}

	sb, err := sandbox.New(cfg, workspace, o.name, o.dryRun)
	if err != nil {
		return nil, nil, err
	}

	return cfg, sb, nil
}

// completeSandboxes completes the names of sandboxes not given yet.
func completeSandboxes(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	var names []string
	for _, name := range sandbox.Names() {
		if !slices.Contains(args, name) {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "ls",
		Annotations: needsMsb,
		Short:       "List sandboxes",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.List()
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop NAME...",
		Annotations:       needsMsb,
		Short:             "Stop sandboxes (for those kept with --keep)",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSandboxes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.Stop(args...)
		},
	}
}

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "rm NAME...",
		Annotations:       needsMsb,
		Short:             "Remove sandboxes (session history shared with the host is kept)",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSandboxes,
		RunE: func(cmd *cobra.Command, args []string) error {
			sandbox.Remove(args...)
			return nil
		},
	}
}

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage a sandbox's secrets",
		Args:  cobra.NoArgs,
	}
	var name string
	var dryRun bool
	rotate := &cobra.Command{
		Use:         "rotate [flags]",
		Annotations: needsMsb,
		Short:       "Fetch the sandboxes' secrets again and swap in the new values",
		Long: dedent(`
			Fetch the secrets of the current directory's sandboxes (or the one given with --name) again from
			the sources in the config, and swap in the new values without a restart. The sandbox may be
			running or stopped.

			Each secret keeps its hosts. To add or remove secrets, recreate the sandbox.
		`),
		Example: examples(
			"mbx secret rotate",
			"mbx secret rotate --name shell-repo",
		),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.Load(workspace, configFile)
			if err != nil {
				return err
			}
			return sandbox.RotateSecrets(cfg, workspace, name, dryRun)
		},
	}
	fs := rotate.Flags()
	fs.StringVar(&name, "name", "", "sandbox name (default: the current directory's sandboxes)")
	fs.BoolVar(&dryRun, "dry-run", false, "print the commands it would run, without running anything (no secrets are fetched)")
	cmd.AddCommand(rotate)
	return cmd
}

func newBuildCmd() *cobra.Command {
	var b sandbox.Build
	var builtIn, dryRun bool
	cmd := &cobra.Command{
		Use:         "build (-t IMAGE [-f DOCKERFILE] [CONTEXT] | --default) [flags] [-- DOCKER_BUILD_ARGS...]",
		Annotations: needsMsbDocker,
		Short:       "Build an image for the VMs and load it into microsandbox",
		Long: dedent(`
			Build an image for the VMs with the host Docker and load it into microsandbox. It's built for the
			host's architecture, which the VMs share.

			As with docker build, the Dockerfile defaults to CONTEXT/Dockerfile, and CONTEXT to the current
			directory. DOCKER_BUILD_ARGS after -- go to docker build.

			With --default, build the default image (` + sandbox.DefaultImage + `) from mbx's built-in Dockerfile.
			run builds it when it's missing or outdated; build it yourself to update what it has, like Claude
			Code (add -- --no-cache to skip Docker's cache).
		`),
		Example: examples(
			"mbx build -t my-sandbox:latest",
			"mbx build -t my-sandbox:latest -f sandbox.Dockerfile ./docker",
			"mbx build --default -- --no-cache",
		),
		Args: func(cmd *cobra.Command, args []string) error {
			before := args
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				before = args[:dash]
			}
			switch {
			case builtIn && (b.Image != "" || b.Dockerfile != "" || len(before) > 0):
				return errors.New("--default builds mbx's built-in Dockerfile; give no -t, -f or CONTEXT with it")
			case !builtIn && b.Image == "":
				return errors.New("give the image's name with -t (or --default for the default image)")
			case len(before) > 1:
				return fmt.Errorf("unexpected arguments %q; arguments for docker build go after --", before[1:])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			b.Context = "."
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				args, b.DockerArgs = args[:dash], args[dash:]
			}
			if len(args) == 1 {
				b.Context = args[0]
			}
			if !builtIn && b.Dockerfile == "" {
				b.Dockerfile = filepath.Join(b.Context, "Dockerfile")
			}
			return sandbox.BuildImage(b, dryRun)
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&b.Image, "tag", "t", "", "the image's name (e.g. my-sandbox:latest)")
	fs.StringVarP(&b.Dockerfile, "file", "f", "", "the Dockerfile (default: CONTEXT/Dockerfile)")
	fs.BoolVar(&builtIn, "default", false, "build the default image ("+sandbox.DefaultImage+") from mbx's built-in Dockerfile")
	fs.BoolVar(&dryRun, "dry-run", false, "print the commands it would run without running them")
	return cmd
}

// withConfig runs fn with the config for the current directory.
func withConfig(fn func(cfg *config.Config) error) error {
	wd, _ := os.Getwd()
	cfg, err := config.Load(wd, configFile)
	if err != nil {
		return err
	}
	return fn(cfg)
}

func newMCPCmd() *cobra.Command {
	status := func(*cobra.Command, []string) error { return withConfig(mcpgateway.LoginStatus) }
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage mbx's logins to MCP servers (default: status)",
		Long: dedent(`
			Manage the logins for the MCP servers that claude in the VM uses through the MCP gateway.

			  claude.mcp_servers           log in to each once with login; the gateway refreshes the token
			  claude.claude_ai_connectors  use the host claude's login
		`),
		Args: cobra.NoArgs,
		RunE: status,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show the login state of the servers in the config, and where their credentials are kept",
			Args:  cobra.NoArgs,
			RunE:  status,
		},
		&cobra.Command{
			Use:   "login [SERVER]",
			Short: "Log in to a server in claude.mcp_servers with OAuth in the browser (default: those not logged in to)",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return withConfig(func(cfg *config.Config) error {
					if len(args) == 0 {
						return mcpgateway.LoginAll(cfg)
					}
					s, ok := cfg.MCPServer(args[0])
					if !ok {
						return fmt.Errorf("%s has no url in mcp_gateway.servers or the host's ~/.claude.json (stdio servers aren't supported)", args[0])
					}
					return mcpgateway.Login(args[0], s)
				})
			},
		},
		&cobra.Command{
			Use:   "logout [SERVER]",
			Short: "Log out of a server (remove mbx's login to it; default: the servers in claude.mcp_servers)",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					return withConfig(mcpgateway.LogoutAll)
				}
				return mcpgateway.Logout(args[0])
			},
		},
	)
	return cmd
}

func newMCPGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp-gw",
		Short: "Manage the MCP gateway (default: status)",
		Long: dedent(`
			Manage the MCP gateway on the host. It forwards the MCP requests of claude in the VM with tokens
			that stay on the host.

			The gateway listens on a unix socket per sandbox, exposed to that sandbox only. One gateway serves
			all sandboxes, and run starts it when needed.

			Restart it after changing a connector's tool permissions on claude.ai, or after updating mbx.
		`),
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return mcpgateway.Status() },
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start the gateway in the background (nothing happens if it's running)",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return mcpgateway.Start() },
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the gateway",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return mcpgateway.Stop() },
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Restart the gateway",
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if err := mcpgateway.Stop(); err != nil {
					return err
				}
				return mcpgateway.Start()
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the gateway runs, and the registered sandboxes with their sockets",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return mcpgateway.Status() },
		},
		&cobra.Command{
			Use:    "serve",
			Short:  "Run the gateway in the foreground (what start runs in the background)",
			Hidden: true,
			Args:   cobra.NoArgs,
			RunE:   func(*cobra.Command, []string) error { return mcpgateway.Serve() },
		},
	)
	return cmd
}

func main() {
	err := newRootCmd().Execute()

	var code exitCode
	switch {
	case errors.As(err, &code):
		os.Exit(int(code))
	case err != nil:
		ui.Error(err)
		os.Exit(1)
	}
}
