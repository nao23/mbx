package mcpgateway

import (
	"bytes"

	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// gateway serves every sandbox, each on its own socket.
type gateway struct {
	mu        sync.Mutex
	upstreams map[string]*upstream
	sockets   map[string]*sandboxSocket // by sandbox name
}

type sandboxSocket struct {
	id  string
	srv *http.Server
}

// Serve runs the gateway, opening and closing sockets as sandboxes are registered and removed. mbx run
// talks to it only through the registration files.
func Serve() error {
	if err := os.MkdirAll(filepath.Dir(SocketPath("x")), 0o700); err != nil {
		return err
	}
	gw := &gateway{upstreams: map[string]*upstream{}, sockets: map[string]*sandboxSocket{}}
	log.Printf("MCP gateway serving sockets in %s", filepath.Dir(SocketPath("x")))
	for {
		gw.reconcile()
		time.Sleep(time.Second)
	}
}

func (gw *gateway) reconcile() {
	regs := registrations()
	for name, s := range gw.sockets {
		if reg, ok := regs[name]; !ok || reg.ID != s.id {
			s.srv.Close()
			os.Remove(SocketPath(s.id))
			delete(gw.sockets, name)
			log.Printf("%s: closed its socket", name)
		}
	}
	for name, reg := range regs {
		if _, ok := gw.sockets[name]; ok {
			continue
		}
		path := SocketPath(reg.ID)
		os.Remove(path) // left by a gateway that was stopped
		ln, err := net.Listen("unix", path)
		if err != nil {
			log.Printf("%s: %v", name, err)
			continue
		}
		os.Chmod(path, 0o600)
		srv := &http.Server{Handler: gw.handler(name)}
		go srv.Serve(ln)
		gw.sockets[name] = &sandboxSocket{id: reg.ID, srv: srv}
		log.Printf("%s: opened %s", name, path)
	}
}

// handler serves the sandbox's socket. The registration is read on each request, so changes apply at once
func (gw *gateway) handler(sandbox string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server, err := url.PathUnescape(strings.TrimPrefix(req.URL.Path, "/"))
		reg := Registered(sandbox)
		if err != nil || reg == nil || !slices.Contains(reg.Servers, server) {
			log.Printf("%s: refused %s %s: server not allowed", sandbox, req.Method, server)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		up, err := gw.upstream(server)
		if err != nil {
			log.Printf("%s: %v", server, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		up.serve(w, req, sandbox, reg)
	})
}

func (gw *gateway) upstream(server string) (*upstream, error) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if up, ok := gw.upstreams[server]; ok {
		return up, nil
	}
	up, err := newUpstream(server)
	if err != nil {
		return nil, err
	}
	gw.upstreams[server] = up
	return up, nil
}

type upstream struct {
	name     string
	claudeAI bool
	tokens   *tokenCache
	blocked  toolPolicy
	proxy    *httputil.ReverseProxy
}

func newUpstream(server string) (*upstream, error) {
	up := &upstream{name: server, tokens: tokenCacheFor(server)}
	tok, err := up.tokens.get()
	if err != nil {
		return nil, err
	}
	var serverURL string
	if display, ok := strings.CutPrefix(server, ClaudeAIPrefix); ok {
		up.claudeAI = true
		id, policy, err := findConnector(tok, display)
		if err != nil {
			return nil, err
		}
		serverURL, up.blocked = "https://mcp-proxy.anthropic.com/v1/mcp/"+id, policy
		log.Printf("%s: connector %s, blocked tools %v", server, id, policy.blockedNames())
	} else {
		// The url logged in to, so the token goes only where it was issued for
		l, err := loadLogin(server)
		if err != nil {
			return nil, err
		}
		serverURL = l.URL
	}
	target, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	up.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host, r.Out.Host = target.Scheme, target.Host, target.Host
			r.Out.URL.Path, r.Out.URL.RawPath = target.Path, ""
			r.Out.Header.Del("Authorization")
			if tok, err := up.tokens.get(); err == nil {
				r.Out.Header.Set("Authorization", "Bearer "+tok)
			}
		},
		ModifyResponse: up.modifyResponse,
		FlushInterval:  -1, // stream SSE responses as they arrive
	}
	return up, nil
}

func (up *upstream) serve(w http.ResponseWriter, req *http.Request, sandbox string, reg *Registration) {
	if up.claudeAI {
		// Anthropic's MCP proxy requires a client session ID; one per sandbox
		req.Header.Set("X-Mcp-Client-Session-Id", reg.Session)
	}
	// Refuse calls to blocked tools without forwarding them
	if req.Method == http.MethodPost && !up.blocked.empty() {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		if id, tool, ok := up.blocked.blockedCall(body); ok {
			log.Printf("%s %s: refused tools/call %s (blocked)", sandbox, up.name, tool)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{
				"code": -32602, "message": "tool " + tool + " is blocked in the connector's claude.ai settings"}})
			return
		}
	}
	log.Printf("%s %s: %s", sandbox, up.name, req.Method)
	up.proxy.ServeHTTP(w, req)
}

func (up *upstream) modifyResponse(resp *http.Response) error {
	// Don't let the VM's claude start its own OAuth flow on a 401
	resp.Header.Del("WWW-Authenticate")
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusMethodNotAllowed {
		// Log up to 500 bytes of the upstream error and pass the body on unchanged
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		log.Printf("%s: upstream %d: %s", up.name, resp.StatusCode, body)
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	if up.blocked.empty() {
		return nil
	}
	// Remove blocked tools from tools/list results (JSON or SSE responses)
	switch ct := resp.Header.Get("Content-Type"); {
	case strings.HasPrefix(ct, "application/json"):
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		body = up.blocked.filterMessage(body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	case strings.HasPrefix(ct, "text/event-stream"):
		resp.Body = up.blocked.filterSSE(resp.Body)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
	}
	return nil
}
