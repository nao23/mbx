package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// login is mbx's OAuth login to an MCP server.
type login struct {
	URL          string    `json:"url"` // as configured when logging in
	TokenURL     string    `json:"token_url"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"` // zero if the server didn't say
	AccessToken  string    `json:"-"`               // in their own keychain items
	RefreshToken string    `json:"-"`
}

// valid reports whether the token can be used for another 30 seconds.
func (l *login) valid() bool {
	return l.AccessToken != "" && (l.Expiry.IsZero() || time.Now().Before(l.Expiry.Add(-30*time.Second)))
}

func (l *login) refresh(ctx context.Context) error {
	if l.RefreshToken == "" {
		return errors.New("the token expired and there's no refresh token")
	}
	conf := &oauth2.Config{ClientID: l.ClientID, ClientSecret: l.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: l.TokenURL}}
	tok, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: l.RefreshToken}).Token()
	if err != nil {
		return err
	}
	l.AccessToken, l.Expiry = tok.AccessToken, tok.Expiry
	if tok.RefreshToken != "" {
		l.RefreshToken = tok.RefreshToken // rotated
	}
	return nil
}

// A login takes three keychain items, as go-keyring writes at most about 3 KB per item on macOS: account
// <server> holds the JSON, <server>:access and <server>:refresh the tokens.
const keychainService = "mbx-mcp-gateway"

func loadLogin(server string) (*login, error) {
	data, err := keyring.Get(keychainService, server)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("read the login for %s from the keychain: %w", server, err)
	}
	var l login
	if err := json.Unmarshal([]byte(data), &l); err != nil {
		return nil, fmt.Errorf("the keychain's login for %s is unreadable (log in again): %w", server, err)
	}
	if l.AccessToken, err = keyring.Get(keychainService, server+":access"); errors.Is(err, keyring.ErrNotFound) {
		return nil, nil // saved by an older mbx, with the tokens in the JSON
	} else if err != nil {
		return nil, fmt.Errorf("read the token for %s from the keychain: %w", server, err)
	}
	if l.RefreshToken, err = keyring.Get(keychainService, server+":refresh"); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("read the refresh token for %s from the keychain: %w", server, err)
	}
	return &l, nil
}

func saveLogin(server string, l *login) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	items := [][2]string{{server, string(data)}, {server + ":access", l.AccessToken}}
	if l.RefreshToken != "" {
		items = append(items, [2]string{server + ":refresh", l.RefreshToken})
	} else if err := keyring.Delete(keychainService, server+":refresh"); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	for _, it := range items {
		if err := keyring.Set(keychainService, it[0], it[1]); err != nil {
			return fmt.Errorf("save the login for %s in the keychain: %w", server, err)
		}
	}
	return nil
}

func removeLogin(server string) error {
	for _, account := range []string{server, server + ":access", server + ":refresh"} {
		if err := keyring.Delete(keychainService, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("remove the login for %s from the keychain: %w", server, err)
		}
	}
	return nil
}
