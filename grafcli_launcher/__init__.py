"""The `graf` launcher — what `uvx --from git+https://github.com/nabooai/grafcli graf` runs.

The CLI itself is the Go binary whose SOURCE lives in nabooai/graf under `cli/`; this repository
holds its releases and this shim. The shim exists so the CLI can be run from any machine with `uv`
and nothing else: it locates a `graf` binary for this platform and execs it with the arguments
untouched. Resolution order:

1. ``$GRAF_BIN`` — an explicit binary (a local `make build`, a pinned download).
2. The installed binary: ``~/.naboo/bin/graf`` (``$GRAF_CONFIG_DIR`` moves ``~/.naboo``). Once
   it exists the CLI keeps ITSELF current (``graf update``, run in the background at most once
   a day), so this launcher's own version only matters for the first install.
3. The LATEST GitHub release of nabooai/grafcli: the archive the graf repo's `cli` workflow
   publishes for this OS/arch. The repository is private, so a GitHub token is needed —
   ``$GH_TOKEN`` / ``$GITHUB_TOKEN``, or ``gh auth token`` when the gh CLI is logged in.

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
    base = os.environ.get("GRAF_CONFIG_DIR") or str(Path.home() / ".naboo")
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


def _download(goos: str, goarch: str, dest: Path) -> bool:
    """Fetch the latest release's asset for this platform into `dest`. False when it cannot (no
    token, no release, no asset)."""
    token = _github_token()
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "grafcli-launcher"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    ext = "zip" if goos == "windows" else "tar.gz"
    try:
        req = urllib.request.Request(f"{API}/repos/{REPO}/releases/latest", headers=headers)
        with urllib.request.urlopen(req, timeout=20) as resp:
            release = json.load(resp)
        version = str(release.get("tag_name", "")).lstrip("v")
        asset_name = f"grafcli_{version}_{goos}_{goarch}.{ext}"
        asset = next((a for a in release.get("assets", []) if a.get("name") == asset_name), None)
        if asset is None:
            _log(f"release v{version} has no asset {asset_name}")
            return False
        dl_headers = dict(headers, Accept="application/octet-stream")
        req = urllib.request.Request(asset["url"], headers=dl_headers)
        with urllib.request.urlopen(req, timeout=120) as resp:
            blob = resp.read()
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError) as e:
        _log(f"could not download the latest release ({e})")
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


def resolve_binary() -> Path:
    explicit = os.environ.get("GRAF_BIN", "").strip()
    if explicit:
        p = Path(explicit)
        if not p.exists():
            raise SystemExit(f"graf launcher: $GRAF_BIN={explicit} does not exist")
        return p
    goos, goarch = _platform()
    dest = _cache_dir() / ("graf.exe" if goos == "windows" else "graf")
    if dest.exists():
        return dest
    if _download(goos, goarch, dest):
        return dest
    raise SystemExit(
        "graf launcher: could not obtain a graf binary. Log in to GitHub (`gh auth login`, or set "
        "GH_TOKEN) so the release can be downloaded, or point $GRAF_BIN at a binary "
        "(source: nabooai/graf, cli/ — `make build`)."
    )


def main() -> None:
    binary = resolve_binary()
    argv = [str(binary), *sys.argv[1:]]
    if os.name == "nt":
        raise SystemExit(subprocess.call(argv))
    os.execv(str(binary), argv)
