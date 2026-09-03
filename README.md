# graf CLI

Query your organization's data graph — Slack, GitHub, Jira, Confluence, Monday,
CSA sessions and the rest, joined into one GraphQL graph — from the command
line.

```console
$ graf ask "latest csa sessions by navnav"
Here are the 3 most recent CSA sessions for Nave Ben Naim (nave@naboo.ai)...

$ graf query '{ csa { sessionCount } }'
{
  "csa": {
    "sessionCount": 3020
  }
}
```

Built to be driven by coding agents as much as by people: stdout carries only
the payload, every failure has a documented exit code, and nothing ever blocks
on a prompt. See [AGENTS.md](AGENTS.md) for the agent-facing guide.

## Install

Run it from anywhere with `uv` — no Go toolchain, no clone:

```sh
uvx --from git+https://github.com/nabooai/grafcli graf ask "what shipped last week?"
uv tool install git+https://github.com/nabooai/grafcli      # puts `graf` on PATH
```

`uvx` installs a tiny Python launcher that fetches the release binary for your
OS/arch into `~/.graf/bin/` on first run (the repository is private, so it
needs a GitHub token: `gh auth login`, or `GH_TOKEN`) and execs it; when no
release is reachable it builds from the sources shipped inside the wheel,
given a Go toolchain on PATH. `GRAF_BIN=/path/to/graf` skips all of that.

Or build from source (Go 1.22+):

```sh
make build     # ./bin/graf
make install   # $GOPATH/bin/graf
```

## Authenticate

Graf sits behind Cloudflare Access, so authenticating means holding an Access
**service token** — a client id and secret sent as `CF-Access-Client-Id` and
`CF-Access-Client-Secret`. There is no separate application login: past the
Access edge, the API trusts the request.

The fastest path is a `.env` in your working directory, which is what a service
token is usually handed to you as:

```sh
CLOUDFLARE_ACCESS_CLIENT_ID=...
CLOUDFLARE_ACCESS_CLIENT_SECRET=...
```

`graf` reads it from the working directory or any parent (up to six levels),
and never overwrites a variable that is already exported. To store the token
instead:

```sh
graf auth login --with-token < token.txt   # two lines, "id:secret", or KEY=VALUE
graf auth status                           # who am I, and does it work?
```

Credentials are never accepted as command-line arguments — argv is readable by
every other process on the machine.

The harness commands (`steer`, `explore`, `run-query`, `harness`) additionally
carry the deployment's **own** bearer token when it sets one (`GRAF_API_TOKEN`
on the server). Store it the same way — it is merged with whatever is already
stored, so the two halves can be added separately:

```sh
echo "GRAF_API_TOKEN=..." | graf auth login --with-token
```

A deployment on this machine (`--url http://127.0.0.1:8007`) needs no
credential at all.

| Variable | Holds |
|---|---|
| `GRAF_CF_ACCESS_CLIENT_ID` / `..._SECRET` | the service token |
| `GRAF_API_TOKEN` | the deployment's own bearer token (its `/api/cli` gate) |
| `CLOUDFLARE_ACCESS_CLIENT_ID` / `..._SECRET` | the same, under Cloudflare's own spelling |
| `GRAF_CF_AUTHORIZATION` | a browser-issued `CF_Authorization` JWT, for a one-off |
| `GRAF_URL` | the deployment to talk to |
| `GRAF_CONFIG_DIR` | where `~/.graf` lives |
| `GRAF_NO_DOTENV` | set to disable `.env` discovery entirely |

## Files

| Path | Holds |
|---|---|
| `~/.graf/config.json` | settings (`base_url`, `model`, `reasoning`, `fda_version`) |
| `~/.graf/.credentials.json` | credentials (service token and/or API token), mode `0600` |
| `~/.graf/bin/` | binaries the `uvx` launcher fetched or built |

`config set` rewrites only its own keys and leaves everything else in the file
untouched; it refuses outright if the file cannot be parsed, rather than
replacing it. `graf doctor` checks the directory's permissions — it should be
`0700`.

## Two ways to ask

The CLI has a model-driven half and a deterministic half, and the useful
workflow runs them in that order.

**`graf ask`** puts a question to the agent, which finds the source, writes the
GraphQL, runs it and reports the rows:

```sh
graf ask "which repos did nave touch last week?"
graf ask "how many open PRs are there?" --json
echo "why did the ETL job fail?" | graf ask -
```

**`graf query`** runs GraphQL directly, with no model in the loop — same query,
same rows, no tokens spent:

```sh
graf query '{ csa { sessionCount } }'
graf query - < query.graphql
graf schema --grep CsaSession        # what can I filter on?
```

`ask --json` reports the queries the agent wrote under `.queries`, so once it
has found the right one you never need to pay for it again:

```sh
graf ask "how many jira issues?" --json | jq -r .queries[0] | graf query -
```

Read the warnings `graf query` prints to stderr. A capped or truncated result
means the rows are a **subset**, and presenting that as the whole answer is a
wrong answer, not a shortcut.

