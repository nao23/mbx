// Package sandbox creates, sets up, runs and removes msbx's sandboxes.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/nao23/msbx/internal/agent"
	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/feature"
	"github.com/nao23/msbx/internal/mcpgateway"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/ui"
)

type Sandbox struct {
	cfg       *config.Config
	ag        agent.Agent
	name      string
	workspace string
	r         *msb.Runner
}

// New checks the config. An empty name means <agent>-<dir>.
func New(cfg *config.Config, workspace, name string, dryRun bool) (*Sandbox, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	if name == "" {
		name = defaultName(cfg, workspace)
	}
	ag, _ := agent.New(cfg) // checked by validate

	return &Sandbox{cfg: cfg, ag: ag, name: name, workspace: workspace, r: &msb.Runner{DryRun: dryRun}}, nil
}

// Run creates the sandbox or reconnects to it, runs the agent with the terminal attached, and returns its
// exit code. args come after --.
func (s *Sandbox) Run(args []string) (int, error) {
	r, cfg, name := s.r, s.cfg, s.name
	if why := defaultImageOutdated(); cfg.Image() == DefaultImage && why != "" {
		r.Step(msb.OnHost, "Build the default image", why+"; build msbx's Dockerfile with the host Docker and load it into microsandbox")
		if err := rebuild(r); err != nil {
			return 0, err
		}
	}
	if _, err := resolveSecrets(r, cfg, features(cfg, s.workspace, nil), func(string) bool { return true }); err != nil {
		return 0, err
	}

	// Only the run that created the sandbox removes it, so a second session (e.g. a shell joined with
	// --name claude-<dir>) doesn't pull it out from under the first
	var gateway *mcpgateway.Registration
	if status, ok := msb.Status(name); ok {
		// The gateway may have stopped since the sandbox was created (e.g. on a reboot)
		if gateway = mcpgateway.Registered(name); gateway != nil {
			if err := mcpgateway.StartFor(r, gateway); err != nil {
				return 0, err
			}
		}
		if status != "Running" {
			r.Step(msb.HostToVM, "Start the existing sandbox", name+" is stopped; start it (creation and seeding are skipped)")
			if err := r.Task(ui.Err.Bold(name), "start", "--quiet", name); err != nil {
				return 0, err
			}
		} else {
			r.Step(
				msb.OnHost,
				"Reconnect to the existing sandbox",
				name+" is running; creation and seeding are skipped, and it's left running on exit",
			)
		}
	} else {
		if servers := s.ag.MCPServers(); len(servers) > 0 {
			var err error
			if gateway, err = mcpgateway.Setup(r, cfg, name, servers); err != nil {
				return 0, err
			}
		}
		if err := create(r, cfg, buildPlan(cfg, s.workspace, name, gateway), s.workspace, name); err != nil {
			remove(r, name)
			return 0, err
		}
		if !cfg.Keep {
			defer remove(r, name)
		}
	}

	if err := setupGuest(r, features(cfg, s.workspace, gateway), name); err != nil {
		return 0, err
	}

	err := s.start(args)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// start runs the agent with the terminal attached and waits for it to exit.
func (s *Sandbox) start(args []string) error {
	command, shown := s.ag.Command(s.r, args)
	if s.r.DryRun {
		fmt.Printf("#    (run with msb exec -t %s; attaches the terminal and waits for exit)\n", s.name)
		fmt.Println(shown)
		return nil
	}
	// msb exec forwards terminal signals into the VM; msbx just waits for it to exit, then cleans up
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	// Blank lines around the agent's own output, to set it apart from msbx's progress
	fmt.Fprintln(os.Stderr)
	err := s.r.Run(append([]string{"exec", "-t", s.name, "--"}, command...)...)
	fmt.Fprintln(os.Stderr)
	return err
}

// Labels on the sandboxes msbx creates, to tell them from others
const (
	labelMsbx      = "msbx"
	labelAgent     = "msbx.agent"
	labelWorkspace = "msbx.workspace"
)

func List() error {
	list, err := msb.List(labelMsbx + "=1")
	if err != nil {
		return err
	}
	st := ui.For(os.Stdout)
	if len(list) == 0 {
		fmt.Println(st.Dim("No sandboxes created by msbx"))
		return nil
	}
	r := &msb.Runner{}
	rows := [][]string{{"NAME", "STATUS", "AGENT", "WORKSPACE", "CREATED"}}
	for _, sb := range list {
		labels := msb.Labels(sb.Name)
		created := sb.CreatedAt
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			created = t.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{sb.Name, sb.Status, labels[labelAgent], r.Disp(labels[labelWorkspace]), created})
	}
	// Columns are padded before coloring, as color codes take no columns
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	for n, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			if i < len(row)-1 {
				cell = ui.Pad(cell, widths[i])
			}
			switch {
			case n == 0:
				cell = st.Dim(cell)
			case i == 0:
				cell = st.Bold(cell)
			case i == 1 && row[1] == "Running":
				cell = st.Green(cell)
			case i == 1:
				cell = st.Yellow(cell)
			case i == 3:
				cell = st.Cyan(cell)
			case i == 4:
				cell = st.Dim(cell)
			}
			cells[i] = cell
		}
		fmt.Println(strings.Join(cells, "   "))
	}
	return nil
}

