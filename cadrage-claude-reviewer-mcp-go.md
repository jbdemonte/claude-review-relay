# Scoping Document — “Claude Reviewer” Go MCP Server for Codex

## 1. Objective

Create a local MCP server on macOS, written in Go, that allows Codex to use Claude Code as an independent reviewer.

The target workflow is:

```text
Codex implements a change
        │
        ▼
Codex calls the review_diff MCP tool
        │
        ▼
The Go server launches Claude Code in read-only mode
        │
        ▼
Claude analyzes the repository and the diff
        │
        ▼
Claude returns a structured review
        │
        ▼
Codex fixes or responds to the remarks
        │
        ▼
Codex calls continue_review with the same review_id
        │
        ▼
The Go server resumes exactly the same Claude session
```

The essential point is preserving Claude's context across multiple calls. A review that has been started must be resumable with its analyses, the files already read, the previous remarks, and Codex's responses.

---

## 2. General constraints

- Language: Go.
- Initial target platform: macOS Apple Silicon and Intel.
- MCP transport: STDIO.
- The program must run as a single binary.
- Claude Code is invoked via its local CLI.
- The program must never store any Anthropic key.
- The existing `claude` authentication must be reused.
- Claude acts solely as a reviewer.
- Claude must be able to read the repository but must never modify files.
- The server must never run destructive Git commands.
- The STDIO protocol must remain clean:
  - stdout reserved exclusively for MCP messages;
  - logs and diagnostics exclusively on stderr.
- The project must be installable and configurable by Codex directly on the Mac.

---

## 3. Context persistence principle

Claude Code supports persistent sessions. The server must keep the Claude `session_id` associated with each review.

### First call

On a first call to `review_diff`:

1. generate a `review_id`;
2. generate a `session_id`, or let Claude generate one;
3. launch Claude Code in non-interactive mode;
4. retrieve the `session_id` from the structured output;
5. record the association:
   - `review_id`;
   - `claude_session_id`;
   - canonical repository path;
   - creation date;
   - last-used date;
   - review goal;
   - Git base used;
   - review status.

### Subsequent calls

On a call to `continue_review`:

1. look up the record using the `review_id`;
2. verify that the requested repository matches the session's repository;
3. relaunch Claude with:

```bash
claude -p --resume "<claude_session_id>" ...
```

4. send only the new message to Claude;
5. do not artificially rebuild the entire history in the prompt;
6. update the last-used date.

### On-disk persistence

Persistence must survive:

- multiple MCP calls;
- a restart of the MCP server;
- a restart of Codex;
- ideally, a restart of the Mac.

Recommended storage for V1:

```text
~/Library/Application Support/claude-reviewer/sessions.json
```

Atomic JSON storage is sufficient for V1.

Atomic writes are mandatory:

1. write to a temporary file in the same directory;
2. call `Sync()`;
3. rename the temporary file to `sessions.json`.

Provide a Go storage interface so that JSON can later be replaced with SQLite without modifying the rest of the code.

```go
type SessionStore interface {
    Create(ctx context.Context, session ReviewSession) error
    Get(ctx context.Context, reviewID string) (ReviewSession, error)
    Update(ctx context.Context, session ReviewSession) error
    Delete(ctx context.Context, reviewID string) error
    List(ctx context.Context) ([]ReviewSession, error)
}
```

Never simply use `claude --continue`, because that option resumes the most recent session in the directory and can therefore resume the wrong conversation. Always use an explicit identifier with `--resume`.

---

## 4. MCP tools to expose

### 4.1 `review_diff`

Starts a new review and creates a new Claude session.

Input:

```json
{
  "repository_path": "/absolute/path/to/repo",
  "goal": "Description of the change that was made",
  "base_ref": "HEAD",
  "review_focus": [
    "correctness",
    "regressions",
    "architecture",
    "performance",
    "security",
    "tests"
  ],
  "additional_context": "Optional context",
  "test_results": "Optional results of tests already run",
  "model": "opus",
  "max_turns": 12
}
```

Rules:

- `repository_path` is required.
- The path must exist.
- The path must be a Git repository.
- Convert the path to a canonical absolute path.
- `goal` is required.
- `base_ref` defaults to `HEAD`.
- The diff must be computed by the server, not blindly supplied by Codex.
- The server may use:
  - `git diff <base_ref>` for uncommitted changes;
  - untracked files must be reported separately;
  - never automatically send obviously secret files.
- The response must contain the new `review_id`.

### 4.2 `continue_review`

Resumes the same Claude conversation.

Input:

