// Package mcpgateway is the MCP gateway on the host.
// It listens on a unix socket per sandbox, which msb exposes to that sandbox only, at a vsock port,
// so the socket tells which sandbox a request comes from without any credential in the VM.
// It forwards each request with a token kept on the host:
// mbx's own OAuth login, or for claude.ai connectors (claudeai:<name>) the host claude's token.
package mcpgateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/secret"
	"github.com/nao23/mbx/internal/ui"
)

// Registration is written by mbx run and removed with the sandbox.
type Registration struct {
	ID      string   `json:"id"` // names the sandbox's socket
	Servers []string `json:"servers"`
	Session string   `json:"session"` // X-Mcp-Client-Session-Id for claude.ai connectors
}

func dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mbx", "mcp-gateway")
}

func SandboxFile(name string) string {
	return filepath.Join(dir(), "sandboxes", name+".json")
}

// SocketPath is the sandbox's socket. Kept short, as a unix socket path is limited to 104 bytes on macOS
func SocketPath(id string) string {
	return filepath.Join(dir(), "s", id+".sock")
}

// Registered returns the sandbox's registration, or nil.
func Registered(name string) *Registration {
	data, err := os.ReadFile(SandboxFile(name))
	if err != nil {
		return nil
	}
	var reg Registration
	if json.Unmarshal(data, &reg) != nil || reg.ID == "" {
		return nil
	}
	return &reg
}

