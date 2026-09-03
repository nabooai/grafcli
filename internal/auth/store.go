// Package auth stores and resolves the Cloudflare Access credential the CLI
// presents to a Graf deployment.
//
// Graf sits behind Cloudflare Access, so "logging in" means holding a service
// token (a client id / secret pair sent as CF-Access-Client-Id and
// CF-Access-Client-Secret) or, for a one-off, a browser-issued CF_Authorization
// JWT. There is no application-level login beyond that: past the Access edge,
// the API trusts the request.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
)

// Credentials is the on-disk credential file, mode 0600.
type Credentials struct {
	// ClientID and ClientSecret are a Cloudflare Access service token.
	ClientID     string `json:"cf_access_client_id,omitempty"`
	ClientSecret string `json:"cf_access_client_secret,omitempty"`
	// Cookie is a browser-issued CF_Authorization JWT. Expires within a day or
	// so; kept because copying one out of devtools is sometimes the fastest way
	// to get unblocked.
	Cookie string `json:"cf_authorization,omitempty"`
	// APIToken is the graf deployment's OWN bearer token (its GRAF_API_TOKEN),
	// sent as "Authorization: Bearer". Independent of the Access edge: a
	// deployment can require one, the other, or both.
	APIToken string `json:"api_token,omitempty"`
}

// Empty reports whether nothing usable is held.
func (c Credentials) Empty() bool {
	return !c.HasAccess() && c.APIToken == ""
}

// HasAccess reports whether a Cloudflare Access credential is held.
func (c Credentials) HasAccess() bool {
	return (c.ClientID != "" && c.ClientSecret != "") || c.Cookie != ""
}

// Merge overlays the non-empty fields of `over` onto c — how "auth login"
// stores a token without discarding a previously stored service token.
func (c Credentials) Merge(over Credentials) Credentials {
	if over.ClientID != "" && over.ClientSecret != "" {
		c.ClientID, c.ClientSecret = over.ClientID, over.ClientSecret
	}
	if over.Cookie != "" {
		c.Cookie = over.Cookie
	}
	if over.APIToken != "" {
		c.APIToken = over.APIToken
	}
	return c
}

// Source names where a credential came from, for `auth status` and `doctor`.
type Source string

const (
	SourceEnv    Source = "environment"
	SourceDotenv Source = ".env file"
	SourceFile   Source = "credentials file"
	SourceNone   Source = "none"
)

// Path returns the credential file location. It is dot-prefixed so it stays
// out of a casual `ls` of a shared directory.
func Path() (string, error) {
	d, err := cfg.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ".credentials.json"), nil
}

// Load reads stored credentials. A missing file is not an error.
func Load() (Credentials, bool, error) {
	var c Credentials
	p, err := Path()
	if err != nil {
		return c, false, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, false, fmt.Errorf("parsing %s: %w", p, err)
	}
	return c, !c.Empty(), nil
}

// Save writes credentials at mode 0600, creating ~/.graf at 0700.
func Save(c Credentials) error {
	d, err := cfg.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(d, ".credentials.json")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Delete removes the credential file. A missing file is not an error.
func Delete() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Resolve returns the credential to use, in precedence order: environment
// (including anything a .env contributed, which is imported into the
// environment without overwriting it), then the stored file. The two halves —
// the Cloudflare Access credential and the deployment's own API token — are
// resolved independently, so a service token from the environment and a stored
// API token combine.
//
// Credentials are never accepted as command-line arguments — argv is
// world-readable through /proc and `ps`.
func Resolve() (Credentials, Source, error) {
	var c Credentials
	source := SourceNone

	id := firstEnv(cfg.EnvClientID, cfg.EnvAltClientID)
	secret := firstEnv(cfg.EnvClientSecret, cfg.EnvAltClientSecret)
	switch {
	case id != "" && secret != "":
		c.ClientID, c.ClientSecret = id, secret
		source = SourceEnv
	case id != "" || secret != "":
		// A half-configured service token is nearly always a typo in one of
		// the two variable names, and it fails at the edge with an opaque
		// redirect to a login page. Say so here instead.
		missing := cfg.EnvClientSecret
		if id == "" {
			missing = cfg.EnvClientID
		}
		return Credentials{}, SourceNone, clierr.New("unauthenticated", clierr.Unauthenticated,
			"incomplete Cloudflare Access service token: %s is not set", missing).
			WithHint("a service token is a pair; both halves must be present")
	default:
		if ck := strings.TrimSpace(os.Getenv(cfg.EnvCookie)); ck != "" {
			c.Cookie = ck
			source = SourceEnv
		}
	}
	if tok := firstEnv(cfg.EnvAPIToken); tok != "" {
		c.APIToken = tok
		if source == SourceNone {
			source = SourceEnv
		}
	}

	stored, ok, err := Load()
	if err != nil {
		return Credentials{}, SourceNone, err
	}
	if ok {
		if !c.HasAccess() && stored.HasAccess() {
			c.ClientID, c.ClientSecret, c.Cookie = stored.ClientID, stored.ClientSecret, stored.Cookie
			if source == SourceNone {
				source = SourceFile
			}
		}
		if c.APIToken == "" && stored.APIToken != "" {
			c.APIToken = stored.APIToken
			if source == SourceNone {
				source = SourceFile
			}
		}
	}
	if c.Empty() {
		return Credentials{}, SourceNone, clierr.NotLoggedIn()
	}
	return c, source, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// Redact abbreviates a secret for display. Never print the whole thing: `auth
// status` output routinely ends up in a bug report or an agent's context.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + "…" + fmt.Sprintf("(%d chars)", len(s))
}
