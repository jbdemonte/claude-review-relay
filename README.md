# Claude Review Relay

Claude Review Relay lets Codex ask Claude Code for an independent, read-only
review of a local Git change. It is a focused Go MCP server for the workflow:

1. Codex implements and tests a change.
2. Claude independently reviews the server-computed diff.
3. Codex evaluates the findings and fixes only confirmed problems.
4. Claude resumes the exact same conversation and verifies the corrections.

The server keeps a durable association between its `review_id` and Claude
Code's explicit `session_id`. Every follow-up uses
`claude -p --resume <session_id>`. It never uses the ambiguous
`claude --continue` command.

The repository is named `claude-review-relay`. The installed binary, MCP
server identifier, tool prefix, configuration directory, and persisted data
paths retain the technical name `claude-reviewer` for compatibility.

## Why This Project Exists

Claude Code already provides useful building blocks:

- [`claude -p`](https://code.claude.com/docs/en/cli-usage) runs a
  non-interactive prompt and can return JSON;
- the same CLI can resume a specific session with `--resume`, while
  `--continue` selects the most recent conversation in the current directory;
- [`claude mcp serve`](https://code.claude.com/docs/en/mcp) makes
  general Claude Code tools available to an MCP client;
- local `/code-review` gives quick feedback on a working diff, while
  `/review <pr>` reviews a pull request;
- [Ultrareview](https://code.claude.com/docs/en/ultrareview) runs a deeper,
  independently verified multi-agent review in a remote cloud sandbox and can
  include staged and uncommitted changes;
- [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)
  can review pull requests in GitHub workflows.

Those capabilities remain appropriate for interactive work, general-purpose
Claude delegation, quick local feedback, deep cloud review, and hosted
pull-request automation. This project covers a different integration problem:
making Claude a repeatable second reviewer inside a Codex implementation loop.
It is orchestration and safety plumbing, not a claim to outperform
Ultrareview's multi-agent bug-finding depth.

| Approach                       | Best suited for                                        | What the caller still manages                                                           |
|--------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------------------------|
| `claude -p`                    | One-off scripts and prompts                            | Git diff construction, prompt policy, schemas, session IDs, errors, and persistence     |
| `claude mcp serve`             | General Claude Code tool access                        | Review-specific scope, lifecycle, durable review identity, and verification workflow    |
| `/code-review` or `/review`    | Quick interactive diff or PR feedback in Claude Code   | Codex integration and a durable correction-verification workflow                        |
| Ultrareview                    | Deep, remotely sandboxed, multi-agent pre-merge review | Codex integration, local MCP lifecycle, and same-session verification after Codex fixes |
| Claude GitHub review workflows | Pull requests hosted on GitHub                         | Local uncommitted work and the Codex-side correction loop                               |
| Claude Review Relay            | Local Codex cross-review                               | Codex evaluates findings and remains responsible for all file modifications             |

Claude Review Relay adds the following review-specific behavior:

- Codex-native MCP tools with typed inputs and outputs;
- local review of tracked uncommitted work, staged work, or changes since a Git
  reference, without requiring a pull request;
- literal include and exclude path scopes computed by the server;
- a strict read-only Claude tool policy;
- structured findings validated against a JSON Schema;
- durable review metadata and explicit-session continuation;
- asynchronous reviews that can run longer than a synchronous MCP connector
  deadline;
- safe error details, redaction, output limits, process cancellation, and
  cross-process storage locking.

Choose Ultrareview when independently reproduced multi-agent findings and a
cloud task that survives terminal closure are the priority. Choose this MCP
server when the priority is an automated Codex policy: narrow a local diff,
invoke a read-only reviewer, persist an explicit review identity, let Codex fix
confirmed findings, and ask the same Claude conversation to verify those fixes.
The two approaches can also be used together for high-risk changes.

This is defense in depth, not a security boundary. Secret detection can miss
unknown formats, and any reviewed content is sent to the configured Claude Code
service.

## What Can Be Reviewed

The server always compares one `base_ref` with the repository's current working
tree. `base_ref` defaults to `HEAD`.

### Uncommitted tracked changes

Use `base_ref: "HEAD"`. The review includes both staged and unstaged changes to
tracked files. Untracked files are reported by repository-relative name, but
their contents are not sent automatically.

Typical tool arguments:

```json
{
  "repository_path": "/absolute/path/to/repository",
  "goal": "Review the current uncommitted implementation.",
  "base_ref": "HEAD",
  "include_paths": ["internal", "cmd", "README.md"],
  "max_turns": 20,
  "timeout_seconds": 1200
}
```

### Everything changed since a commit or branch

Set `base_ref` to a commit SHA, tag, or branch such as `origin/main`. The review
covers the difference from that reference through the current `HEAD`, plus any
current tracked working-tree changes.

```json
{
  "repository_path": "/absolute/path/to/repository",
  "goal": "Review all changes introduced since the main branch.",
  "base_ref": "origin/main",
  "include_paths": ["internal", "cmd"]
}
```

### One exact commit

The API does not accept a separate `to_ref`: its right-hand side is always the
current working tree. To review one exact non-merge commit, use a clean checkout
or temporary Git worktree at that commit and set `base_ref` to its parent:

```text
current checkout:  <commit-sha>
base_ref:          <commit-sha>^
```

For a merge commit, select the intended parent explicitly, for example
`<commit-sha>^1`. Any additional working-tree changes at that checkout are also
part of the diff, so keep it clean when exact commit isolation matters.

### Path-scoped changes

`include_paths` and `exclude_paths` contain literal repository-relative files
or directories. They apply to both the tracked diff and the untracked filename
list. Use a narrow scope for faster, more focused reviews.

## How It Works

```text
Codex
  |
  | start_review(repository, base_ref, scope, goal, test results)
  v
Claude Review Relay
  |-- validates the repository and literal path scope
  |-- computes and sanitizes the Git diff locally
  |-- persists the pending review record
  |-- starts a read-only Claude worker
  `-- returns pending immediately
          |
          | captures and persists Claude's explicit session_id
          | get_review_status(review_id)
          v
      structured verdict and findings
          |
          | Codex confirms and fixes valid findings
          v
  start_continue_review(same review_id, refresh_diff: true)
          |
          `-- claude -p --resume <same-session-id>
```

The MCP server is a local STDIO process started by Codex, not a permanent macOS
daemon. It normally remains alive for the lifetime of its Codex client. If the
server shuts down during a review, it cancels the Claude subprocess and stores
the review as `interrupted` when a resumable Claude session ID was captured, or
`failed` otherwise.

Long reviews use background workers so the initial MCP call returns in a few
seconds even when Claude needs several minutes. Polling reads the persisted
state; it does not restart Claude or create a new conversation.

## Core Guarantees

- MCP over STDIO, with stdout reserved for the protocol and JSON logs on stderr;
- MCP annotations identify metadata tools as read-only and `close_review` as
  destructive so Codex can apply its configured approval behavior;
- the server, not the model, computes the Git diff;
- Git commands use separate arguments, not a shell;
- Claude receives only `Read`, `Glob`, and `Grep`; editing, Bash, Web, and MCP
  tools are explicitly denied;
- prompts and diffs are sent through stdin rather than command-line arguments;
- responses are constrained by JSON Schema and validated again in Go;
- session records use atomic writes, `0600` permissions, and advisory file locks;
- per-review OS-backed leases prevent concurrent continuations of one review;
- complete private keys reject the request, sensitive filenames are excluded,
  and common token forms are redacted;
- a diff larger than the configured limit fails explicitly and is never silently
  truncated;
- resumed output must report the same Claude session ID as the requested one.

## Prerequisites

- macOS on Apple Silicon or Intel;
- Go 1.25 or newer;
- Git;
- Claude Code installed and authenticated with `claude auth login`;
- Codex CLI or another MCP client with STDIO support.

This guide and the production smoke test were validated with Claude Code
2.1.216. `claude-reviewer doctor` checks the installed CLI's authentication and
required flags directly instead of relying only on a version number.

## Build and Test

```bash
go build -o ./bin/claude-reviewer ./cmd/claude-reviewer
go test ./...
go vet ./...
```

Or run:

```bash
make check
```

## Install on macOS

Install the binary atomically in the current user's local bin directory:

```bash
mkdir -p "$HOME/.local/bin"
cp ./bin/claude-reviewer "$HOME/.local/bin/claude-reviewer.new"
chmod +x "$HOME/.local/bin/claude-reviewer.new"
mv -f "$HOME/.local/bin/claude-reviewer.new" "$HOME/.local/bin/claude-reviewer"
```

Add the server to Codex with its absolute executable path:

```bash
codex mcp add claude-reviewer -- "$HOME/.local/bin/claude-reviewer" serve
codex mcp list
```

Minimal equivalent Codex server configuration, replacing the username:

```toml
[mcp_servers.claude-reviewer]
command = "/Users/USERNAME/.local/bin/claude-reviewer"
args = ["serve"]
```

Codex also supports optional per-tool approval overrides. To pre-approve the
three tools that start or finalize review state, extend the server configuration
with:

```toml
[mcp_servers.claude-reviewer.tools.start_review]
approval_mode = "approve"

[mcp_servers.claude-reviewer.tools.start_continue_review]
approval_mode = "approve"

[mcp_servers.claude-reviewer.tools.close_review]
approval_mode = "approve"
```

The supported values are `auto`, `prompt`, `writes`, and `approve`; see the
[official Codex manual's MCP configuration section](https://developers.openai.com/codex/codex-manual.md#model-context-protocol).

Do not use `$HOME` literally in the TOML `command`; shell expansion is not
guaranteed there. Restart every running Codex client after replacing the binary.
An already running MCP process continues using the executable version it loaded
at startup.

Either add `$HOME/.local/bin` to the shell `PATH`, or use the absolute commands
shown below.

## Diagnose the Installation

```bash
"$HOME/.local/bin/claude-reviewer" doctor
```

The JSON report checks the Claude executable and version, authentication,
required flags, Git, data-directory access, and session storage.

Run the production review pipeline against an isolated one-line Git diff with:

```bash
"$HOME/.local/bin/claude-reviewer" doctor --review-smoke-test
```

The smoke test calls the configured Claude models. It can incur cost and take
several minutes. The equivalent opt-in Go integration test is:

```bash
CLAUDE_REVIEWER_INTEGRATION=1 go test ./internal/smoke -run TestInstalledClaudeReview
```

## Use It from Codex

Nothing needs to be started manually after MCP registration. Codex starts the
STDIO server when it needs the configured tools. You can ask for a review
directly, for example:

```text
Ask Claude Review Relay to review the current uncommitted changes in internal/
and cmd/. Use maximum effort, analyze every finding, fix confirmed problems,
and ask the same Claude session to verify the corrections.
```

For a committed range:

```text
Ask Claude Review Relay to review everything changed since origin/main. Limit
the scope to internal/session and internal/reviewer, and include the Go test and
vet results in the review context.
```

Normally you do not call the MCP tools or write their JSON arguments yourself:
Codex does that. Add the policy block below to `AGENTS.md` when every non-trivial
change in a project should follow the cross-review workflow automatically.

## Recommended Asynchronous Workflow

1. Call `start_review` with the repository, functional goal, literal file scope,
   and test results.
2. Save the returned `review_id`, `claude_session_id` when present,
   `expected_response_sequence`, and `poll_after_seconds`.
3. Poll `get_review_status` no more frequently than `poll_after_seconds` until
   `status` is no longer `pending`.
4. Independently validate every finding. Claude is a reviewer, not an authority.
5. Apply confirmed corrections and rerun the relevant tests.
6. Call `start_continue_review` with the same `review_id`, a correction summary,
   and `refresh_diff: true`.
7. Poll until the operation is no longer `pending`. Require the expected
   response sequence and the same Claude session ID.
8. Call `close_review` after accepting the final verdict.

Status meanings:

- `pending`: a background operation is active;
- `open`: a validated structured response is available;
- `interrupted`: the operation stopped and the explicit Claude session can be
  resumed;
- `failed`: no resumable Claude session was captured;
- `closed`: the review was explicitly finalized.

The model route remains the strongest configured route:

- primary model: `fable`;
- fallback model: `opus`;

Select the review depth according to the change's risk instead of using maximum
depth for every review:

- small, localized, low-risk changes: `effort: high`, `max_turns: 10`;
- security-sensitive, architectural, concurrent, persistent-data,
  authentication, payment, deployment, recovery, or otherwise high-risk
  changes: `effort: max`, `max_turns: 20`;
- uncertain classification: use the high-risk profile;
- both profiles: `timeout_seconds: 1200`.

Pass `effort` and `max_turns` explicitly. If `effort` is omitted, the server's
configured fallback remains `max`.

`max_turns` limits Claude's internal agentic turns; it is not a duration.
`timeout_seconds` limits the Claude subprocess. The asynchronous start call does
not wait for that duration.

## Copy-Paste `AGENTS.md` Policy

Place the following block in another project's `AGENTS.md` to make cross-review
part of the Codex workflow:

```markdown
## Cross-Review with Claude

For every non-trivial change:

1. Implement the change.
2. Run the relevant tests, linting, and type checking.
3. Select the review depth according to risk:
   - For a small, localized, low-risk change, use `effort: high` and
     `max_turns: 10`.
   - For security-sensitive, architectural, concurrent, persistent-data,
     authentication, payment, deployment, recovery, or otherwise high-risk
     changes, use `effort: max` and `max_turns: 20`.
   - When uncertain, use the high-risk profile.
4. Call `claude-reviewer.start_review` with the selected `effort` and
   `max_turns`, literal `include_paths` for the files under review, and
   `timeout_seconds: 1200`.
5. Provide a precise goal and the test results.
6. Poll `claude-reviewer.get_review_status` no more frequently than the returned
   `poll_after_seconds` until the status is no longer `pending`.
7. Analyze each finding instead of accepting it blindly.
8. Fix confirmed critical-, high-, and medium-severity findings.
9. Prepare a factual technical response for incorrect findings.
10. Call `claude-reviewer.start_continue_review` with the same `review_id`,
   `refresh_diff: true`, and a request to verify the fixes.
11. Poll until the status is no longer `pending`, then require the returned
    `expected_response_sequence`; report a terminal error instead of polling
    indefinitely if that sequence was not produced.
12. Confirm that the continuation returns the same `claude_session_id`.
13. Stop after two completed review cycles unless a critical issue remains.
14. Call `claude-reviewer.close_review` after the final accepted verdict.
15. Do not treat Claude approval as a substitute for tests.
16. Claude is a read-only reviewer; Codex remains the only agent that modifies
    the repository.
```

The project can add stricter language, test, or commit-attribution rules around
this block. The filename recognized by Codex is `AGENTS.md`.

## Tool Reference

| Tool                    | Behavior                                                              |
|-------------------------|-----------------------------------------------------------------------|
| `start_review`          | Validate, persist, and start a background review; return immediately  |
| `get_review_status`     | Read the latest background status, structured response, or safe error |
| `start_continue_review` | Resume the persisted explicit Claude session in a background worker   |
| `review_diff`           | Synchronous compatibility form of the initial review                  |
| `continue_review`       | Synchronous compatibility form of the continuation                    |
| `get_review`            | Read persisted metadata without contacting Claude                     |
| `list_reviews`          | List review metadata with optional repository and status filters      |
| `close_review`          | Mark a review closed or delete only its local association             |

`review_diff` and `continue_review` remain useful for small, bounded reviews.
They are not recommended for maximum-effort reviews because an MCP client may
stop waiting before Claude finishes. Progress notifications do not guarantee
that a client-side deadline will be extended.

## Session Persistence

Session metadata is stored at:

```text
~/Library/Application Support/claude-reviewer/sessions.json
```

The native Claude conversation remains in Claude Code's own storage. This
project stores the explicit association needed to resume it safely. Restarting
Codex, the MCP server, or the Mac does not intentionally change that mapping.

An `approve` verdict leaves the review `open` so Codex can still request a
correction-verification pass. `close_review` finalizes it explicitly.
`delete_claude_session: true` deletes only the local association; it does not
delete Claude Code's native conversation data.

## Optional Configuration

Create `~/Library/Application Support/claude-reviewer/config.json`:

```json
{
  "claude_binary": "/opt/homebrew/bin/claude",
  "default_model": "fable",
  "default_fallback_model": "opus",
  "default_effort": "max",
  "default_max_turns": 12,
  "timeout_seconds": 240,
  "async_timeout_seconds": 1200,
  "max_diff_bytes": 2097152,
  "max_output_bytes": 8388608,
  "log_level": "info",
  "session_retention_days": 30
}
```

Without an explicit Claude path, resolution tries `PATH`, then the Apple Silicon
and Intel Homebrew locations. `session_retention_days` is reserved for future
explicit cleanup; V1 does not automatically delete review records.

## Errors

Tool errors are actionable JSON with `code`, `message`, and safe `details`.
Claude invocation failures can include a correlation ID, failure stage, exit
code, terminal reason, bounded redacted stderr, model, turn counts, and argument
names without prompt values.

Common codes include:

- request and Git errors: `invalid_repository`, `invalid_base_ref`,
  `invalid_path_scope`, `empty_review_scope`, `diff_too_large`;
- lifecycle errors: `review_not_found`, `review_closed`,
  `review_not_resumable`, `review_busy`, `repository_mismatch`;
- Claude errors: `claude_not_found`, `claude_not_authenticated`,
  `claude_timeout`, `claude_canceled`, `claude_max_turns`, `claude_failed`,
  `claude_session_id_missing`, `invalid_claude_output`,
  `claude_output_too_large`;
- worker and storage errors: `storage_error`, `worker_failed`,
  `background_worker_stopped`, `server_shutting_down`;
- content policy errors: `sensitive_content_detected`.

When a failure captured a Claude session ID, details identify the review as
resumable. Continue it with the same `review_id`; never create a replacement
conversation and pretend that context was preserved.

## V1 Limitations

- macOS only;
- one base Git reference compared with the current working tree; no independent
  `from_ref` and `to_ref` pair;
- untracked file contents are not included automatically;
- no HTTP server, graphical interface, GitHub App, PR comments, network
  database, multi-Mac synchronization, or telemetry;
- no automatic split for diffs larger than the configured limit;
- no automatic review-record retention cleanup;
- closing a review does not delete the native Claude Code conversation.