```json
{
  "review_id": "review-uuid",
  "message": "I have fixed findings F001 and F003. Check the new diff.",
  "refresh_diff": true,
  "test_results": "Optional new results"
}
```

Rules:

- resume the session using its `claude_session_id`;
- use the same working directory;
- if `refresh_diff` is `true`, recompute the current diff and add it to the new message;
- remind Claude that this is the continuation of the review, without repeating the entire initial prompt;
- return a new structured response;
- keep the same `review_id`.

### 4.3 `get_review`

Returns the local metadata of a review without calling Claude.

Input:

```json
{
  "review_id": "review-uuid"
}
```

Output:

```json
{
  "review_id": "...",
  "claude_session_id": "...",
  "repository_path": "...",
  "goal": "...",
  "created_at": "...",
  "updated_at": "...",
  "status": "open"
}
```

Do not return any secrets or the full content of the conversation.

### 4.4 `list_reviews`

Lists persisted reviews, sorted from most recent to oldest.

Optional input:

```json
{
  "repository_path": "/optional/path",
  "status": "open"
}
```

### 4.5 `close_review`

Marks a review as closed.

Input:

```json
{
  "review_id": "review-uuid",
  "delete_claude_session": false
}
```

For V1, there is no need to delete Claude Code's native data. It is enough to close or delete the local association, depending on the chosen option.

---

## 5. Running Claude Code

### Recommended initial command

Use JSON stream output to robustly retrieve the initialization message and the `session_id`.

General form:

```bash
claude \
  -p \
  --output-format stream-json \
  --verbose \
  --permission-mode dontAsk \
  --tools "Read,Glob,Grep" \
  --disallowedTools "Edit" "Write" "NotebookEdit" "Bash" "WebSearch" "WebFetch" "mcp__*" \
  --max-turns 12 \
  --model opus \
  "<prompt>"
```

The process must be launched with:

```go
cmd.Dir = repositoryPath
```

### Session resumption

```bash
claude \
  -p \
  --resume "<claude_session_id>" \
  --output-format stream-json \
  --verbose \
  --permission-mode dontAsk \
  --tools "Read,Glob,Grep" \
  --disallowedTools "Edit" "Write" "NotebookEdit" "Bash" "WebSearch" "WebFetch" "mcp__*" \
  --max-turns 12 \
  "<follow-up-message>"
```

### Read-only security

Claude must have access only to:

- `Read`;
- `Glob`;
- `Grep`.

Any write or execution tool must be forbidden.

The Go server computes the Git diff itself before launching Claude. This avoids giving Claude the Bash tool.

Forbid at minimum:

- `Edit`;
- `Write`;
- `NotebookEdit`;
- `Bash`;
- `WebSearch`;
- `WebFetch`;
- all external MCP tools.

Do not use `--dangerously-skip-permissions`.

### Timeout and interruption

- Default timeout for a Claude call: 10 minutes.
- Timeout configurable in the configuration file.
- Use `exec.CommandContext`.
- On MCP cancellation, interrupt the child process.
- On macOS, ensure that descendant processes are not left orphaned.
- Capture stderr separately for diagnostics.
- Limit the size of outputs kept in memory.

---

## 6. Retrieving the `session_id`

The `stream-json` parser must read stdout line by line.

When a system initialization event is received, extract the `session_id`.

The code must not depend on the exact order of all events, only on structural fields such as:

```json
{
  "type": "system",
  "subtype": "init",
  "session_id": "..."
}
```

Since the exact format may evolve, the parser must:

- ignore unknown fields;
- accept that the `session_id` may appear directly or inside a wrapper structure;
- keep unrecognized lines in the debug logs;
- fail clearly if no `session_id` is obtained during a new review;
- accept that a resumption keeps the same identifier.

Add unit tests with several examples of JSON streams.

---

## 7. Response format requested from Claude

Use `--json-schema` when the local version of Claude Code supports it.

Logical schema:

```json
{
  "type": "object",
  "required": ["verdict", "summary", "findings", "missing_tests"],
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["approve", "changes_requested", "needs_context"]
    },
    "summary": {
      "type": "string"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": [
          "id",
          "severity",
          "category",
          "file",
          "problem",
          "impact",
          "recommendation",
          "confidence"
        ],
        "properties": {
          "id": {
            "type": "string"
          },
          "severity": {
            "type": "string",
            "enum": ["critical", "high", "medium", "low"]
          },
          "category": {
            "type": "string",
            "enum": [
              "correctness",
              "regression",
              "architecture",
              "performance",
              "security",
              "concurrency",
              "maintainability",
              "test"
            ]
          },
          "file": {
            "type": "string"
          },
          "line": {
            "type": ["integer", "null"]
          },
          "problem": {
            "type": "string"
          },
          "impact": {
            "type": "string"
          },
          "recommendation": {
            "type": "string"
          },
          "confidence": {
            "type": "number",
            "minimum": 0,
            "maximum": 1
          }
        }
      }
    },
    "missing_tests": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "questions": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  }
}
```

