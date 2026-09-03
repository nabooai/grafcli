// Package cfg resolves configuration from flags, environment, .env and disk.
//
// Settings live in ~/.graf/config.json; credentials live beside them in
// ~/.graf/.credentials.json (see internal/auth). $XDG_CONFIG_HOME/graf/ is read
// as a fallback for anyone who configured the CLI there.
//
// Save rewrites only the keys this CLI owns and preserves everything else, so a
// config file shared with another tool survives a `graf config set`.
package cfg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// IsLoopback reports whether baseURL points at this machine — the one place a
// missing credential is not an error.
func IsLoopback(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const (
	// DefaultBaseURL is the deployment the CLI talks to when nothing overrides
	// it. Graf serves the SPA and the API from one origin, so there is a single
	// URL rather than the app/api pair a split deployment would need.
	DefaultBaseURL = "https://graf.nissimtech.com"

	// DefaultModel and DefaultReasoning are left empty on purpose: the server
	// owns its own defaults, and a blank field means "server default". Pinning
	// them here would silently freeze the CLI to whatever was current the day
	// it was built.
	DefaultFdaVersion = 14

	EnvBaseURL      = "GRAF_URL"
	EnvModel        = "GRAF_MODEL"
	EnvReasoning    = "GRAF_REASONING"
	EnvNoInput      = "GRAF_NO_INPUT"
	EnvConfigDir    = "GRAF_CONFIG_DIR"
	EnvNoDotenv     = "GRAF_NO_DOTENV"
	EnvClientID     = "GRAF_CF_ACCESS_CLIENT_ID"
	EnvClientSecret = "GRAF_CF_ACCESS_CLIENT_SECRET"
	// The CLOUDFLARE_ACCESS_* pair is the spelling Cloudflare's own docs and
	// most .env files use. Read as an alias so an existing service token works
	// without being renamed.
	EnvAltClientID     = "CLOUDFLARE_ACCESS_CLIENT_ID"
	EnvAltClientSecret = "CLOUDFLARE_ACCESS_CLIENT_SECRET"
	// EnvCookie carries a browser-issued CF_Authorization JWT, which is what
	// you get by copying a request out of devtools. Short-lived — service
	// tokens are the supported path — but it unblocks a one-off.
	EnvCookie = "GRAF_CF_AUTHORIZATION"
	// EnvAPIToken is the deployment's own bearer token — the one the graf serve
	// checks on its /api/cli endpoints (GRAF_API_TOKEN on the server side).
	EnvAPIToken = "GRAF_API_TOKEN"
)

// Config is the on-disk settings file. Secrets are excluded by construction.
type Config struct {
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	Reasoning  string `json:"reasoning,omitempty"`
	FdaVersion int    `json:"fda_version,omitempty"`
}

// Resolved is the effective configuration plus the origin of each value, so
// `graf config list --explain` can show why a setting took effect.
type Resolved struct {
	BaseURL    string
	Model      string
	Reasoning  string
	FdaVersion int

	BaseURLOrigin    string
	ModelOrigin      string
	ReasoningOrigin  string
	FdaVersionOrigin string
}

// Dir is the CLI's state directory: ~/.graf, or $GRAF_CONFIG_DIR.
func Dir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".graf"), nil
}

// fallbackPath is the XDG location, read when a key is absent from Dir().
// Never written to.
func fallbackPath() string {
	if os.Getenv(EnvConfigDir) != "" {
		return ""
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "graf", "config.json")
}

// Path returns the settings file location.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func readFile(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// Load reads the settings file, falling back to the XDG location per key.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	c, err := readFile(p)
	if err != nil {
		return c, err
	}
	if fb := fallbackPath(); fb != "" && fb != p {
		alt, err := readFile(fb)
		if err != nil {
			return c, err
		}
		if c.BaseURL == "" {
			c.BaseURL = alt.BaseURL
		}
		if c.Model == "" {
			c.Model = alt.Model
		}
		if c.Reasoning == "" {
			c.Reasoning = alt.Reasoning
		}
		if c.FdaVersion == 0 {
			c.FdaVersion = alt.FdaVersion
		}
	}
	return c, nil
}

