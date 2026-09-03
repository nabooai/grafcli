# graf — agent guide

`graf` queries a company's systems joined into one GraphQL graph. You are
expected to drive it unattended, so it never prompts, never blocks, and never
writes anything but the payload to stdout.

Run `graf manifest` for the machine-readable command tree and exit-code table.
It needs no credential and makes no network request.

## Pick the right half

| You want | Use | Cost |
|---|---|---|
| A question answered from data you cannot address yet | `graf ask` | tokens + seconds |
| The same, single-shot, with receipts (`--json`: steers, tools, queries) | `graf harness` | tokens + seconds |
| To know what the agent is TOLD about a question (URL facts, name hits) | `graf steer` | one HTTP request |
| Ranked, validated query options for an intent (the agent's `explore_schema`) | `graf explore` | tokens |
| Rows through the agent's `run_query` tool (warnings, resolved ids, snapshot) | `graf run-query` | one HTTP request |
| Rows from a query you already know, bare | `graf query` | one HTTP request |

When an answer looks wrong, run `graf steer` on the question first: the steers
are the real stored values the agent was pointed at, and a typo or an
ambiguous name shows up there. `graf explore` then shows the query options it
chose between. Both are what a person would look at before blaming the model.

Prefer `graf query`. Use `graf ask` to *discover* the query, then reuse it:

```sh
graf ask "how many jira issues?" --json | jq -r .queries[0] > q.graphql
graf query - < q.graphql            # from now on, free and deterministic
```

`ask --json` returns `{answer, conversation_id, queries, usage}`. `queries` is
the GraphQL the agent actually ran — the most reusable thing a turn produces.

## Reading `graf query`

- stdout is the `data` object. `--raw` gives the full envelope.
- **Warnings go to stderr and must be read.** "capped", "truncated" or "a FULL
  page … MORE exist" means the rows are a SUBSET. Page with `first:`/`offset:`
  or use the `<root>Count` / `<root>GroupBy` roots. Reporting a capped page as
  the complete list is a wrong answer.
- Exit 14 means the graph rejected the query. The message names the valid
  fields — read it and fix the query. Do not retry unchanged.
- `graf schema --grep <Type>` prints just that type's block, which is far
  cheaper than the whole SDL (~2 MB). The default is the v2 schema, which is
  the one `graf query` serves; `--v1` is a different graph and will not match.

## Rules that will bite you

- **Never pass a credential on argv.** There is no `--token` flag. Use the
  environment, a `.env`, or `graf auth login --with-token` reading stdin.
  The harness commands need the deployment's own bearer token too when it
  sets one: `GRAF_API_TOKEN` (env, `.env`, or stored the same way). A
  loopback `--url` needs no credential.
- **`run-query` exit 14 carries the tool's error text.** It names the valid
  fields; fix the query, do not retry it unchanged. Exit 10 from `harness`
  after a long wait is usually the server's turn cap — raise `--max-turns`
  or narrow the question.
- **A question is one argument.** `graf ask "why did X fail?"` — quote it, or
  pipe it with `graf ask -`.
- **Do not pipe when you can flag.** `--json`, `--limit` and `--grep` exist so
  you do not need `| head`, which breaks permission prefix-matching.
- **`graf chats rm` refuses without `--yes`** when stdin is not a terminal.
  That is deliberate; deletion has no undo.
- Everything under `ask`, `query`, `schema`, `chats list/show`, `sources`,
  `config get/list`, `doctor`, `version` and `manifest` is read-only. The
  commands that change state are `chats rm`, `chats rename`, `config set`,
  `auth login` and `auth logout`.

## Exit codes

```
0  ok               retry: no
2  usage            retry: no    — malformed invocation, fix the command
3  not_found        retry: no    — no such conversation
4  unauthenticated  retry: no    — no usable Access credential; run `graf auth status`
5  forbidden        retry: no    — credential valid, not authorized here
7  invalid_input    retry: no    — the server rejected the body
8  rate_limited     retry: YES   — honor retry_after
9  timeout          retry: YES   — or raise --timeout
10 server           retry: YES
13 agent_failed     retry: YES   — rephrase; repeating verbatim rarely helps
14 graphql          retry: no    — fix the query, the error names valid fields
```

Every failure also writes a JSON envelope as the last line of stderr:

```json
{"error":{"kind":"usage","exit_code":2,"detail":"...","hint":"...","fix":"graf ask \"...\"","retryable":false}}
```

`fix`, when present, is a complete command you can run verbatim.

## Reference

```sh
graf ask <question> [--chat ID] [--json] [--events] [--record F] [--plain]
graf query <graphql|-> [--raw] [--compact]
graf schema [--grep S] [--v1]
graf chats list|show|new|rename|rm
graf sources [--json] | graf sources secrets
graf config list|get|set [--explain]
graf auth login --with-token | status | logout
graf doctor | replay <file> | manifest | exit-codes | version
```

Global: `--url`, `--model`, `--reasoning`, `--fda-version`, `--timeout`,
`--debug`, `--no-input`, `--env-file`.