Each finding must have a stable identifier within the conversation:

```text
F001
F002
F003
```

On resumption, Claude must explicitly indicate, for previous findings:

- `resolved`;
- `still_open`;
- `invalidated`;
- or `partially_resolved`.

The schema can therefore be extended for `continue_review` with:

```json
{
  "previous_findings": [
    {
      "id": "F001",
      "status": "resolved",
      "comment": "..."
    }
  ]
}
```

---

## 8. Reviewer system prompt

Create an embedded file, for example:

```text
internal/reviewer/prompts/reviewer.md
```

Expected content:

```text
You are an independent senior software reviewer.

You are not the author of the change. Your role is to actively look for real
defects, regressions, fragile assumptions, and missing tests.

You work in read-only mode. You must never modify a file.

Rules:

- Examine the diff, then read the necessary surrounding files.
- Check callers and callees of modified functions.
- Do not report stylistic preferences without a concrete impact.
- Each finding must describe a reproducible scenario or a specific risk.
- Cite the file and, when possible, the line.
- Do not claim to have run tests.
- Distinguish facts, assumptions, and uncertainties.
- Do not recommend a broad redesign when a local fix is sufficient.
- Consider the provided functional goal.
- When resuming a review, preserve previous finding IDs.
- Verify fixes before marking a finding as resolved.
- Approval means that no significant issue was identified, not that the code is
  guaranteed to be defect-free.

Use stable finding IDs F001, F002, F003, and so on. For each resumed review,
report the status of every previous finding in `previous_findings`.
```

The first user prompt must include:

- the goal;
- the Git base;
- the diff;
- the untracked files;
- the provided test results;
- the additional context;
- the requested review focus areas;
- the instruction to read the surrounding code in read-only mode.

---

## 9. Diff handling

Create a dedicated Git component.

Suggested interface:

```go
type GitService interface {
    ValidateRepository(ctx context.Context, path string) error
    Root(ctx context.Context, path string) (string, error)
    Diff(ctx context.Context, path, baseRef string) (string, error)
    UntrackedFiles(ctx context.Context, path string) ([]string, error)
    CurrentBranch(ctx context.Context, path string) (string, error)
    HeadSHA(ctx context.Context, path string) (string, error)
}
```

Commands allowed in the Go process:

```bash
git rev-parse --show-toplevel
git rev-parse --verify <base_ref>
git rev-parse HEAD
git branch --show-current
git diff --no-ext-diff --unified=80 <base_ref> --
git status --porcelain=v1
```

Use `exec.CommandContext` with separate arguments. Never build a concatenated shell command.

### Size limit

The diff can be huge.

Provide:

- a configurable limit, 2 MiB by default;
- binary file detection;
- an explicit error message if the diff exceeds the limit;
- a future option to split the review by files;
- never truncate silently.

For V1, if the limit is exceeded, return an error explaining to Codex that it must reduce the scope of the change or choose a future filtering option.

---

## 10. Protection against secrets

Before sending the diff to Claude, detect at minimum:

- `.env` files;
- private keys;
- file names containing `secret`, `credentials`, `token`;
- blocks resembling PEM keys;
- strings that are clearly token-like.

Behavior:

- do not record secrets in the logs;
- replace their value with `[REDACTED]` when reasonable;
- refuse the review if a complete private key is detected;
- tell Codex which files were excluded or masked.

Do not claim that this detection is foolproof.

---

## 11. Configuration

Create an optional file:

```text
~/Library/Application Support/claude-reviewer/config.json
```

Example:

```json
{
  "claude_binary": "/opt/homebrew/bin/claude",
  "default_model": "opus",
  "default_max_turns": 12,
  "timeout_seconds": 600,
  "max_diff_bytes": 2097152,
  "log_level": "info",
  "session_retention_days": 30
}
```

Claude binary resolution:

1. explicit configuration;
2. `exec.LookPath("claude")`;
3. common Homebrew paths:
   - `/opt/homebrew/bin/claude`;
   - `/usr/local/bin/claude`.

Never hardcode only an Apple Silicon path.

Add a command:

```bash
claude-reviewer doctor
```

It must check:

- presence of the Claude binary;
- Claude Code version;
- authentication with `claude auth status`;
- presence of Git;
- write access to the data directory;
- validity of the session storage;
- compatibility of the required CLI flags.

