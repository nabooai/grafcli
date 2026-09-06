// Package update keeps the installed binary current.
//
// The CLI is distributed as GitHub release archives (goreleaser naming:
// grafcli_<ver>_<os>_<arch>.tar.gz|zip, plus checksums.txt). A check asks the
// releases API for the latest tag; an apply downloads this platform's archive,
// verifies its sha256 against checksums.txt, and swaps the binary in place of
// os.Executable() atomically (rename the old one aside, rename the new one in).
//
// Two entry points: `graf update` runs it in the foreground, and the root
// command spawns `graf update --quiet` DETACHED at most once a day, so the
// binary a person or an agent invokes is rarely more than a day stale. State
// (last check, last result) lives in ~/.naboo/update.json so the next
// foreground run can say what happened.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nabooai/grafcli/internal/cfg"
)

const (
	Repo = "nabooai/grafcli"
	// Interval between background checks.
	Interval = 24 * time.Hour
)

// API is the GitHub API base; tests point it at a fake.
var API = "https://api.github.com"

// State is ~/.naboo/update.json.
type State struct {
	LastCheck time.Time `json:"last_check"`
	// Latest is the newest tag seen on the last successful check.
	Latest string `json:"latest,omitempty"`
	// UpdatedTo is set when an apply replaced the binary; cleared once the
	// notice has been shown.
	UpdatedTo string `json:"updated_to,omitempty"`
	Error     string `json:"error,omitempty"`
}

func StatePath() (string, error) {
	d, err := cfg.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "update.json"), nil
}

func LoadState() (State, error) {
	var s State
	p, err := StatePath()
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, nil // a corrupt state file is treated as no state
	}
	return s, nil
}

func SaveState(s State) error {
	p, err := StatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Due reports whether a background check should run now.
func Due(s State, now time.Time) bool {
	return now.Sub(s.LastCheck) >= Interval
}

// Release is what the check learned.
type Release struct {
	Tag    string
	Assets map[string]string // name → API url (downloaded with Accept: octet-stream)
}

// Token is the GitHub token used for the (private) repository: GH_TOKEN,
// GITHUB_TOKEN, else `gh auth token`.
func Token() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil {
		out, err := exec.Command(gh, "auth", "token").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

// Latest asks the releases API for the newest release.
func Latest(ctx context.Context, client *http.Client, token string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "grafcli-update")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
			return Release{}, fmt.Errorf("releases API answered %d — the repository is private; log in with `gh auth login` or set GH_TOKEN", resp.StatusCode)
		}
		return Release{}, fmt.Errorf("releases API answered %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("parsing the releases API response: %w", err)
	}
	rel := Release{Tag: body.TagName, Assets: map[string]string{}}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// Newer reports whether tag is a higher semver than current. Non-semver
// versions ("dev", a git describe) never update.
func Newer(tag, current string) bool {
	a, okA := parse(tag)
	b, okB := parse(current)
	if !okA || !okB {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.SplitN(p, "-", 2)[0])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// AssetName is the archive goreleaser publishes for this platform.
func AssetName(tag string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("grafcli_%s_%s_%s.%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH, ext)
}

func download(ctx context.Context, client *http.Client, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "grafcli-update")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// extractBinary pulls `graf` (or graf.exe) out of the archive.
func extractBinary(name string, blob []byte) ([]byte, error) {
	member := "graf"
	if strings.HasSuffix(name, ".zip") {
		member = "graf.exe"
		zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == member {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("archive %s carries no %s", name, member)
	}
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Name == member || h.Name == "./"+member {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("archive %s carries no %s", name, member)
}

// checksum returns the expected sha256 of `name` from checksums.txt.
func checksum(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], true
		}
	}
	return "", false
}

// Apply downloads rel's archive for this platform, verifies it, and replaces
// the binary at exe. The swap is two renames so a crash mid-way leaves either
// the old or the new binary in place, never a half-written one.
func Apply(ctx context.Context, client *http.Client, rel Release, token, exe string) error {
	name := AssetName(rel.Tag)
	url, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("release %s has no asset for this platform (%s)", rel.Tag, name)
	}
	blob, err := download(ctx, client, url, token)
	if err != nil {
		return err
	}
	if sumsURL, ok := rel.Assets["checksums.txt"]; ok {
		sums, err := download(ctx, client, sumsURL, token)
		if err != nil {
			return fmt.Errorf("downloading checksums.txt: %w", err)
		}
		want, ok := checksum(sums, name)
		if !ok {
			return fmt.Errorf("checksums.txt has no entry for %s", name)
		}
		got := sha256.Sum256(blob)
		if hex.EncodeToString(got[:]) != want {
			return fmt.Errorf("checksum mismatch for %s — refusing to install", name)
		}
	}
	bin, err := extractBinary(name, blob)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".graf-update-*")
	if err != nil {
		return fmt.Errorf("the binary's directory is not writable (%s): %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	// A running executable cannot be overwritten on Windows (and unlinking it
	// on unix is fine): move it aside, then move the new one in.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		os.Rename(old, exe) // best effort: put the previous binary back
		os.Remove(tmpName)
		return err
	}
	os.Remove(old) // fails on Windows while running; harmless, cleaned next time
	return nil
}

// Result is what a Run did.
type Result struct {
	Current string
	Latest  string
	Updated bool
	Message string
}

// Run performs one check, and applies the update when one is available and
// apply is set. It records the outcome in the state file either way.
func Run(ctx context.Context, client *http.Client, current string, apply bool) (Result, error) {
	st, _ := LoadState()
	st.LastCheck = time.Now()
	st.Error = ""
	res := Result{Current: current}
	token := Token()
	rel, err := Latest(ctx, client, token)
	if err != nil {
		st.Error = err.Error()
		_ = SaveState(st)
		return res, err
	}
	res.Latest = rel.Tag
	st.Latest = rel.Tag
	if !Newer(rel.Tag, current) {
		res.Message = "up to date"
		_ = SaveState(st)
		return res, nil
	}
	if !apply {
		res.Message = "update available"
		_ = SaveState(st)
		return res, nil
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		st.Error = err.Error()
		_ = SaveState(st)
		return res, err
	}
	if err := Apply(ctx, client, rel, token, exe); err != nil {
		st.Error = err.Error()
		_ = SaveState(st)
		return res, err
	}
	res.Updated = true
	res.Message = "updated"
	st.UpdatedTo = rel.Tag
	_ = SaveState(st)
	return res, nil
}
