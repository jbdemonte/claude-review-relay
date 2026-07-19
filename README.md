# Claude Reviewer MCP

A local Go MCP server that lets Codex delegate Git diff reviews to Claude Code
in read-only mode. Each `review_id` is durably associated with an explicit
Claude `session_id`. Follow-ups exclusively use
`claude -p --resume <session_id>`; the ambiguous `--continue` option is never
used.

## Core Guarantees

- MCP over STDIO, with stdout reserved for the protocol and JSON logs on stderr;
- five tools: `review_diff`, `continue_review`, `get_review`, `list_reviews`,
  and `close_review`;
- explicit MCP annotations: reads are marked read-only and closing is marked
  destructive so Codex approval policies behave correctly;
- diffs are computed locally with separate Git arguments, without a shell or
  destructive commands;
- Claude is limited to `Read,Glob,Grep`; writing, Bash, Web, and MCP tools are
  forbidden;
- the prompt and diff are sent to Claude through stdin, never on the command
  line visible through `ps`, and do not depend on the macOS `ARG_MAX` limit;
- responses are constrained by JSON Schema and then validated in Go;
- sessions are stored in
  `~/Library/Application Support/claude-reviewer/sessions.json`, written
  atomically with `Sync`, rename, and `0600` permissions;
- each review has a non-blocking lock (`review_busy`), while other reviews can
  run concurrently;
- the default diff limit is 2 MiB and is never silently truncated;
- sensitive filenames are excluded, common tokens are redacted, and complete
  private keys cause the review to be rejected.

Secret detection is a supplemental defense and is not infallible. Untracked
files are reported by name, but their contents are not sent automatically.

## Prerequisites

- macOS on Apple Silicon or Intel;
- Go 1.25 or newer;
- Git;
- Claude Code installed and authenticated (`claude auth login`);
- Codex CLI for automatic server registration.

## Build and Test

```bash
go build -o ./bin/claude-reviewer ./cmd/claude-reviewer
go test ./...
go vet ./...
```

Or:

```bash
make check
```

## User Installation on macOS

```bash
mkdir -p "$HOME/.local/bin"
cp ./bin/claude-reviewer "$HOME/.local/bin/claude-reviewer"
chmod +x "$HOME/.local/bin/claude-reviewer"
```

Add `~/.local/bin` to `PATH` if necessary, then diagnose the environment:

```bash
claude-reviewer doctor
```

The JSON report checks the Claude Code binary and version, authentication, Git,
write access to the data directory, session storage, and required CLI flags.

## MCP Installation in Codex

The recommended command injects the actual absolute path:

```bash
codex mcp add claude-reviewer -- "$HOME/.local/bin/claude-reviewer" serve
codex mcp list
```

Equivalent TOML configuration (replace the username):

```toml
[mcp_servers.claude-reviewer]
command = "/Users/USERNAME/.local/bin/claude-reviewer"
args = ["serve"]
```

Do not write `$HOME` literally in `command`; shell expansion is not guaranteed
for this field.

The server starts with no subcommand or explicitly with:

```bash
claude-reviewer
claude-reviewer serve
```

## Usage

`review_diff` requires at least `repository_path` and `goal`. `base_ref`
defaults to `HEAD`, the primary model to `fable`, the fallback model to `opus`,
the effort to `max`, and `max_turns` to 12. The result contains a new
`review_id` and the persisted `claude_session_id`.

The `max` effort deliberately prioritizes review quality over cost and latency.
Each call can choose a lower effort from `low`, `medium`, `high`, and `xhigh`.

`continue_review` requires the same `review_id` and a new `message`. With
`refresh_diff: true`, the server recomputes the diff and adds it only to the
follow-up message. It reloads the association from disk and invokes exactly:

```text
claude -p --resume <claude_session_id> ... <new-message>
```

The conversational context therefore belongs to the native Claude session and
survives restarts of the server, Codex, and the Mac.

`get_review` does not contact Claude. `list_reviews` accepts `repository_path`
and `status` filters. `close_review` closes the association; with
`delete_claude_session: true`, V1 deletes only the local association, not native
Claude Code data.

## Optional Configuration

Create `~/Library/Application Support/claude-reviewer/config.json`:

```json
{
  "claude_binary": "/opt/homebrew/bin/claude",
  "default_model": "fable",
  "default_fallback_model": "opus",
  "default_effort": "max",
  "default_max_turns": 12,
  "timeout_seconds": 600,
  "max_diff_bytes": 2097152,
  "max_output_bytes": 8388608,
  "log_level": "info",
  "session_retention_days": 30
}
```

Without an explicit path, resolution tries `PATH`, then the Apple Silicon and
Intel Homebrew paths. `session_retention_days` is reserved for future explicit
cleanup; V1 never deletes sessions automatically.

## Errors

Tool errors are actionable JSON (`code`, `message`, `details`) and expose no
stack trace, prompt, or diff. Codes include `invalid_repository`,
`invalid_base_ref`, `review_not_found`, `review_closed`, `review_busy`,
`repository_mismatch`, `claude_not_found`, `claude_not_authenticated`,
`claude_timeout`, `claude_failed`, `claude_session_id_missing`,
`invalid_claude_output`, `diff_too_large`, `claude_output_too_large`,
`sensitive_content_detected`, and `storage_error`.

## V1 Limitations

V1 has no HTTP server, graphical interface, GitHub App, PR comments, Claude code
modification, network database, multi-Mac synchronization, telemetry, or
automatic splitting of diffs larger than 2 MiB. Closing a review does not delete
its conversation from native Claude Code storage.