MCP mode remains the default command:

```bash
claude-reviewer
```

or explicitly:

```bash
claude-reviewer serve
```

---

## 12. Desired project structure

```text
claude-reviewer/
├── cmd/
│   └── claude-reviewer/
│       └── main.go
├── internal/
│   ├── claude/
│   │   ├── client.go
│   │   ├── parser.go
│   │   └── client_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── paths_darwin.go
│   ├── git/
│   │   ├── service.go
│   │   └── service_test.go
│   ├── mcp/
│   │   ├── server.go
│   │   └── tools.go
│   ├── reviewer/
│   │   ├── service.go
│   │   ├── prompt.go
│   │   ├── schema.go
│   │   └── prompts/
│   │       └── reviewer.md
│   ├── session/
│   │   ├── model.go
│   │   ├── store.go
│   │   ├── json_store.go
│   │   └── json_store_test.go
│   └── security/
│       ├── redact.go
│       └── redact_test.go
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── AGENTS.md
└── LICENSE
```

Choose a maintained and reasonably lightweight Go MCP library. Before adding it, check its current documentation and prefer an official or clearly maintained SDK. Isolate the library behind the `internal/mcp` package to limit coupling.

---

## 13. Data model

```go
type ReviewSession struct {
    ReviewID        string       `json:"review_id"`
    ClaudeSessionID string       `json:"claude_session_id"`
    RepositoryPath  string       `json:"repository_path"`
    Goal            string       `json:"goal"`
    BaseRef         string       `json:"base_ref"`
    HeadSHAAtStart  string       `json:"head_sha_at_start"`
    Model           string       `json:"model"`
    Status          ReviewStatus `json:"status"`
    CreatedAt       time.Time    `json:"created_at"`
    UpdatedAt       time.Time    `json:"updated_at"`
}

type ReviewStatus string

const (
    ReviewStatusOpen   ReviewStatus = "open"
    ReviewStatusClosed ReviewStatus = "closed"
)
```

Paths must be normalized, resolving symbolic links when possible.

The server must prevent a `review_id` from being used in a different repository.

---

## 14. Concurrency

Multiple MCP calls may arrive simultaneously.

Rules:

- allow different reviews in parallel;
- forbid two simultaneous calls on the same `review_id`;
- use a per-session lock;
- protect the JSON storage;
- write data atomically;
- return a clear `review_busy` error if a session is already in use;
- do not block all reviews because of a single long-running session.

---

## 15. Structured MCP errors

Define actionable errors:

- `invalid_repository`
- `invalid_base_ref`
- `review_not_found`
- `review_closed`
- `review_busy`
- `repository_mismatch`
- `claude_not_found`
- `claude_not_authenticated`
- `claude_timeout`
- `claude_failed`
- `claude_session_id_missing`
- `invalid_claude_output`
- `diff_too_large`
- `sensitive_content_detected`
- `storage_error`

Each error must include:

```json
{
  "code": "review_not_found",
  "message": "No review matches this identifier.",
  "details": {}
}
```

Never return a raw stack trace to Codex.

---

## 16. Logs

JSON logs or `slog`.

Include:

- timestamp;
- level;
- MCP tool;
- `review_id`;
- duration;
- Claude exit code;
- diff size;
- number of findings.

Do not log:

- the full diff;
- full prompts;
- secrets;
- the full Claude response by default.

Provide an explicit debug mode.

All logs must go to stderr.

---

## 17. macOS installation

The README must provide the following commands.

### Build

```bash
go build -o ./bin/claude-reviewer ./cmd/claude-reviewer
```

### User installation

```bash
mkdir -p "$HOME/.local/bin"
cp ./bin/claude-reviewer "$HOME/.local/bin/claude-reviewer"
chmod +x "$HOME/.local/bin/claude-reviewer"
```

Verify that `~/.local/bin` is in the `PATH`.

### Diagnostics

```bash
claude-reviewer doctor
```

### Adding to Codex

Preferred command:

```bash
codex mcp add claude-reviewer -- "$HOME/.local/bin/claude-reviewer" serve
```

Then:

```bash
codex mcp list
```

Equivalent TOML configuration:

```toml
[mcp_servers.claude-reviewer]
command = "/Users/USERNAME/.local/bin/claude-reviewer"
args = ["serve"]
```

Do not write `$HOME` literally in the TOML `command` field, because shell expansion is not guaranteed. The installation script must inject the real absolute path.

---

## 18. Codex instructions to install in `AGENTS.md`

The project must provide this ready-to-copy block:

