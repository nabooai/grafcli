package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	return d
}

func TestResolvePrecedence(t *testing.T) {
	d := tempDir(t)
	if err := os.WriteFile(filepath.Join(d, "config.json"),
		[]byte(`{"base_url":"https://file.example.com","model":"file-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// config file only
	r, err := Resolve("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != "https://file.example.com" || r.BaseURLOrigin != "config file" {
		t.Errorf("file tier: %q (%s)", r.BaseURL, r.BaseURLOrigin)
	}

	// env beats the file
	t.Setenv(EnvBaseURL, "https://env.example.com")
	r, _ = Resolve("", "", "", 0)
	if r.BaseURL != "https://env.example.com" || r.BaseURLOrigin != "env "+EnvBaseURL {
		t.Errorf("env tier: %q (%s)", r.BaseURL, r.BaseURLOrigin)
	}

	// flag beats env
	r, _ = Resolve("https://flag.example.com", "", "", 0)
	if r.BaseURL != "https://flag.example.com" || r.BaseURLOrigin != "flag --url" {
		t.Errorf("flag tier: %q (%s)", r.BaseURL, r.BaseURLOrigin)
	}

	// a trailing slash would double up when a path is appended
	r, _ = Resolve("https://flag.example.com/", "", "", 0)
	if r.BaseURL != "https://flag.example.com" {
		t.Errorf("trailing slash not trimmed: %q", r.BaseURL)
	}
}

func TestResolveDefaults(t *testing.T) {
	tempDir(t)
	r, err := Resolve("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != DefaultBaseURL || r.BaseURLOrigin != "default" {
		t.Errorf("base_url = %q (%s)", r.BaseURL, r.BaseURLOrigin)
	}
	// Model is deliberately empty: blank means "let the server decide", so the
	// CLI never freezes to whatever was current the day it was built.
	if r.Model != "" || r.ModelOrigin != "server default" {
		t.Errorf("model = %q (%s)", r.Model, r.ModelOrigin)
	}
	if r.FdaVersion != DefaultFdaVersion {
		t.Errorf("fda_version = %d", r.FdaVersion)
	}
}

// Save must not delete keys it does not own — the file may be shared.
func TestSavePreservesForeignKeys(t *testing.T) {
	d := tempDir(t)
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, []byte(`{"base_url":"https://old","someone_elses_token":"keep-me","nested":{"a":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Config{BaseURL: "https://new"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"keep-me", "https://new", `"nested"`} {
		if !contains(got, want) {
			t.Errorf("Save dropped %q:\n%s", want, got)
		}
	}
	if contains(got, "https://old") {
		t.Errorf("Save did not update base_url:\n%s", got)
	}
}

// Replacing a file it cannot parse would destroy someone else's settings.
func TestSaveRefusesUnparseableFile(t *testing.T) {
	d := tempDir(t)
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Config{BaseURL: "https://new"}); err == nil {
		t.Fatal("Save should refuse to overwrite an unparseable config")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "not json at all" {
		t.Errorf("the original file was modified: %q", b)
	}
}

func TestSaveWritesPrivateMode(t *testing.T) {
	d := tempDir(t)
	if err := Save(Config{BaseURL: "https://x"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(d, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.json mode = %04o, want 0600", perm)
	}
}

func TestLoadDotenv(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, ".env")
	body := "# a comment\n\n" +
		"export CLOUDFLARE_ACCESS_CLIENT_ID=from-file\n" +
		"CLOUDFLARE_ACCESS_CLIENT_SECRET=\"quoted-secret\"\n" +
		"GRAF_MODEL='single-quoted'\n" +
		"UNRELATED_PRODUCTION_SECRET=should-not-be-imported\n" +
		"malformed line without equals\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvAltClientID, "")
	t.Setenv(EnvAltClientSecret, "")
	t.Setenv(EnvModel, "")
	os.Unsetenv(EnvAltClientID)
	os.Unsetenv(EnvAltClientSecret)
	os.Unsetenv(EnvModel)
	os.Unsetenv("UNRELATED_PRODUCTION_SECRET")

	n, err := LoadDotenv(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("imported %d keys, want 3", n)
	}
	if got := os.Getenv(EnvAltClientID); got != "from-file" {
		t.Errorf("client id = %q", got)
	}
	if got := os.Getenv(EnvAltClientSecret); got != "quoted-secret" {
		t.Errorf("quotes were not stripped: %q", got)
	}
	if got := os.Getenv(EnvModel); got != "single-quoted" {
		t.Errorf("single quotes were not stripped: %q", got)
	}
	// Only the keys the CLI reads are imported; a .env full of unrelated
	// production secrets must not be splashed into every child process.
	if os.Getenv("UNRELATED_PRODUCTION_SECRET") != "" {
		t.Error("an unrelated key was imported from .env")
	}
}

// A real environment variable always beats a file on disk.
func TestLoadDotenvNeverOverwritesTheEnvironment(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, ".env")
	if err := os.WriteFile(p, []byte("CLOUDFLARE_ACCESS_CLIENT_ID=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAltClientID, "from-environment")
	if _, err := LoadDotenv(p); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(EnvAltClientID); got != "from-environment" {
		t.Errorf("the .env overwrote a real environment variable: %q", got)
	}
}

func TestLoadDotenvKillSwitch(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, ".env")
	if err := os.WriteFile(p, []byte("CLOUDFLARE_ACCESS_CLIENT_ID=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvNoDotenv, "1")
	os.Unsetenv(EnvAltClientID)
	n, err := LoadDotenv(p)
	if err != nil || n != 0 {
		t.Fatalf("kill switch ignored: n=%d err=%v", n, err)
	}
	if os.Getenv(EnvAltClientID) != "" {
		t.Error("a key was imported despite " + EnvNoDotenv)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestDirIsNabooWithLegacyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GRAF_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	d, err := Dir()
	if err != nil || d != filepath.Join(home, ".naboo") {
		t.Fatalf("Dir() = %q (%v)", d, err)
	}
	// only the legacy ~/.graf/config.json exists: its keys are read
	legacy := filepath.Join(home, ".graf")
	os.MkdirAll(legacy, 0o700)
	os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"base_url":"https://legacy.example","model":"m"}`), 0o600)
	c, err := Load()
	if err != nil || c.BaseURL != "https://legacy.example" || c.Model != "m" {
		t.Fatalf("legacy fallback: %+v (%v)", c, err)
	}
	// ~/.naboo wins per key, and a Save writes ONLY there
	off := false
	if err := Save(Config{BaseURL: "https://new.example", AutoUpdate: &off}); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if c.BaseURL != "https://new.example" || c.Model != "m" || c.AutoUpdateEnabled() {
		t.Errorf("merged: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(home, ".naboo", "config.json")); err != nil {
		t.Error("Save did not write ~/.naboo/config.json")
	}
	b, _ := os.ReadFile(filepath.Join(legacy, "config.json"))
	if string(b) != `{"base_url":"https://legacy.example","model":"m"}` {
		t.Error("Save touched the legacy file")
	}
}
