package mcpgateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"

	"github.com/nao23/mbx/internal/msb"
)

// The bridge script, with the port set, must parse as POSIX sh
func TestBridgeScript(t *testing.T) {
	s := BridgeScript(41837)
	if !strings.HasPrefix(s, "port=41837\n") {
		t.Errorf("BridgeScript doesn't set the port first:\n%s", s)
	}
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(s), "bridge.sh"); err != nil {
		t.Errorf("bridge.sh doesn't parse: %v", err)
	}
}

var testPolicy = toolPolicy{
	permissions:       map[string]string{"slack_create_canvas": "blocked", "slack_send_message": "ask"},
	defaultPermission: "ask",
}

// A tools/call of a blocked tool is found, in a batch too; other calls and methods pass
func TestBlockedCall(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slack_create_canvas"}}`, true},
		{`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slack_create_canvas"}}]`, true},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slack_send_message"}}`, false},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slack_read_channel"}}`, false},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false},
		{`not json`, false},
	} {
		if _, _, got := testPolicy.blockedCall([]byte(tc.body)); got != tc.want {
			t.Errorf("blockedCall(%s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// Blocked tools are removed from a tools/list result; other messages are left as they are
func TestFilterMessage(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"slack_create_canvas"},{"name":"slack_read_channel"}]}}`
	out := string(testPolicy.filterMessage([]byte(in)))
	if strings.Contains(out, "slack_create_canvas") || !strings.Contains(out, "slack_read_channel") {
		t.Errorf("filterMessage = %s", out)
	}
	other := `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`
	if got := string(testPolicy.filterMessage([]byte(other))); got != other {
		t.Errorf("non-tools message changed: %s", got)
	}
}

// Blocked tools are removed from a tools/list result sent as SSE, keeping the event lines
func TestFilterSSE(t *testing.T) {
	in := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"slack_create_canvas\"},{\"name\":\"slack_read_channel\"}]}}\n\n"
	out, _ := io.ReadAll(testPolicy.filterSSE(io.NopCloser(strings.NewReader(in))))
	if strings.Contains(string(out), "slack_create_canvas") || !strings.Contains(string(out), "slack_read_channel") ||
		!strings.HasPrefix(string(out), "event: message\n") {
		t.Errorf("filterSSE = %q", out)
	}
}

// The gateway opens a socket per registered sandbox, refuses servers not registered for it, and closes the
// socket when the registration goes
func TestGatewaySockets(t *testing.T) {
	// A short HOME, as a unix socket path is limited to 104 bytes and t.TempDir's are long on macOS
	home, err := os.MkdirTemp("/tmp", "mbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Dir(SocketPath("x")), 0o700); err != nil {
		t.Fatal(err)
	}
	reg, err := register(&msb.Runner{}, "claude-test", []string{"claudeai:Slack"})
	if err != nil {
		t.Fatal(err)
	}

	gw := &gateway{upstreams: map[string]*upstream{}, sockets: map[string]*sandboxSocket{}}
	gw.reconcile()
	fi, err := os.Stat(SocketPath(reg.ID))
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket = %v, %v; want a 0600 socket", fi, err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(reg.ID))
	}}}
	for _, path := range []string{"/other", "/", "/s/x/claudeai:Slack"} {
		resp, err := client.Post("http://gateway"+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", path, resp.StatusCode)
		}
	}

	os.Remove(SandboxFile("claude-test"))
	gw.reconcile()
	if _, err := os.Stat(SocketPath(reg.ID)); !os.IsNotExist(err) {
		t.Errorf("socket after the registration was removed: %v, want it gone", err)
	}
}