// Save writes the settings file, preserving keys this CLI does not own.
//
// The document is read, our keys are set, and the rest is written back
// untouched. It refuses to write at all if the existing file cannot be parsed,
// rather than replacing something it does not understand.
func Save(c Config) error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p := filepath.Join(d, "config.json")

	doc := map[string]json.RawMessage{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("refusing to overwrite unparseable %s: %w", p, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	set := func(key string, value any) error {
		switch v := value.(type) {
		case string:
			if v == "" {
				return nil
			}
		case int:
			if v == 0 {
				return nil
			}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		doc[key] = raw
		return nil
	}
	if err := set("base_url", c.BaseURL); err != nil {
		return err
	}
	if err := set("model", c.Model); err != nil {
		return err
	}
	if err := set("reasoning", c.Reasoning); err != nil {
		return err
	}
	if err := set("fda_version", c.FdaVersion); err != nil {
		return err
	}

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Resolve applies the precedence chain: flag > env > config file > default.
func Resolve(flagBaseURL, flagModel, flagReasoning string, flagFdaVersion int) (Resolved, error) {
	var r Resolved
	file, err := Load()
	if err != nil {
		return r, err
	}

	switch {
	case flagBaseURL != "":
		r.BaseURL, r.BaseURLOrigin = flagBaseURL, "flag --url"
	case os.Getenv(EnvBaseURL) != "":
		r.BaseURL, r.BaseURLOrigin = os.Getenv(EnvBaseURL), "env "+EnvBaseURL
	case file.BaseURL != "":
		r.BaseURL, r.BaseURLOrigin = file.BaseURL, "config file"
	default:
		r.BaseURL, r.BaseURLOrigin = DefaultBaseURL, "default"
	}
	r.BaseURL = strings.TrimRight(r.BaseURL, "/")

	switch {
	case flagModel != "":
		r.Model, r.ModelOrigin = flagModel, "flag --model"
	case os.Getenv(EnvModel) != "":
		r.Model, r.ModelOrigin = os.Getenv(EnvModel), "env "+EnvModel
	case file.Model != "":
		r.Model, r.ModelOrigin = file.Model, "config file"
	default:
		r.ModelOrigin = "server default"
	}

	switch {
	case flagReasoning != "":
		r.Reasoning, r.ReasoningOrigin = flagReasoning, "flag --reasoning"
	case os.Getenv(EnvReasoning) != "":
		r.Reasoning, r.ReasoningOrigin = os.Getenv(EnvReasoning), "env "+EnvReasoning
	case file.Reasoning != "":
		r.Reasoning, r.ReasoningOrigin = file.Reasoning, "config file"
	default:
		r.ReasoningOrigin = "server default"
	}

	switch {
	case flagFdaVersion != 0:
		r.FdaVersion, r.FdaVersionOrigin = flagFdaVersion, "flag --fda-version"
	case file.FdaVersion != 0:
		r.FdaVersion, r.FdaVersionOrigin = file.FdaVersion, "config file"
	default:
		r.FdaVersion, r.FdaVersionOrigin = DefaultFdaVersion, "default"
	}

	return r, nil
}

// LoadDotenv reads KEY=VALUE pairs from path into the process environment
// WITHOUT overwriting anything already set, so a real environment variable
// always beats a file on disk.
//
// This exists because a Cloudflare Access service token is normally handed out
// as a .env fragment; making the operator re-export it by hand is friction with
// no security benefit. Only the keys the CLI actually reads are imported —
// a .env full of unrelated production secrets does not get splashed into the
// environment of every child process.
func LoadDotenv(path string) (int, error) {
	if os.Getenv(EnvNoDotenv) != "" {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	wanted := map[string]bool{
		EnvClientID: true, EnvClientSecret: true,
		EnvAltClientID: true, EnvAltClientSecret: true,
		EnvCookie: true, EnvBaseURL: true,
		EnvModel: true, EnvReasoning: true,
	}

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !wanted[key] {
			continue
		}
		val = strings.TrimSpace(val)
		// Strip one layer of matching quotes, the way a shell would.
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if val == "" || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}
