package mcpgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/ui"
)

// Login logs in as MCP clients do: discovers the authorization server, registers a client unless
// client_id is set, has the user authorize in the browser, and saves the tokens in the keychain.
func Login(name string, s config.MCPServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ui.Step("Log in to "+name, s.URL)

	as, scope, err := discover(ctx, s.URL)
	if err != nil {
		return err
	}
	if s.Scope != "" {
		scope = s.Scope
	}
	ui.Done("authorization server "+as.Issuer, scope)

	// localhost may resolve to either loopback address
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.CallbackPort))
	if err != nil {
		return fmt.Errorf("listen for the OAuth callback: %w (callback_port must be free during the login)", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	listeners := []net.Listener{ln}
	if ln6, err := net.Listen("tcp", fmt.Sprintf("[::1]:%d", port)); err == nil {
		listeners = append(listeners, ln6)
	}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	redirect := fmt.Sprintf("http://localhost:%d/callback", port)

	clientID, clientSecret := s.ClientID, ""
	if s.ClientSecretSource != (secret.Source{}) {
		if clientSecret, err = s.ClientSecretSource.Value(); err != nil {
			return err
		}
	}
	if clientID == "" {
		if as.RegistrationEndpoint == "" {
			return fmt.Errorf("%s has no dynamic client registration; set mcp_gateway.servers.%s.client_id", as.Issuer, name)
		}
		if clientID, clientSecret, err = registerClient(ctx, as.RegistrationEndpoint, redirect, scope); err != nil {
			return err
		}
		ui.Done("registered client "+clientID, "dynamic client registration")
	}

	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: as.AuthorizationEndpoint, TokenURL: as.TokenEndpoint},
		RedirectURL:  redirect,
		Scopes:       strings.Fields(scope),
	}
	verifier, state := oauth2.GenerateVerifier(), randomHex(32)
	resource := oauth2.SetAuthURLParam("resource", s.URL) // RFC 8707, as the MCP spec requires
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), resource)

	codes := make(chan string, 1)
	errs := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		switch {
		case req.URL.Path != "/callback":
			http.NotFound(w, req)
			return
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			errs <- fmt.Errorf("authorization failed: %s %s", q.Get("error"), q.Get("error_description"))
			fmt.Fprintln(w, "msbx: authorization failed; see the terminal.")
			return
		}
		codes <- q.Get("code")
		fmt.Fprintf(w, "msbx: logged in to %s. You can close this tab.\n", name)
	})}
	for _, l := range listeners {
		go srv.Serve(l)
	}
	defer srv.Close()

	fmt.Printf("  Opening the browser to authorize. If it doesn't open, visit:\n  %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		ui.Warn("couldn't open the browser: " + err.Error())
	}
	var code string
	select {
	case code = <-codes:
	case err := <-errs:
		return err
	case <-ctx.Done():
		return errors.New("timed out waiting for the authorization in the browser")
	}

	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource)
	if err != nil {
		return fmt.Errorf("exchange the authorization code: %w", err)
	}
	l := &login{
		URL:          s.URL,
		TokenURL:     as.TokenEndpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scope:        scope,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
	}
	if err := saveLogin(name, l); err != nil {
		return err
	}
	note := "no refresh token: log in again when it expires"
	if l.RefreshToken != "" {
		note = "the gateway refreshes it"
	}
	ui.Done("logged in to "+name, note)
	return nil
}

func Logout(name string) error {
	if err := removeLogin(name); err != nil {
		return err
	}
	ui.Done("logged out of "+name, "")
	return nil
}

// A variable so tests can follow the URL instead
var openBrowser = func(u string) error { return exec.Command("open", u).Run() }

// authServer is the authorization server metadata (RFC 8414).
type authServer struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// discover finds the authorization server through the protected resource metadata (RFC 9728), named in
// the 401's WWW-Authenticate or at its well-known URLs. Without it, the server's origin is taken, as in
// earlier MCP specs. scope is what the server asks for, if it says.
func discover(ctx context.Context, serverURL string) (*authServer, string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("invalid MCP server url %q", serverURL)
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")

	// The server answers an unauthenticated request with 401, naming its metadata
	challenge := map[string]string{}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"msbx","version":"1"}}}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		challenge = authParams(resp.Header.Get("WWW-Authenticate"))
	}

	var resource struct {
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	candidates := []string{challenge["resource_metadata"]}
	if path != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+path)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	issuer := origin
	if getJSON(ctx, candidates, &resource) && len(resource.AuthorizationServers) > 0 {
		issuer = strings.TrimSuffix(resource.AuthorizationServers[0], "/")
	}

	iu, err := url.Parse(issuer)
	if err != nil {
		return nil, "", fmt.Errorf("invalid authorization server %q", issuer)
	}
	iOrigin, iPath := iu.Scheme+"://"+iu.Host, strings.TrimSuffix(iu.Path, "/")
	var as authServer
	if !getJSON(ctx, []string{
		iOrigin + "/.well-known/oauth-authorization-server" + iPath,
		iOrigin + "/.well-known/openid-configuration" + iPath,
		issuer + "/.well-known/openid-configuration",
	}, &as) || as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, "", fmt.Errorf("no OAuth authorization server metadata found for %s (authorization server %s)", serverURL, issuer)
	}
	if as.Issuer == "" {
		as.Issuer = issuer
	}
	scope := challenge["scope"]
	if scope == "" {
		scope = strings.Join(resource.ScopesSupported, " ")
	}
	return &as, scope, nil
}

var authParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

func authParams(header string) map[string]string {
	params := map[string]string{}
	for _, m := range authParam.FindAllStringSubmatch(header, -1) {
		params[m[1]] = m[2]
	}
	return params
}

// getJSON decodes the first URL that answers 200 with JSON into v, and reports whether one did.
func getJSON(ctx context.Context, urls []string, v any) bool {
	for _, u := range urls {
		if u == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && json.Unmarshal(data, v) == nil {
			return true
		}
	}
	return false
}

// registerClient registers msbx as a public client (RFC 7591).
func registerClient(ctx context.Context, endpoint, redirect, scope string) (id, secret string, err error) {
	reg := map[string]any{
		"client_name":                "msbx",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if scope != "" {
		reg["scope"] = scope
	}
	data, _ := json.Marshal(reg)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("register a client: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if resp.StatusCode/100 != 2 || json.Unmarshal(body, &out) != nil || out.ClientID == "" {
		return "", "", fmt.Errorf("register a client: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return out.ClientID, out.ClientSecret, nil
}
