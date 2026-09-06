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
