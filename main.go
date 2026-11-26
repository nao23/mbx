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
		Long: `mbx runs a coding agent (or a shell) in a microsandbox microVM, with the current directory mounted
at the same path as on the host.

Config files are in ~/.config/mbx (override with the MBX_CONFIG_DIR env var): the one whose directories
has the current directory, else config.yaml. --config gives one instead.`,
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

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the config files",
		Args:  cobra.NoArgs,
	}
	var newFile bool
	edit := &cobra.Command{
		Use:   "edit",
		Short: "Make or edit a config file from a menu of its settings",
		Long: `Make or edit a config file from a menu of its settings: the current directory's config file (config.yaml,
made if missing), or with --new a new one for the current directory starting from config.yaml. The menu
lists the settings with their values; pick one to change it (Esc goes back), and Save to write the changed
ones after a confirmation, keeping the file's other keys and comments.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile != "" {
				return errors.New("config edit picks the file itself; run it without --config")
			}
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			return config.Edit(workspace, newFile)
		},
	}
	edit.Flags().BoolVar(&newFile, "new", false, "make a config file for the current directory, starting from config.yaml")
	cmd.AddCommand(edit)
	return cmd
}

type runOptions struct {
	name, agent                                     string
	keep, noDocker, ecr, github, dryRun, showConfig bool
	rest                                            []string // args after --
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
		Long: `Create a sandbox for the current directory, mounted at the same path as on the host, and start claude
or a shell in it.

The sandbox is removed on exit unless --keep is given. If a sandbox with the same name exists, run
connects to it instead, and leaves it running on exit.

ARGS after -- are passed to claude; for shell they replace the configured command (default: bash).`,
		Example: `  mbx run claude
  mbx run claude -- -p "summarize this repo"
  mbx run shell -- make test
  mbx run claude --github --dry-run
  mbx run shell --show-config`,
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
	fs.BoolVar(&o.noDocker, "no-docker", false, "don't use Docker Engine in the VM")
	fs.BoolVar(&o.ecr, "ecr", false, "enable the ECR integration in the config")
	fs.BoolVar(&o.github, "github", false, "enable the GitHub integration in the config")
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
	if o.noDocker {
		cfg.Docker.Enabled = false
	}
	if o.ecr {
		cfg.Docker.ECR.Enabled = true
	}
	if o.github {
		cfg.GitHub.Enabled = true
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
		Long: `Fetch the values of the secrets of the current directory's sandboxes (or the one named with --name) again,
from the sources in the config, and swap them in (running or not) without a restart. Each secret keeps its
hosts; to add or remove secrets, recreate the sandbox.`,
		Example: `  mbx secret rotate
  mbx secret rotate --name shell-repo`,
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
		Long: `Build an image for the VMs with the host Docker (for the host's architecture, as the VMs have it) and load it
into microsandbox. As with docker build, the Dockerfile defaults to CONTEXT/Dockerfile and CONTEXT to the current
directory.
With --default, build the default image (` + sandbox.DefaultImage + `) from mbx's built-in Dockerfile: run
builds it when it's missing or outdated, so use this to update what it has, like Claude Code (with
-- --no-cache to skip Docker's cache).

DOCKER_BUILD_ARGS after -- are passed to docker build.`,
		Example: `  mbx build -t my-sandbox:latest
  mbx build -t my-sandbox:latest -f sandbox.Dockerfile ./docker
  mbx build --default -- --no-cache`,
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
		Long: `Manage the credentials for the MCP servers claude in the VM uses through the MCP gateway
(claude.mcp_servers and claude.claude_ai_connectors). Log in to each server in claude.mcp_servers once with
login; the gateway refreshes the token after that. claude.ai connectors use the host claude's login.`,
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
			Use:   "login SERVER",
			Short: "Log in to a server in claude.mcp_servers with OAuth in the browser",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return withConfig(func(cfg *config.Config) error {
					s, ok := cfg.MCPServer(args[0])
					if !ok {
						return fmt.Errorf("%s has no url in mcp_gateway.servers or the host's ~/.claude.json (stdio servers aren't supported)", args[0])
					}
					return mcpgateway.Login(args[0], s)
				})
			},
		},
		&cobra.Command{
			Use:   "logout SERVER",
			Short: "Remove mbx's login to a server from the keychain",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return mcpgateway.Logout(args[0]) },
		},
	)
	return cmd
}

func newMCPGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp-gw",
		Short: "Manage the MCP gateway (default: status)",
		Long: `Manage the MCP gateway on the host, which forwards the MCP requests of claude in the VM with tokens that
stay on the host. It listens on a unix socket per sandbox, exposed to that sandbox only. One gateway serves
all sandboxes, and run starts it when needed; restart it after changing a connector's tool permissions on
claude.ai or updating mbx.`,
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
