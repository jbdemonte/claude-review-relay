# AGENTS.md

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## Language Policy

- Keep all repository content in English, including documentation, source code, comments, commit-facing text, and user-facing copy.
- Communicate with the user in French in CLI conversations.

## Commit Attribution

- Always create commits exclusively under the user's configured Git identity.
- Never attribute a commit to Codex or Claude, or add either as an author, co-author, signer, or contributor in commit messages or metadata.

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
6. Poll `claude-reviewer.get_review_status` at the returned
   `poll_after_seconds` interval until the status is no longer `pending`.
7. If the status is `waiting_for_quota`, stop polling and report `retry_at` when
   present. At or after that time, or when capacity is available if it is
   absent, call `claude-reviewer.start_retry_review` with the same `review_id`
   and a message that restates the interrupted operation, then resume polling.
   Do not create a replacement review.
8. Analyze each finding instead of accepting it blindly.
9. Fix confirmed critical-, high-, and medium-severity findings.
10. Prepare a factual technical response for incorrect findings.
11. Call `claude-reviewer.start_continue_review` with the same `review_id`,
   `refresh_diff: true`, and a request to verify the fixes.
12. Poll until the status is no longer `pending`, then require the returned
    `expected_response_sequence`; report a terminal error instead of polling
    indefinitely if that sequence was not produced.
13. Confirm that the continuation returns the same `claude_session_id`.
14. Stop after two completed review cycles unless a critical issue remains.
15. Call `claude-reviewer.close_review` after the final accepted verdict.
16. Do not treat Claude approval as a substitute for tests.
17. Claude is a read-only reviewer; Codex remains the only agent that modifies
    the repository.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:

- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them; do not pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what is confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No flexibility or configurability that was not requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: “Would a senior engineer say this is overcomplicated?” If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:

- Do not improve adjacent code, comments, or formatting.
- Do not refactor things that are not broken.
- Match the existing style, even if you would do it differently.
- If you notice unrelated dead code, mention it; do not delete it.

When your changes create orphans:

- Remove imports, variables, and functions that your changes made unused.
- Do not remove pre-existing dead code unless asked.

The test: every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:

- “Add validation” becomes “Write tests for invalid inputs, then make them pass.”
- “Fix the bug” becomes “Write a test that reproduces it, then make it pass.”
- “Refactor X” becomes “Ensure tests pass before and after.”

For multi-step tasks, state a brief plan:

```text
1. [Step] -> verify: [check]
2. [Step] -> verify: [check]
3. [Step] -> verify: [check]
```

Strong success criteria let you loop independently. Weak criteria such as “make it work” require constant clarification.

---

**These guidelines are working if:** there are fewer unnecessary changes in diffs, fewer rewrites caused by overcomplication, and clarifying questions come before implementation rather than after mistakes.