// Names returns the names of the sandboxes msbx created, for shell completion.
func Names() []string {
	list, _ := msb.List(labelMsbx + "=1")
	var names []string
	for _, sb := range list {
		names = append(names, sb.Name)
	}
	return names
}

func Stop(names ...string) error {
	return (&msb.Runner{}).Run(append([]string{"stop"}, names...)...)
}

func Remove(names ...string) {
	r := &msb.Runner{}
	for _, name := range names {
		remove(r, name)
	}
}

func remove(r *msb.Runner, name string) {
	r.Step(msb.OnHost, "Remove the sandbox", "The Docker disk goes with it; session history shared with the host is kept")
	_ = r.Task(ui.Err.Bold(name), "rm", "-f", "--quiet", name)
	r.RemoveFile(mcpgateway.SandboxFile(name))
}

// validate checks what buildPlan relies on.
func validate(cfg *config.Config) error {
	if _, err := agent.New(cfg); err != nil {
		return err
	}
	if _, err := feature.Enabled(cfg); err != nil {
		return err
	}
	if cfg.Image() == "" {
		return errors.New("microsandbox_config.image is not set")
	}
	for _, s := range cfg.Claude.MCPServers {
		if strings.HasPrefix(s, mcpgateway.ClaudeAIPrefix) {
			return fmt.Errorf(
				"claude.mcp_servers: %s: list claude.ai connectors in claude.claude_ai_connectors (as %s)",
				s,
				strings.TrimPrefix(s, mcpgateway.ClaudeAIPrefix),
			)
		}
		if _, ok := cfg.MCPServer(s); !ok {
			return fmt.Errorf("claude.mcp_servers: %s has no url in mcp_gateway.servers or the host's ~/.claude.json (stdio servers aren't supported)", s)
		}
	}
	if _, err := microsandboxFlags(cfg); err != nil {
		return err
	}
	if scripts, ok := cfg.Microsandbox["scripts"].(map[string]any); ok {
		for k := range scripts {
			if strings.HasPrefix(k, "msbx-") {
				return fmt.Errorf("microsandbox_config.scripts: %s: the msbx- prefix is reserved for msbx", k)
			}
		}
	}
	return nil
}

var nonNameChar = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// defaultName is <agent>-<dir>, so claude and a shell in the same directory get separate sandboxes.
func defaultName(cfg *config.Config, workspace string) string {
	return cfg.Agent + "-" + nonNameChar.ReplaceAllString(filepath.Base(workspace), "-")
}
