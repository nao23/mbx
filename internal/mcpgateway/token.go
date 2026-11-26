package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/secret"
)

// ClaudeAIPrefix marks a claude.ai connector among a registration's servers.
const ClaudeAIPrefix = "claudeai:"

type tokenCache struct {
	server   string
	claudeAI bool
	mu       sync.Mutex
	token    string
	expires  time.Time
}

func tokenCacheFor(server string) *tokenCache {
	return &tokenCache{server: server, claudeAI: strings.HasPrefix(server, ClaudeAIPrefix)}
}

func (c *tokenCache) get() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires.Add(-30*time.Second)) {
		return c.token, nil
	}
	if c.claudeAI {
		tok, expires, err := hostClaudeAIToken()
		if err != nil {
			return "", err
		}
		c.token, c.expires = tok, expires
		return c.token, nil
	}
	l, err := loadLogin(c.server)
	if err != nil {
		return "", err
	}
	if l == nil {
		return "", fmt.Errorf("not logged in to %s; run mbx mcp login %s", c.server, c.server)
	}
	if !l.valid() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := l.refresh(ctx); err != nil {
			return "", fmt.Errorf("refresh the token for %s: %v; log in again with mbx mcp login %s", c.server, err, c.server)
		}
		if err := saveLogin(c.server, l); err != nil {
			return "", err
		}
	}
	c.token, c.expires = l.AccessToken, l.Expiry
	if c.expires.IsZero() {
		c.expires = time.Now().Add(5 * time.Minute) // no expiry given; read the login again in a while
	}
	return c.token, nil
}

// hostClaudeAIToken is read only for claude.claude_ai_connectors, and refreshed only by the host claude.
func hostClaudeAIToken() (string, time.Time, error) {
	data, err := hostClaudeCredentials()
	if err != nil {
		return "", time.Time{}, err
	}
	var creds struct {
		ClaudeAIOAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // ms since epoch
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", time.Time{}, err
	}
	e := creds.ClaudeAIOAuth
	expires := time.UnixMilli(e.ExpiresAt)
	if e.AccessToken == "" || time.Now().After(expires) {
		return "", time.Time{}, fmt.Errorf(
			"the host claude's claude.ai token expired at %s; use the host claude once to refresh it",
			expires.Format(time.RFC3339),
		)
	}
	return e.AccessToken, expires, nil
}

// hostClaudeCredentials reads where the host claude keeps its credentials: the keychain on macOS, else
// .credentials.json in ~/.claude.
func hostClaudeCredentials() ([]byte, error) {
	if runtime.GOOS == "darwin" {
		out, err := keyring.Get("Claude Code-credentials", secret.KeychainAccount())
		if err != nil {
			return nil, fmt.Errorf("read the host claude's credentials from the keychain: %w", err)
		}
		return []byte(out), nil
	}
	data, err := os.ReadFile(filepath.Join(config.HostClaudeDir(), ".credentials.json"))
	if err != nil {
		return nil, fmt.Errorf("read the host claude's credentials: %w", err)
	}
	return data, nil
}
