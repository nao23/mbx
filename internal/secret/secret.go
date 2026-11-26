// Package secret fetches secret values on the host.
package secret

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/nao23/mbx/internal/shell"
)

// KeychainAccount is the login user, the account `security add-generic-password -a "$USER"` and Claude Code use.
func KeychainAccount() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// Keychain names the store keychain items are in: the macOS keychain, or on Linux the Secret Service
// (GNOME Keyring, KWallet, etc.), as go-keyring uses them.
func Keychain() string {
	if runtime.GOOS == "darwin" {
		return "the macOS keychain"
	}
	return "the Secret Service keyring"
}

// AddKeychainItem is the command that adds the keychain item name, for the login user. secret-tool reads
// the value from stdin.
func AddKeychainItem(name string) string {
	if runtime.GOOS == "darwin" {
		return "security add-generic-password -s " + shell.Quote(name) + ` -a "$USER" -w '<value>'`
	}
	return "secret-tool store --label " + shell.Quote(name) + " service " + shell.Quote(name) + ` username "$USER"`
}

// Source says where a secret value comes from. Set exactly one field
type Source struct {
	Keychain string `yaml:"keychain,omitempty"`
	Command  string `yaml:"command,omitempty"`
	Env      string `yaml:"env,omitempty"`
}

// Describe returns the command that fetches the value, for dry-run output.
func (s Source) Describe() string {
	switch {
	case s.Keychain != "" && runtime.GOOS != "darwin":
		return "secret-tool lookup service " + shell.Quote(s.Keychain) + " username " + shell.Quote(KeychainAccount())
	case s.Keychain != "":
		return "security find-generic-password -s " + shell.Quote(s.Keychain) + " -a " + shell.Quote(KeychainAccount()) + " -w"
	case s.Command != "":
		return s.Command
	default:
		return "printenv " + shell.Quote(s.Env)
	}
}

func (s Source) Label() string {
	switch {
	case s.Keychain != "":
		return "keychain " + shell.Quote(s.Keychain)
	case s.Command != "":
		return "$ " + s.Command
	default:
		return "env " + s.Env
	}
}

// Value fetches the value. Errors say how to fix it.
func (s Source) Value() (string, error) {
	return s.Fetch(os.Stderr)
}

// Fetch is Value with a command's own messages (such as login prompts) written to stderr.
func (s Source) Fetch(stderr io.Writer) (string, error) {
	var out []byte
	var err error
	switch {
	case s.Keychain != "":
		v, err := keyring.Get(s.Keychain, KeychainAccount())
		if err != nil {
			return "", fmt.Errorf(
				"keychain item %s (account %s) not found or not readable (add it with: %s)",
				shell.Quote(s.Keychain),
				KeychainAccount(),
				AddKeychainItem(s.Keychain),
			)
		}
		out = []byte(v)
	case s.Command != "":
		cmd := exec.Command("sh", "-c", s.Command)
		cmd.Stderr = stderr
		out, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("command `%s` failed (%w); see its output above", s.Command, err)
		}
	case s.Env != "":
		out = []byte(os.Getenv(s.Env))
	default:
		return "", errors.New("no source (keychain / command / env)")
	}

	val := strings.TrimSpace(string(out))
	if val == "" {
		if s.Env != "" {
			return "", fmt.Errorf("environment variable %s is not set", s.Env)
		}
		return "", fmt.Errorf("%s gave an empty value", s.Label())
	}
	return val, nil
}
