# grafcli — releases of the `graf` CLI

`graf` queries your organization's data graph — Slack, GitHub, Jira, Confluence, Monday and the
rest, joined into one GraphQL graph — from the command line, and exposes the answering harness
one stage at a time (`steer` / `explore` / `run-query` / `harness`).

**The source lives in [`nabooai/graf`](https://github.com/nabooai/graf) under `cli/`** (with the
full README and the agent guide, `AGENTS.md`). This repository holds only:

- the **releases** — one archive per OS/arch plus `checksums.txt`, published by the graf repo's
  `cli` GitHub Action when a `cli-v<semver>` tag is pushed there;
- the **`uvx` launcher** (`grafcli_launcher`), so the CLI runs from any machine with `uv`.

## Install

```sh
uvx --from git+https://github.com/nabooai/grafcli graf ask "what shipped last week?"
uv tool install git+https://github.com/nabooai/grafcli      # puts `graf` on PATH
```

The launcher fetches the latest release's binary for your OS/arch into `~/.naboo/bin/graf` on
first run (the repository is private, so it needs a GitHub token: `gh auth login`, or
`GH_TOKEN`) and execs it. From then on the binary keeps itself current (`graf update`, and a
background check once a day). `GRAF_BIN=/path/to/graf` points the launcher at a local build.

Or download an archive from the Releases page and put `graf` on your PATH.

## Configure it: `~/.naboo/config.json`

Settings live in **`~/.naboo/config.json`** (`GRAF_CONFIG_DIR` moves the directory; the
pre-0.2 `~/.graf/config.json` is still read as a fallback, never written). Precedence for every
setting is **flag > environment > config file > default**.

```json
{
  "base_url": "https://graf.nissimtech.com",
  "model": "gemini/gemini-3.8-flash",
  "reasoning": "low",
  "fda_version": 14,
  "auto_update": true,
  "headers": {
    "CF-Access-Client-Id": "…",
    "CF-Access-Client-Secret": "…"
  }
}
```

| Key | Env / flag | Meaning |
|---|---|---|
| `base_url` | `GRAF_URL` / `--url` | the deployment to talk to (default `https://graf.nissimtech.com`). A loopback URL (`http://127.0.0.1:8007`) needs no credential. |
| `model` | `GRAF_MODEL` / `--model` | model for agent turns (`ask`, `harness`); default: the server's |
| `reasoning` | `GRAF_REASONING` / `--reasoning` | reasoning effort for agent turns; default: the server's |
| `fda_version` | `--fda-version` | agent version `ask` runs (default 14, the answering agent) |
| `auto_update` | `GRAF_NO_UPDATE=1` to disable | the once-a-day background self-update (default on) |
| `headers` | config file only | a map of extra headers sent on EVERY request — the home for the Cloudflare Access service token (`CF-Access-Client-Id` / `CF-Access-Client-Secret`) or a proxy's auth header. `graf config set headers.<Name> <value>`, `config unset headers.<Name>`; `config list` redacts the values, `config get headers.<Name>` prints one whole. With the service token here, no `auth login` is needed. |

Edit it with the CLI rather than by hand — `config set` rewrites only its own keys and refuses
to touch a file it cannot parse:

```sh
graf config set base_url https://graf.nissimtech.com
graf config set headers.CF-Access-Client-Id "$ID"
graf config set headers.CF-Access-Client-Secret "$SECRET"
graf config set auto_update false
graf config list --explain      # every setting and where it came from
graf config get model
```

**Credentials are NOT in this file.** They live in `~/.naboo/.credentials.json` (mode `0600`),
written by `graf auth login --with-token` from stdin — never from argv:

```sh
# the deployment's bearer: an API key minted in the web UI (Security → API keys),
# or the deployment-wide GRAF_API_TOKEN
echo "GRAF_API_TOKEN=graf_…" | graf auth login --with-token

# the Cloudflare Access service token in front of the public deployment (two lines, "id:secret",
# or KEY=VALUE lines from a .env) — merged with what is already stored
graf auth login --with-token < token.txt
graf auth status                          # what is in effect, and does it work
```

The same values are honoured from the environment or a `.env` in the working directory (or any
parent, up to six levels): `GRAF_API_TOKEN`, `GRAF_CF_ACCESS_CLIENT_ID` / `GRAF_CF_ACCESS_CLIENT_SECRET`
(also under Cloudflare's own `CLOUDFLARE_ACCESS_CLIENT_ID` / `..._SECRET`), `GRAF_CF_AUTHORIZATION`
for a one-off browser JWT. `GRAF_NO_DOTENV=1` turns `.env` discovery off.

`~/.naboo/` also holds `bin/graf` (the installed binary) and `update.json` (the last update
check). `graf doctor` checks the directory's permissions, connectivity, credentials and the graph.

This repository is private: the launcher and the self-updater download releases with your GitHub
credentials (`gh auth login`, or `GH_TOKEN` / `GITHUB_TOKEN`).
