<instructions>
Implement the task described above.

1. Read CLAUDE.md (if it exists) for project conventions, build commands,
   test commands, and commit message format.
2. Understand the codebase and the task requirements.
3. When fixing bugs, prefer a test-first approach:
   a. Write a test that demonstrates the bug (verify it fails).
   b. Apply the fix.
   c. Verify the test passes.
   Skip writing a dedicated test when the fix is trivial and self-evident
   (e.g., adding a timeout, fixing a typo), the behavior is impractical to
   unit test (e.g., network timeouts, OS-level behavior), or the fix is a
   documentation-only change. The goal is to prove the bug existed and
   confirm it's resolved, not to test for testing's sake.
4. Implement the minimal changes required. Prefer backwards-compatible
   changes wherever possible — avoid breaking existing APIs, interfaces,
   or behavior unless the task explicitly requires it.
5. Run relevant tests to verify your changes work. Only test the specific
   packages/files affected by your changes.
6. If tests fail, fix the issues and re-run. Only report FAILED if you
   cannot make tests pass after reasonable effort.
7. Stage all your changes with `git add`. Do not commit — the action
   handles committing. All changes will be squashed into a single commit,
   so organize your work accordingly.
   IMPORTANT: NEVER stage credential files, secret keys, or tokens.
   Do NOT stage files matching: gha-creds-*.json, *.pem, *.key, *.p12,
   credentials.json, service-account*.json, or .env files. If you see
   these files in the working tree, leave them unstaged.
8. Write a commit message to the path stored in the
   `AUTOSOLVE_COMMIT_MESSAGE_PATH` environment variable. Read the path
   with `printenv AUTOSOLVE_COMMIT_MESSAGE_PATH`, then use the Write
   tool with that exact path. The path lives outside the repo on
   purpose so the file can never be accidentally staged — do NOT write
   it inside the working directory. Use standard git format: a subject
   line (under 72 characters, imperative mood), a blank line, then a
   body explaining what was changed and why. Since all changes go into
   a single commit, the message should cover the full scope of the
   change. Focus on helping a reviewer understand the commit — do NOT
   list individual files. Example:
   ```
   Fix timeout in retry loop

   The retry loop was using a hardcoded 5s timeout which was too short
   for large payloads. Increased to 30s and made it configurable via
   the RETRY_TIMEOUT env var. Added a test that verifies retry behavior
   with slow responses.
   ```
   If CLAUDE.md specifies a commit message format, follow that instead.
9. Write a PR description to the path stored in the
   `AUTOSOLVE_PR_BODY_PATH` environment variable. Read the path with
   `printenv AUTOSOLVE_PR_BODY_PATH`, then use the Write tool with that
   exact path. As with the commit message, this path is outside the
   repo. This will be used as the body of the pull request. The PR
   description and commit message serve similar purposes for
   single-commit PRs, but the PR description should be more
   reader-friendly. Include:
   - A brief summary of what was changed and why (2-3 sentences max).
   - What testing was done (tests added, tests run, manual verification).
   Do NOT include a list of changed files — reviewers can see that in the
   diff. Keep it concise and focused on helping a reviewer understand the
   change.

**OUTPUT REQUIREMENT**: You MUST end your response with exactly one of
these lines (no other text on that line):
IMPLEMENTATION_RESULT - SUCCESS
IMPLEMENTATION_RESULT - FAILED
</instructions>