```md
## Cross-Review with Claude

For every non-trivial change:

1. Implement the change.
2. Run the relevant tests, linting, and type checking.
3. Call `claude-reviewer.review_diff`.
4. Provide a precise goal and the test results.
5. Analyze each finding instead of accepting it blindly.
6. Fix confirmed critical-, high-, and medium-severity findings.
7. Prepare a factual technical response for incorrect findings.
8. Call `claude-reviewer.continue_review` with the same `review_id`.
9. Ask Claude to verify the fixes and reassess previous findings.
10. Stop after two complete cycles unless a critical issue remains.
11. Do not treat Claude approval as a substitute for tests.
12. Claude is a read-only reviewer; Codex remains the only agent that modifies the repository.
```

---

## 19. Mandatory tests

### Unit tests

- parsing of the Claude JSON stream;
- `session_id` extraction;
- structured response;
- storing and resuming a session;
- atomic writes;
- locking by `review_id`;
- detection of a different repository;
- `base_ref` validation;
- secret redaction;
- diff size limit;
- timeout errors;
- Claude process errors.

### Integration tests

Use a fake `claude` executable configurable in the tests.

Minimum scenario:

1. `review_diff` launches the fake Claude;
2. the fake Claude returns an init event with `session_id = A`;
3. the server persists `A`;
4. the server returns a `review_id = R`;
5. `continue_review(R)` relaunches the fake Claude with `--resume A`;
6. verify that the new prompt contains only the necessary follow-up;
7. restart a new instance of the store;
8. verify that `continue_review(R)` still works.

### Real manual test

On a small Git repository:

1. create a deliberately incorrect change;
2. call `review_diff` from Codex;
3. note the `review_id`;
4. fix it partially;
5. call `continue_review`;
6. verify that Claude remembers the initial finding;
7. stop and restart Codex;
8. call `continue_review` again with the same identifier;
9. verify that the context is preserved.

---

## 20. Acceptance criteria

The project is complete when:

- the binary starts as a STDIO MCP server;
- Codex sees the five tools;
- an initial review produces a `review_id`;
- the `claude_session_id` is persisted;
- a follow-up resumes the same session with `--resume`;
- the context survives a server restart;
- Claude can neither write nor run Bash;
- the diff is computed by the server;
- responses are structured and validated;
- errors are clean and actionable;
- stdout never contains logs;
- `go test ./...` passes;
- `go vet ./...` passes;
- the README enables a complete installation on macOS;
- `claude-reviewer doctor` validates the local environment;
- Codex can install the server with a documented command.

---

## 21. Out of scope for V1

Do not implement in the first version:

- remote HTTP MCP server;
- graphical interface;
- GitHub App;
- automatic creation of PR comments;
- code modification by Claude;
- orchestration of multiple Claude models;
- networked database;
- synchronization across multiple Macs;
- centralized team management;
- automatic telemetry submission;
- review of diffs larger than 2 MiB via automatic splitting.

Prepare the interfaces to allow these evolutions, but do not over-engineer V1.

---

## 22. Implementation order requested of Codex

1. Initialize the Go module.
2. Choose and integrate the Go MCP library.
3. Create the STDIO server and a temporary `ping` tool.
4. Implement the macOS paths and the configuration.
5. Implement the JSON `SessionStore` with tests.
6. Implement the Git service with tests.
7. Implement the Claude client and the `stream-json` parser.
8. Implement the prompt and the response schema.
9. Implement `review_diff`.
10. Implement `continue_review`.
11. Implement `get_review`, `list_reviews`, `close_review`.
12. Add the security protections and limits.
13. Add the per-session locks.
14. Add `doctor`.
15. Write the integration tests with the fake Claude.
16. Write the README and the `AGENTS.md` block.
17. Build and perform a real test with the local Claude Code.
18. Add a Codex installation command.
19. Run:
    - `gofmt`;
    - `go test ./...`;
    - `go vet ./...`.
20. Provide a final summary:
    - files created;
    - architecture;
    - installation commands;
    - remaining limitations;
    - proof that a session is actually resumed.

---

## 23. Important decisions not to change without justification

- Go is mandatory.
- MCP STDIO is mandatory for V1.
- Context must be preserved via the real Claude `session_id` and `--resume`.
- The `review_id` → `claude_session_id` association must be persisted on disk.
- Claude is strictly read-only.
- The server computes the diff.
- stdout is reserved for the MCP protocol.
- `claude --continue` must not be used.
- A `review_id` cannot silently migrate to a different repository.
- Concurrent sessions must be locked individually.
- Claude's responses must be structured and validated.