// registrations returns the registered sandboxes by name.
func registrations() map[string]*Registration {
	regs := map[string]*Registration{}
	files, _ := filepath.Glob(filepath.Join(dir(), "sandboxes", "*.json"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if reg := Registered(name); reg != nil {
			regs[name] = reg
		}
	}
	return regs
}

func pid() int {
	data, err := os.ReadFile(filepath.Join(dir(), "gateway.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	return pid
}

// Start does nothing if the gateway is running.
func Start() error {
	if pid := pid(); pid != 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(dir(), "sandboxes"), 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir(), "gateway.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "mcp-gw", "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive this mbx
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(dir(), "gateway.pid"), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
		return fmt.Errorf("MCP gateway exited at startup; see %s", logPath)
	case <-time.After(300 * time.Millisecond):
	}
	ui.Done("MCP gateway started", fmt.Sprintf("pid %d", pid))
	return nil
}

// waitSocket waits for the gateway to open the sandbox's socket.
func waitSocket(id string) error {
	for i := 0; i < 50; i++ {
		if fi, err := os.Stat(SocketPath(id)); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the MCP gateway didn't open %s; see %s", SocketPath(id), filepath.Join(dir(), "gateway.log"))
}

func Stop() error {
	pid := pid()
	if pid == 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 30 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(filepath.Join(dir(), "gateway.pid"))
	ui.Done("MCP gateway stopped", fmt.Sprintf("pid %d", pid))
	return nil
}

func Status() error {
	st := ui.For(os.Stdout)
	home, _ := os.UserHomeDir()
	if pid := pid(); pid != 0 {
		log := strings.Replace(filepath.Join(dir(), "gateway.log"), home, "~", 1)
		fmt.Printf("%s %s  %s\n", st.OK(), st.Bold("MCP gateway running"), st.Dim(fmt.Sprintf("pid %d · log %s", pid, log)))
	} else {
		fmt.Printf("%s %s\n", st.Dim("○"), st.Bold("MCP gateway not running"))
	}

	regs := registrations()
	if len(regs) == 0 {
		return nil
	}
	names := make([]string, 0, len(regs))
	width := 0
	for name := range regs {
		names = append(names, name)
		width = max(width, len(name))
	}
	slices.Sort(names)
	fmt.Println("\n" + st.Bold("Sandboxes"))
	for _, name := range names {
		reg := regs[name]
		mark := st.OK()
		if fi, err := os.Stat(SocketPath(reg.ID)); err != nil || fi.Mode()&os.ModeSocket == 0 {
			mark = st.Dim("○") // the gateway isn't serving it
		}
		sock := strings.Replace(SocketPath(reg.ID), home, "~", 1)
		fmt.Printf("  %s %s  %s\n", mark, ui.Pad(name, width), st.Dim(strings.Join(reg.Servers, ", ")+" · "+sock))
	}
	return nil
}

// LoginStatus doesn't refresh tokens.
func LoginStatus(cfg *config.Config) error {
	st := ui.For(os.Stdout)
	const layout = "2006-01-02 15:04"
	item := func(ok bool, name, note string, details ...[2]string) {
		mark, text := st.OK(), st.Dim(note)
		if !ok {
			mark, text = st.Failed(), st.Red(note)
		}
		fmt.Printf("  %s %s  %s\n", mark, st.Bold(name), text)
		for _, d := range details {
			fmt.Printf("      %s  %s\n", st.Dim(ui.Pad(d[0], 11)), d[1])
		}
	}
	if len(cfg.Claude.MCPServers) == 0 && len(cfg.Claude.ClaudeAIConnectors) == 0 {
		fmt.Println(st.Dim("No MCP servers in claude.mcp_servers or claude.claude_ai_connectors"))
		return nil
	}

	if len(cfg.Claude.MCPServers) > 0 {
		fmt.Println(st.Bold("MCP servers") + "  " + st.Dim("claude.mcp_servers · mbx's own logins"))
	}
	for _, name := range cfg.Claude.MCPServers {
		def, _ := cfg.MCPServer(name)
		from := "the host's ~/.claude.json"
		if cfg.MCPGateway.Servers[name].URL != "" {
			from = "mcp_gateway.servers"
		}
		where := fmt.Sprintf("%s, service %s, account %s", secret.Keyring(), keyringService, name)
		l, err := loadLogin(name)
		var ok bool
		var note string
		switch {
		case err != nil:
			note = err.Error()
		case l == nil:
			note, where = "not logged in · run mbx mcp login "+name, where+" (none yet)"
		case l.URL != def.URL:
			note = "logged in at another url (" + l.URL + ") · run mbx mcp login " + name
		case l.valid() && l.Expiry.IsZero():
			ok, note = true, "logged in, no expiry given"
		case l.valid() && l.RefreshToken != "":
			ok, note = true, "logged in, valid until "+l.Expiry.Local().Format(layout)+" · the gateway refreshes it"
		case l.valid():
			ok, note = true, "logged in, valid until "+l.Expiry.Local().Format(layout)+" · no refresh token: log in again after that"
		case l.RefreshToken != "":
			ok, note = true, "logged in, expired · the gateway refreshes it on the next request"
		default:
			note = "the login expired without a refresh token · run mbx mcp login " + name
		}
		item(ok, name, note, [2]string{"url", def.URL + st.Dim("  from "+from)}, [2]string{"credentials", where})
	}

	if len(cfg.Claude.ClaudeAIConnectors) > 0 {
		if len(cfg.Claude.MCPServers) > 0 {
			fmt.Println()
		}
		fmt.Println(st.Bold("claude.ai connectors") + "  " + st.Dim("claude.claude_ai_connectors · the host claude's token"))
		names := strings.Join(cfg.Claude.ClaudeAIConnectors, ", ")
		where := [2]string{"credentials", hostClaudeCredentialsPlace() + " (claudeAiOauth), the host claude's login"}
		if _, expires, err := hostClaudeAIToken(); err != nil {
			item(false, names, err.Error(), where)
		} else {
			item(true, names, "valid until "+expires.Local().Format(layout)+" · refreshed by using the host's claude", where)
		}
	}
	return nil
}

// LoggedIn reports whether mbx has a usable login to the server at url: not one at another url, or one
// expired that can't be refreshed.
func LoggedIn(server, url string) (bool, error) {
	l, err := loadLogin(server)
	if err != nil || l == nil {
		return false, err
	}
	return l.URL == url && (l.valid() || l.RefreshToken != ""), nil
}

// Setup registers the sandbox and starts the gateway if needed, which then opens the sandbox's socket.
func Setup(r *msb.Runner, cfg *config.Config, name string, servers []string) (*Registration, error) {
	// Without a login, the server just fails to connect in the VM. The gateway reads the login on each
	// connection until it has one, so logging in later and reconnecting (/mcp in claude) works
	for _, s := range servers {
		if strings.HasPrefix(s, ClaudeAIPrefix) {
			continue
		}
		def, _ := cfg.MCPServer(s)
		ok, err := LoggedIn(s, def.URL)
		if err != nil {
			return nil, err
		}
		if !ok {
			ui.Warn(fmt.Sprintf("not logged in to %s (mbx mcp login %s)", s, s))
		}
	}
	r.Step(
		msb.OnHost,
		"Register the sandbox with the MCP gateway",
		"The gateway opens a unix socket for this sandbox only, which may use "+strings.Join(servers, ", "),
	)
	reg, err := register(r, name, servers)
	if err != nil {
		return nil, err
	}
	if err := StartFor(r, reg); err != nil {
		return nil, err
	}
	return reg, nil
}

// StartFor starts the gateway if it isn't running, and waits for it to open the sandbox's socket.
func StartFor(r *msb.Runner, reg *Registration) error {
	r.Step(
		msb.OnHost,
		"Start the MCP gateway if it isn't running",
		"Forwards the VM's MCP requests with tokens kept on the host; one gateway serves all sandboxes",
	)
	return r.Do(func() error {
		if err := Start(); err != nil {
			return err
		}
		return waitSocket(reg.ID)
	}, "mbx", "mcp-gw", "start")
}

// register uses a placeholder ID in dry-run mode.
func register(r *msb.Runner, name string, servers []string) (*Registration, error) {
	reg := &Registration{ID: randomHex(8), Servers: servers, Session: newUUID()}
	if r.DryRun {
		reg = &Registration{ID: "<id>", Servers: servers}
	} else if p := SocketPath(reg.ID); len(p) > 103 {
		return nil, fmt.Errorf("the MCP gateway's socket path %s is too long for a unix socket", p)
	}
	data, _ := json.Marshal(reg)
	return reg, r.WriteFile(SandboxFile(name), data, "socket ID, servers, session ID")
}

// VMServerName gives claude.ai connectors the host claude's names (claude_ai_<name>),
// so tool names and permission rules match the host's.
func VMServerName(server string) string {
	if display, ok := strings.CutPrefix(server, ClaudeAIPrefix); ok {
		return "claude_ai_" + strings.ReplaceAll(display, " ", "_")
	}
	return server
}

// URL is the server's URL in the VM, served by the bridge to the sandbox's socket (see BridgeScript).
func URL(port int, server string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s", port, url.PathEscape(server))
}

// BridgeScript starts socat in the VM, relaying 127.0.0.1:<port> to the vsock port
// that msb exposes the sandbox's socket on.
func BridgeScript(port int) string {
	// /run outlives a stop and pids are reused after the restart, so the pid's process must be socat
	return fmt.Sprintf(`pid=/run/mbx-mcp-bridge.pid
[ -f "$pid" ] && [ "$(cat "/proc/$(cat "$pid")/comm" 2>/dev/null)" = socat ] && exit 0
command -v socat >/dev/null || { echo "warning: socat is missing; MCP servers are unavailable" >&2; exit 0; }
nohup socat TCP-LISTEN:%[1]d,bind=127.0.0.1,reuseaddr,fork VSOCK-CONNECT:2:%[1]d >/var/log/mbx-mcp-bridge.log 2>&1 &
echo $! >"$pid"`, port)
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80 // version 4, RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
