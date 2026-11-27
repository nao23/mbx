package mcpgateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/msb"
)

// Tests run no external commands: the keyring is go-keyring's in-memory mock, the browser is a plain
// HTTP client, and PATH is empty so any other command fails instead of running
func TestMain(m *testing.M) {
	os.Setenv("PATH", "")
	keyring.MockInit()
	openBrowser = func(u string) error {
		resp, err := http.Get(u) // follows the authorization server's redirect to the callback
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	os.Exit(m.Run())
}

// fakeAuth is an MCP server with its OAuth authorization server, which grants every authorization request.
type fakeAuth struct {
	*httptest.Server
	dcr        bool // offer dynamic client registration
	challenge  bool // name the resource metadata in the 401's WWW-Authenticate
	clientID   string
	refreshes  int
	resources  []string          // resource parameters received
	challenges map[string]string // code -> PKCE challenge
}

func newFakeAuth(t *testing.T, dcr, challenge bool) *fakeAuth {
	f := &fakeAuth{dcr: dcr, challenge: challenge, challenges: map[string]string{}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if f.challenge {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.URL+`/meta/prm", scope="mcp:read"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	prm := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": f.URL + "/mcp", "authorization_servers": []string{f.URL + "/as"}, "scopes_supported": []string{"a", "b"}})
	}
	mux.HandleFunc("/meta/prm", prm)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", prm)
	mux.HandleFunc("/.well-known/oauth-authorization-server/as", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{"issuer": f.URL + "/as", "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token"}
		if f.dcr {
			meta["registration_endpoint"] = f.URL + "/register"
		}
		writeJSON(w, meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var reg struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		json.NewDecoder(r.Body).Decode(&reg)
		if len(reg.RedirectURIs) != 1 || !strings.HasPrefix(reg.RedirectURIs[0], "http://localhost:") {
			http.Error(w, "bad redirect_uris", http.StatusBadRequest)
			return
		}
		f.clientID = "registered-client"
		writeJSON(w, map[string]any{"client_id": f.clientID})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != f.clientID || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorization request", http.StatusBadRequest)
			return
		}
		f.resources = append(f.resources, q.Get("resource"))
		code := fmt.Sprintf("code%d", len(f.challenges))
		f.challenges[code] = q.Get("code_challenge")
		redirect, _ := url.Parse(q.Get("redirect_uri"))
		redirect.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if f.challenges[r.Form.Get("code")] != base64.RawURLEncoding.EncodeToString(sum[:]) {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			f.resources = append(f.resources, r.Form.Get("resource"))
			writeJSON(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 3600})
		case "refresh_token":
			f.refreshes++
			writeJSON(w, map[string]any{"access_token": fmt.Sprintf("access-r%d", f.refreshes), "refresh_token": "refresh-2", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	})
	return f
}

// Login discovers the authorization server from the 401, registers a client, gets tokens through the
// browser with PKCE and the resource parameter, and saves them; the gateway later refreshes them
func TestLoginWithDynamicRegistration(t *testing.T) {
	f := newFakeAuth(t, true, true)
	server := config.MCPServer{URL: f.URL + "/mcp"}
	if err := Login("fake", server); err != nil {
		t.Fatal(err)
	}
	l, _ := loadLogin("fake")
	if l == nil || l.AccessToken != "access-1" || l.RefreshToken != "refresh-1" || l.ClientID != "registered-client" ||
		l.URL != server.URL || l.TokenURL != f.URL+"/token" || l.Scope != "mcp:read" {
		t.Fatalf("saved login = %+v", l)
	}
	if want := []string{server.URL, server.URL}; strings.Join(f.resources, " ") != strings.Join(want, " ") {
		t.Errorf("resource parameters = %v, want the server URL in both requests", f.resources)
	}

	// An expired token is refreshed when used, and the rotated refresh token saved
	l.Expiry = time.Now().Add(-time.Minute)
	saveLogin("fake", l)
	tok, err := tokenCacheFor("fake").get()
	if err != nil || tok != "access-r1" {
		t.Fatalf("get after expiry = %q, %v; want the refreshed token", tok, err)
	}
	if l, _ := loadLogin("fake"); l.RefreshToken != "refresh-2" || !l.valid() {
		t.Errorf("login after refresh = %+v", l)
	}
	if tok, _ := tokenCacheFor("fake").get(); tok != "access-r1" || f.refreshes != 1 {
		t.Errorf("a valid token was refreshed again (%d refreshes)", f.refreshes)
	}

	if err := Logout("fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := tokenCacheFor("fake").get(); err == nil || !strings.Contains(err.Error(), "mbx mcp login fake") {
		t.Errorf("get after logout: err = %v, want one saying to log in", err)
	}
	if err := Logout("fake"); err == nil || !strings.Contains(err.Error(), "not logged in to fake") {
		t.Errorf("logout with no login: err = %v, want one saying there's none", err)
	}
}

// Without a WWW-Authenticate pointer the resource metadata is found at its well-known URL; a configured
// client_id is used without registration, and a configured scope wins over the server's
func TestLoginWithClientID(t *testing.T) {
	f := newFakeAuth(t, false, false)
	f.clientID = "preregistered"
	if err := Login("fake2", config.MCPServer{URL: f.URL + "/mcp", ClientID: "preregistered", Scope: "custom"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := loadLogin("fake2"); l == nil || l.ClientID != "preregistered" || l.Scope != "custom" {
		t.Errorf("saved login = %+v", l)
	}

	// Without client_id, a server without registration can't be logged in to
	err := Login("fake3", config.MCPServer{URL: f.URL + "/mcp"})
	if err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Errorf("Login without client_id or registration: err = %v, want one asking for client_id", err)
	}
}

// run refuses a server mbx isn't logged in to, or logged in to at another URL
func TestSetupRequiresLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{}
	cfg.MCPGateway.Port = 41837
	cfg.MCPGateway.Servers = map[string]config.MCPServer{"example": {URL: "https://mcp.example.com/mcp"}}
	for name, l := range map[string]*login{
		"no login":    nil,
		"another url": {URL: "https://old.example.com/mcp", AccessToken: "x"},
	} {
		keyring.MockInit()
		if l != nil {
			saveLogin("example", l)
		}
		_, err := Setup(&msb.Runner{DryRun: true}, cfg, "sb", []string{"example"})
		if err == nil || !strings.Contains(err.Error(), "mbx mcp login example") {
			t.Errorf("%s: err = %v, want one saying to log in", name, err)
		}
	}
	saveLogin("example", &login{URL: "https://mcp.example.com/mcp", AccessToken: "x"})
	if _, err := Setup(&msb.Runner{DryRun: true}, cfg, "sb", []string{"example", ClaudeAIPrefix + "Slack"}); err != nil {
		t.Errorf("Setup with a login: %v", err)
	}
}