## The harness, one stage at a time

`graf ask` streams a conversation with the agent. The same answering pipeline
is also exposed stage by stage, against the server's `/api/cli` endpoints —
which is how you find out *why* an answer came out the way it did, and how a
script reuses just the stage it needs:

```sh
graf steer "what is new with saki?"          # what the agent is TOLD before it picks a tool (free)
graf explore "open PRs in the api repo"      # its explore_schema tool: ranked, validated query options
graf run-query '{ github { listPullRequestsCount } }'   # its run_query tool: the envelope the agent reads (free)
graf harness "how many open PRs are there?"  # the whole pipeline single-shot, with receipts
```

`steer` prints one block per steer — a pasted URL resolved to the entity it
names, a loose word matched to the stored values it could mean:

```
## name_hits
Name hits — real stored values. `Type(field: "value")` is a copyable filter; `@root` serves it…
saki: no stored value is spelled that — likely a typo for:
  CustomerChannel(key: "SKAI::production-skai") @customerChannels …
```

`run-query` differs from `query` in that it goes through the agent's tool: the
envelope carries `warnings`, `generation` (which snapshot answered), pre-resolved
`<field>__resolved` siblings for reference ids, and whole-row truncation. A
rejected query prints the tool's error text — which names the valid fields —
on stderr with exit 14.

`harness --json` returns `{answer, ungrounded, steers, tools, queries}`: the
answer plus every receipt that produced it. It is stateless (no conversation is
kept) and bounded by `--max-turns` and the server's timeout.

## Conversations

Each `ask` opens a new conversation unless `--chat` continues one:

```sh
ID=$(graf ask "summarize the incident" --json | jq -r .conversation_id)
graf ask "who was on call?" --chat "$ID"

graf chats list --limit 10
graf chats show "$ID"
graf chats rm "$ID" --yes
```

## Watch the agent work

`--events` draws the agent's activity on stderr as it happens:

```
  ⟳ Connecting  graf.nissimtech.com

  ✦ New conversation  a99ccd13-ba00-40ea-be35-2a763552f7ae

  ⊕ Name hits
    navnav:
    _persons(Nave Ben Naim <nave@naboo.ai>) [matched "navnav"/"NAVNAV221" · 15 accounts]
    SlackChannel(monday-test-with-navnav) [name]

  ▸ explore_schema        latest 3 csa sessions
  ! explore_schema        degraded: index_missing_or_stale  4s

  ◆ Thinking
    I'm refining how we filter CSA sessions, targeting sessions where the
    user matches `nave@naboo.ai`…

  ▸ run_query             { csa { session( filter: { user: { eq: "nave@nab…
  ! run_query             list returned a FULL page of 3 rows ordered by `s…  0s

  ◆ Answer

Here are the 3 most recent CSA sessions for Nave Ben Naim…

  ┌────────────────────────────────┐
  │ turns    3                     │
  │ queries  1                     │
  │ tokens   15,417 in / 1,657 out │
  │ cost     $0.0381               │
  │ elapsed  14.5s                 │
  └────────────────────────────────┘
```

`⊕ Name hits` is the grounding the server injects before the agent sees your
question — the real stored values it matched your words against. It is shown
because an answer that looks wrong is usually explained there.

`!` rather than `✓` marks a tool whose result carried a warning, a degraded
index, or an error. Those are the results worth reading.

Two step kinds are hidden by default and have their own flags: `--show-input`
expands the full model input (identical every turn, thousands of tokens), and
`--steps` reveals step kinds this build does not have a dedicated line for.

The view goes to stderr, so `graf ask … --events > answer.txt` still writes a
clean answer file. Color is disabled automatically when stderr is not a
terminal, and honors `NO_COLOR`.

Record a session and re-render it offline — the recording is replayed through
the same parser and renderer the live path uses:

```sh
graf ask "what changed?" --events --record run.sse
graf replay run.sse
graf replay run.sse --realtime    # paced, rather than instant
```

## Inspect the deployment

```sh
graf sources                    # the data sources wired into the graph
graf sources secrets            # which credential names the vault holds
graf schema                     # the full GraphQL SDL (v2 — what `query` serves)
graf config list --explain      # where each setting came from
graf doctor                     # connectivity, credentials, schema, graph
```

Precedence is flag > environment > config file > default.

## Exit codes

```
0 ok    2 usage    3 not-found    4 unauthenticated    5 forbidden
7 invalid-input    8 rate-limited    9 timeout    10 server
13 agent-failed    14 graphql-error
```

Only `8`, `9`, `10` and `13` are worth retrying — and 13 rewards rephrasing the
question more than repeating it. `14` means the graph rejected your query, so
retrying it unchanged cannot help. Run `graf exit-codes` for the full contract,
and `graf manifest` for a machine-readable command tree.

## Develop

```sh
make test      # unit tests, including SSE fixtures under testdata/
make lint      # go vet + gofmt
make snapshot  # cross-compile all platforms via goreleaser
```
