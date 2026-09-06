package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.2.0", "v0.1.1", true},
		{"v0.1.1", "v0.1.1", false},
		{"v0.1.0", "v0.1.1", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.2.0", "dev", false},
		{"nonsense", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.tag, c.cur, got, c.want)
		}
	}
}

func TestDue(t *testing.T) {
	now := time.Now()
	if !Due(State{}, now) {
		t.Error("no state must be due")
	}
	if Due(State{LastCheck: now.Add(-time.Hour)}, now) {
		t.Error("an hour-old check must not be due")
	}
	if !Due(State{LastCheck: now.Add(-25 * time.Hour)}, now) {
		t.Error("a day-old check must be due")
	}
}

func archive(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "graf", Mode: 0o755, Size: int64(len(bin))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(bin)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// The whole flow against a fake releases API: latest → asset → checksum →
// the binary on disk is replaced atomically and the old one is gone.
func TestApplyReplacesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test builds a tar.gz")
	}
	newBin := []byte("#!/bin/sh\necho new\n")
	arch := archive(t, newBin)
	sum := sha256.Sum256(arch)
	name := AssetName("v9.9.9")
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v9.9.9",
				"assets": []map[string]string{
					{"name": name, "url": srv.URL + "/asset/bin"},
					{"name": "checksums.txt", "url": srv.URL + "/asset/sums"},
				},
			})
		case "/asset/bin":
			if r.Header.Get("Accept") != "application/octet-stream" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write(arch)
		case "/asset/sums":
			w.Write([]byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	API = srv.URL

	rel, err := Latest(context.Background(), srv.Client(), "tok")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Tag != "v9.9.9" {
		t.Fatalf("tag = %q", rel.Tag)
	}
	if _, err := Latest(context.Background(), srv.Client(), ""); err == nil {
		t.Error("a private repo without a token must fail loudly")
	}

	exe := filepath.Join(t.TempDir(), "graf")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), srv.Client(), rel, "tok", exe); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, newBin) {
		t.Errorf("binary not replaced: %q", got)
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error("the old binary was left behind")
	}
	st, _ := os.Stat(exe)
	if st.Mode()&0o100 == 0 {
		t.Error("the new binary is not executable")
	}

	// A tampered archive must be refused and the binary left untouched.
	rel.Assets["checksums.txt"] = srv.URL + "/asset/sums"
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset/sums" {
			w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  " + name + "\n"))
			return
		}
		w.Write(arch)
	}))
	t.Cleanup(tampered.Close)
	rel.Assets[name] = tampered.URL + "/asset/bin"
	rel.Assets["checksums.txt"] = tampered.URL + "/asset/sums"
	if err := Apply(context.Background(), tampered.Client(), rel, "tok", exe); err == nil {
		t.Error("a checksum mismatch must refuse to install")
	}
	got, _ = os.ReadFile(exe)
	if !bytes.Equal(got, newBin) {
		t.Error("a refused update touched the binary")
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	st := State{LastCheck: time.Now().Truncate(time.Second), Latest: "v1.2.3", UpdatedTo: "v1.2.3"}
	if err := SaveState(st); err != nil {
		t.Fatal(err)
	}
	back, err := LoadState()
	if err != nil || back.Latest != "v1.2.3" || back.UpdatedTo != "v1.2.3" || !back.LastCheck.Equal(st.LastCheck) {
		t.Errorf("round trip: %+v (%v)", back, err)
	}
}
