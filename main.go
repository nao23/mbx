// msbx runs a coding agent (or a shell) in a microsandbox microVM.
package main

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/mcpgateway"
	"github.com/nao23/msbx/internal/sandbox"
	"github.com/nao23/msbx/internal/ui"
)

// exitCode carries the agent's exit code out of a command, so msbx exits with it.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "msbx",
		Short: "Run a coding agent (or a shell) in a microsandbox microVM",
		Long: `msbx runs a coding agent (or a shell) in a microsandbox microVM, with the current directory mounted
at the same path as on the host.

Config file: ~/.config/msbx/config.yaml (override with the MSBX_CONFIG env var)`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w (see %s --help)", err, cmd.CommandPath())
	})
	root.AddCommand(
		newRunCmd(),
		newLsCmd(),
		newStopCmd(),
		newRmCmd(),
		newRebuildCmd(),
		newConfigCmd(),
		newMCPCmd(),
		newMCPGatewayCmd(),
	)
	return root
}

// runOptions are shared by run and config, which shows what run would do.
type runOptions struct {
	name, agent                         string
	keep, noDocker, ecr, github, dryRun bool
	rest                                []string // args after -- for run, DIR for config
}

func (o *runOptions) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.name, "name", "", "sandbox name (default: <agent>-<dir>)")
	fs.BoolVar(&o.keep, "keep", false, "keep the sandbox on exit")
	fs.BoolVar(&o.noDocker, "no-docker", false, "don't use Docker Engine in the VM")
	fs.BoolVar(&o.ecr, "ecr", false, "enable the ECR integration in the config")
	fs.BoolVar(&o.github, "github", false, "enable the GitHub integration in the config")
}

// setArgs takes the agent (claude or shell) from the first argument, unless it comes after --.
func (o *runOptions) setArgs(cmd *cobra.Command, args []string) {
	if len(args) > 0 && cmd.ArgsLenAtDash() != 0 && (args[0] == "claude" || args[0] == "shell") {
		o.agent, args = args[0], args[1:]
	}
	o.rest = args
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
		Use:   "run [claude|shell] [flags] [-- ARGS...]",
		Short: "Start claude or a shell in a sandbox for the current directory",
		Long: `Create a sandbox for the current directory, mounted at the same path as on the host, and start claude
or a shell in it (default: agent in the config, i.e. claude).

The sandbox is removed on exit unless --keep is given. If a sandbox with the same name exists, run
connects to it instead, and leaves it running on exit.

ARGS after -- are passed to claude; for shell they replace the configured command (default: bash).`,
		Example: `  msbx run
  msbx run claude -- -p "summarize this repo"
  msbx run shell -- make test
  msbx run --github --dry-run`,
		ValidArgsFunction: completeAgent,
		// Only the agent may come before --, so a stray word isn't passed to claude as a prompt
		Args: func(cmd *cobra.Command, args []string) error {
			before := args
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				before = args[:dash]
			}
			switch {
			case len(before) == 0, len(before) == 1 && (before[0] == "claude" || before[0] == "shell"):
				return nil
			case len(before) == 1:
				return fmt.Errorf("unknown agent %q (claude or shell); arguments for it go after --", before[0])
			default:
				return fmt.Errorf("unexpected arguments %q; arguments for the agent go after --", before[1:])
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.setArgs(cmd, args)
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			_, sb, err := setup(o, workspace)
			if err != nil {
				return err
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
	o.addFlags(cmd.Flags())
	cmd.Flags().BoolVar(
		&o.dryRun,
		"dry-run",
		false,
		"print the commands it would run, in order, without running anything (no secrets are fetched)",
	)
	return cmd
}

func newConfigCmd() *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:   "config [claude|shell] [flags] [DIR]",
		Short: "Show the merged config and what run would pass to msb create",
		Long: `Show the merged config, with the options applied, and what run would pass to msb create when run in
DIR (default: the current directory).`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return []string{"claude", "shell"}, cobra.ShellCompDirectiveFilterDirs
			}
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.setArgs(cmd, args)
			if len(o.rest) > 1 {
				return fmt.Errorf("too many arguments: %v (see %s --help)", o.rest, cmd.CommandPath())
			}
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(o.rest) > 0 {
				workspace = config.ExpandHome(o.rest[0])
			}
			cfg, sb, err := setup(o, workspace)
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Printf("# Config file: %s\n# Workspace: %s\n", config.Path(), workspace)
			fmt.Printf("\n# --- Merged config (with the command-line options)\n%s", out)

			return sb.PrintPlan()
		},
	}
	o.addFlags(cmd.Flags())
	return cmd
}

// setup applies the command-line options over the config.
func setup(o runOptions, workspace string) (*config.Config, *sandbox.Sandbox, error) {
	cfg, err := config.Load(workspace)
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
		Use:   "ls",
		Short: "List sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.List()
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop NAME...",
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
		Short:             "Remove sandboxes (session history shared with the host is kept)",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSandboxes,
		RunE: func(cmd *cobra.Command, args []string) error {
			sandbox.Remove(args...)
			return nil
		},
	}
}

func newRebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the default image (" + sandbox.DefaultImage + ")",
		Long: `Rebuild the default image (` + sandbox.DefaultImage + `): build msbx's built-in Dockerfile with the host
Docker, and load it into microsandbox.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.Rebuild()
		},
	}
}

// withConfig runs fn with the config for the current directory.
func withConfig(fn func(cfg *config.Config) error) error {
	wd, _ := os.Getwd()
	cfg, err := config.Load(wd)
	if err != nil {
		return err
	}
	return fn(cfg)
}

func newMCPCmd() *cobra.Command {
	status := func(*cobra.Command, []string) error { return withConfig(mcpgateway.LoginStatus) }
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage msbx's logins to MCP servers (default: status)",
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
			Short: "Remove msbx's login to a server from the keychain",
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
claude.ai or updating msbx.`,
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
