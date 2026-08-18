package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
)

func isolate(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv(cfg.EnvConfigDir, d)
	for _, k := range []string{cfg.EnvClientID, cfg.EnvClientSecret,
		cfg.EnvAltClientID, cfg.EnvAltClientSecret, cfg.EnvCookie} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return d
}

func TestSaveLoadRoundTrip(t *testing.T) {
	d := isolate(t)
	want := Credentials{ClientID: "id", ClientSecret: "secret"}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}

	// The credential file must not be world- or group-readable.
	fi, err := os.Stat(filepath.Join(d, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials mode = %04o, want 0600", perm)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	isolate(t)
	_, ok, err := Load()
	if err != nil {
		t.Fatalf("Load on a missing file: %v", err)
	}
	if ok {
		t.Error("reported credentials that do not exist")
	}
}

func TestResolvePrecedence(t *testing.T) {
	isolate(t)
	if err := Save(Credentials{ClientID: "file-id", ClientSecret: "file-secret"}); err != nil {
		t.Fatal(err)
	}

	// stored file when nothing is exported
	got, src, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "file-id" || src != SourceFile {
		t.Errorf("file tier: %+v from %s", got, src)
	}

	// the CLOUDFLARE_* alias is honored
	t.Setenv(cfg.EnvAltClientID, "alt-id")
	t.Setenv(cfg.EnvAltClientSecret, "alt-secret")
	got, src, _ = Resolve()
	if got.ClientID != "alt-id" || src != SourceEnv {
		t.Errorf("alias tier: %+v from %s", got, src)
	}

	// the GRAF_* pair wins over the alias
	t.Setenv(cfg.EnvClientID, "graf-id")
	t.Setenv(cfg.EnvClientSecret, "graf-secret")
	got, _, _ = Resolve()
	if got.ClientID != "graf-id" {
		t.Errorf("GRAF_ pair should win: %+v", got)
	}
}

// Half a service token fails at the Access edge with an opaque redirect, so it
// is caught here with a message naming the variable that is missing.
func TestResolveRejectsHalfAToken(t *testing.T) {
	isolate(t)
	t.Setenv(cfg.EnvClientID, "only-the-id")
	_, _, err := Resolve()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.ExitCode != clierr.Unauthenticated {
		t.Fatalf("want unauthenticated, got %v", err)
	}
	if !strings.Contains(ce.Detail, cfg.EnvClientSecret) {
		t.Errorf("the error does not name the missing variable: %q", ce.Detail)
	}
}

func TestResolveWithNothingConfigured(t *testing.T) {
	isolate(t)
	_, src, err := Resolve()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.ExitCode != clierr.Unauthenticated {
		t.Fatalf("want unauthenticated, got %v", err)
	}
	if src != SourceNone {
		t.Errorf("source = %s", src)
	}
	if ce.Fix == "" {
		t.Error("an unauthenticated error must carry a runnable fix")
	}
}

func TestRedactNeverRevealsTheWholeSecret(t *testing.T) {
	secret := "0123456789abcdefghij"
	got := Redact(secret)
	if strings.Contains(got, secret) {
		t.Fatalf("Redact returned the secret: %q", got)
	}
	if got == "" {
		t.Fatal("Redact returned nothing at all")
	}
	if Redact("") != "" {
		t.Error("Redact of an empty string should stay empty")
	}
	if strings.Contains(Redact("short"), "short") {
		t.Errorf("a short secret was not masked: %q", Redact("short"))
	}
}

func TestEmpty(t *testing.T) {
	cases := map[string]struct {
		c    Credentials
		want bool
	}{
		"nothing":     {Credentials{}, true},
		"id only":     {Credentials{ClientID: "a"}, true},
		"secret only": {Credentials{ClientSecret: "b"}, true},
		"pair":        {Credentials{ClientID: "a", ClientSecret: "b"}, false},
		"cookie":      {Credentials{Cookie: "jwt"}, false},
	}
	for name, tc := range cases {
		if got := tc.c.Empty(); got != tc.want {
			t.Errorf("%s: Empty() = %v, want %v", name, got, tc.want)
		}
	}
}
