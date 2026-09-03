"""The `graf` launcher — what `uvx --from git+https://github.com/nabooai/grafcli graf` runs.

The CLI itself is the Go binary in this repository. This shim exists so the CLI can be run
from any machine with `uv` and nothing else: it locates a `graf` binary for this platform and
execs it with the arguments untouched. Resolution order:

1. ``$GRAF_BIN`` — an explicit binary (a local `make build`, a pinned download).
2. The cache: ``~/.graf/bin/graf-<version>`` (``$GRAF_CONFIG_DIR`` moves ``~/.graf``).
3. The GitHub release ``v<version>`` of nabooai/grafcli: the archive goreleaser publishes for
   this OS/arch. The repository is private, so a GitHub token is needed — ``$GH_TOKEN`` /
   ``$GITHUB_TOKEN``, or ``gh auth token`` when the gh CLI is logged in.
4. ``go build`` from the sources shipped inside this wheel, when a Go toolchain is on PATH.

Everything the shim prints goes to stderr; stdout belongs to the binary, so a caller that pipes
`graf` sees exactly what the Go CLI would have written.
"""

from __future__ import annotations

import io
import json
import os
import platform
import shutil
import stat
import subprocess
import sys
import tarfile
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

REPO = "nabooai/grafcli"
API = "https://api.github.com"


def _version() -> str:
    try:
        from importlib.metadata import version

        return version("grafcli")
    except Exception:  # noqa: BLE001 — a source checkout run without install
        return "dev"


def _log(msg: str) -> None:
    print(f"graf launcher: {msg}", file=sys.stderr)


def _platform() -> tuple[str, str]:
    system = platform.system().lower()
    goos = {"linux": "linux", "darwin": "darwin", "windows": "windows"}.get(system)
    machine = platform.machine().lower()
    goarch = {
        "x86_64": "amd64",
        "amd64": "amd64",
        "aarch64": "arm64",
        "arm64": "arm64",
    }.get(machine)
    if not goos or not goarch:
        raise SystemExit(f"graf launcher: no build for {system}/{machine}")
    return goos, goarch


def _cache_dir() -> Path:
    base = os.environ.get("GRAF_CONFIG_DIR") or str(Path.home() / ".graf")
    d = Path(base) / "bin"
    d.mkdir(parents=True, exist_ok=True)
    return d


def _github_token() -> str:
    for name in ("GH_TOKEN", "GITHUB_TOKEN"):
        if os.environ.get(name, "").strip():
            return os.environ[name].strip()
    gh = shutil.which("gh")
    if gh:
        try:
            out = subprocess.run([gh, "auth", "token"], capture_output=True, text=True, timeout=10)
            if out.returncode == 0 and out.stdout.strip():
                return out.stdout.strip()
        except (OSError, subprocess.SubprocessError):
            pass
    return ""


def _download(version: str, goos: str, goarch: str, dest: Path) -> bool:
    """Fetch the release asset for this platform into `dest`. False when it cannot (no token,
    no such release, no asset) — the caller falls through to a build."""
    token = _github_token()
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "grafcli-launcher"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    ext = "zip" if goos == "windows" else "tar.gz"
    asset_name = f"grafcli_{version}_{goos}_{goarch}.{ext}"
    try:
        req = urllib.request.Request(f"{API}/repos/{REPO}/releases/tags/v{version}", headers=headers)
        with urllib.request.urlopen(req, timeout=20) as resp:
            release = json.load(resp)
        asset = next((a for a in release.get("assets", []) if a.get("name") == asset_name), None)
        if asset is None:
            _log(f"release v{version} has no asset {asset_name}")
            return False
        dl_headers = dict(headers, Accept="application/octet-stream")
        req = urllib.request.Request(asset["url"], headers=dl_headers)
        with urllib.request.urlopen(req, timeout=120) as resp:
            blob = resp.read()
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError) as e:
        _log(f"could not download release v{version} ({e})")
        return False
    member = "graf.exe" if goos == "windows" else "graf"
    if ext == "zip":
        with zipfile.ZipFile(io.BytesIO(blob)) as z:
            data = z.read(member)
    else:
        with tarfile.open(fileobj=io.BytesIO(blob), mode="r:gz") as t:
            f = t.extractfile(member)
            if f is None:
                _log(f"archive {asset_name} carries no {member}")
                return False
            data = f.read()
    tmp = dest.with_suffix(dest.suffix + ".part")
    tmp.write_bytes(data)
    tmp.chmod(tmp.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    tmp.replace(dest)
    _log(f"downloaded {asset_name} → {dest}")
    return True


def _build(version: str, dest: Path) -> bool:
    go = shutil.which("go")
    src = Path(__file__).resolve().parent / "_src"
    if not go or not (src / "go.mod").exists():
        return False
    _log(f"building from source with {go} (this happens once)")
    ldflags = f"-s -w -X github.com/nabooai/grafcli/cmd.Version=v{version}"
    env = dict(os.environ, CGO_ENABLED="0")
    proc = subprocess.run(
        [go, "build", "-ldflags", ldflags, "-o", str(dest), "."],
        cwd=str(src),
        env=env,
        stdout=sys.stderr,
        stderr=sys.stderr,
    )
    return proc.returncode == 0 and dest.exists()


def resolve_binary() -> Path:
    explicit = os.environ.get("GRAF_BIN", "").strip()
    if explicit:
        p = Path(explicit)
        if not p.exists():
            raise SystemExit(f"graf launcher: $GRAF_BIN={explicit} does not exist")
        return p
    version = _version()
    goos, goarch = _platform()
    dest = _cache_dir() / (f"graf-{version}" + (".exe" if goos == "windows" else ""))
    if dest.exists():
        return dest
    if _download(version, goos, goarch, dest):
        return dest
    if _build(version, dest):
        return dest
    raise SystemExit(
        "graf launcher: could not obtain a graf binary. Either log in to GitHub (`gh auth login`, "
        "or set GH_TOKEN) so the release can be downloaded, install Go so it can be built, or "
        "point $GRAF_BIN at a binary."
    )


def main() -> None:
    binary = resolve_binary()
    argv = [str(binary), *sys.argv[1:]]
    if os.name == "nt":
        raise SystemExit(subprocess.call(argv))
    os.execv(str(binary), argv)
