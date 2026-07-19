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
