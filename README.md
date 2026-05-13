# cockroachdb/actions

Reusable GitHub Actions and workflows for CockroachDB projects.

## Actions

### autotag-from-changelog

Creates git tags from CHANGELOG.md versions. Fails only when there is content
under `[Unreleased]` and the previous release version tag does not yet exist;
otherwise it succeeds even if `[Unreleased]` contains entries.

**Usage:**

```yaml
- uses: cockroachdb/actions/autotag-from-changelog@v0
```

**Inputs:**

| Name             | Default        | Description                |
| ---------------- | -------------- | -------------------------- |
| `changelog-path` | `CHANGELOG.md` | Path to the changelog file |

**Required permissions:**

```yaml
permissions:
  contents: write
```

### changelog-check

Validates that CHANGELOG.md follows the [Keep a Changelog](https://keepachangelog.com/)
standard. Ensures proper changelog structure, version ordering,
and detects breaking changes to enable automated version bump determination.

**Usage:**

```yaml
- uses: cockroachdb/actions/changelog-check@v0
  with:
    check-mode: diff
    base-ref: ${{ github.event.pull_request.base.ref }}
```

**Inputs:**

| Name               | Default        | Description                                                              |
| ------------------ | -------------- | ------------------------------------------------------------------------ |
| `changelog-path`   | `CHANGELOG.md` | Path to the changelog file                                               |
| `validation-depth` | `1`            | How many changelog entries to validate starting from the most recent     |
| `check-mode`       | `unreleased`   | Check mode for breaking change detection: `unreleased` (entire Unreleased section) or `diff` (PR changes only). Does not affect format/version validation, which always runs. |
| `base-ref`         | `''`           | Required when `check-mode` is `diff`. The base git ref to compare against for detecting breaking changes in the diff only (e.g., `main`, or `github.event.pull_request.base.ref` in PRs). Not needed for `unreleased` mode. |

**Outputs:**

| Name            | Description                                                      |
| --------------- | ---------------------------------------------------------------- |
| `is_valid`      | Whether the CHANGELOG format and version ordering are valid      |
| `has_breaking`  | Whether breaking changes were detected                           |

**Features:**

- Validates CHANGELOG.md format using Keep a Changelog standard
- Checks that versions are in descending order (newest first)
- Checks that release dates are in descending order
- Detects breaking changes via two methods:
  - Entries prefixed with `Breaking Change:` in any section
  - Presence of a `### Removed` section header
- Supports checking entire Unreleased section or only PR diff

### release-version-extract

Extracts the current version from CHANGELOG.md and determines the next version
based on unreleased changes. Analyzes changelog entries to automatically
determine whether a major, minor, or patch version bump is needed.

**Usage:**

```yaml
- uses: cockroachdb/actions/release-version-extract@v0
  id: version
- run: echo "Next version will be ${{ steps.version.outputs.next_version }}"
```

**Inputs:**

| Name             | Default        | Description                |
| ---------------- | -------------- | -------------------------- |
| `changelog-path` | `CHANGELOG.md` | Path to the changelog file |

**Outputs:**

| Name                 | Description                                                      |
| -------------------- | ---------------------------------------------------------------- |
| `current_version`    | Current latest released version (empty if no releases)           |
| `next_version`       | Suggested next version based on unreleased changes               |
| `bump_type`          | Type of version bump (`major`/`minor`/`patch`/`initial`, or empty if no changes) |
| `has_unreleased`     | Whether there are unreleased changes (`true`/`false`)            |
| `unreleased_changes` | Text content of unreleased changelog entries                     |

**Features:**

- Automatically determines version bump type from changelog entries
- Detects major bumps when breaking changes are present (lines prefixed with `Breaking Change:` or `### Removed` section)
- Handles initial releases (first release → 0.1.0)
- Returns empty `bump_type` when there are no unreleased changes
- Follows semantic versioning principles

### autosolve/assess

Runs Claude in read-only mode to assess whether a task is suitable for automated
resolution. Claude evaluates the task against configurable criteria and returns a
PROCEED or SKIP decision with reasoning.

**Usage:**

```yaml
# Check the target repo out into a subdirectory; the sandbox rejects
# the workspace root itself. See the "autosolve sandbox" section below.
- uses: actions/checkout@v6
  with:
    path: repo
- uses: cockroachdb/actions/autosolve/assess@v0
  with:
    working_directory: ./repo
    system_prompt: "Assess whether this issue can be resolved automatically."
    context_vars: "ISSUE_TITLE,ISSUE_BODY"
  env:
    ISSUE_TITLE: ${{ github.event.issue.title }}
    ISSUE_BODY: ${{ github.event.issue.body }}
```

**Inputs:**

| Name                  | Default              | Description                                                                                                                                           |
| --------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `claude_cli_version`  | `2.1.79`             | Claude CLI version to install (e.g. `2.1.79` or `latest`)                                                                                            |
| `system_prompt`       | **one required**     | Trusted instructions for Claude describing the task to assess. Do not embed untrusted user input here — use `context_vars` instead. At least one of `system_prompt` or `skill` is required. |
| `skill`               | **one required**     | Path to a skill/prompt file relative to `GITHUB_WORKSPACE`. At least one of `system_prompt` or `skill` is required.                                  |
| `context_vars`        | `""`                 | Comma-separated list of environment variable names to pass through to Claude for untrusted user input (e.g., issue titles/bodies)                     |
| `assessment_criteria` | [see default](autosolve/internal/prompt/templates/default-assessment-criteria.md) | Trusted criteria for the assessment. Do not embed untrusted user input. |
| `model`               | `claude-opus-4-6`    | Claude model ID                                                                                                                                       |
| `blocked_paths`       | `""`                 | Comma-separated path prefixes that cannot be modified (case-sensitive). `.github/` is always blocked.                                                                 |
| `log_level`           | `error`              | Controls Claude output in the step log: `error` (status only), `info` (result summary, permission denial warnings), `debug` (stream everything).     |
| `working_directory`   | **required**         | Directory the action runs in (relative to `GITHUB_WORKSPACE`). Must be a strict subdirectory — the workspace root is rejected. See [autosolve sandbox](#autosolve-sandbox). |
| `read_paths`          | `""`                 | Comma-separated extra host paths (files or directories) to bind read-only into the sandbox. See [autosolve sandbox](#autosolve-sandbox).            |

**Outputs:**

| Name         | Description                        |
| ------------ | ---------------------------------- |
| `assessment` | `PROCEED` or `SKIP`                |
| `summary`    | Human-readable assessment reasoning |
| `result`     | Full Claude result text             |

**Features:**

- Runs Claude in read-only mode (Read, Grep, Glob only) — no file modifications
- Safely passes untrusted user input via environment variables instead of prompt injection
- Supports custom assessment criteria or skill files
- Designed to gate the more expensive `autosolve/implement` step

### autosolve/implement

Runs Claude to implement a solution, validates changes with a security review,
pushes to a fork, and creates a pull request. Includes retry logic, blocked-path
enforcement, sensitive file detection, and token usage tracking.

**Usage:**

```yaml
# Check the target repo out into a subdirectory; the sandbox rejects
# the workspace root itself. See the "autosolve sandbox" section below.
- uses: actions/checkout@v6
  with:
    path: repo
- uses: cockroachdb/actions/autosolve/implement@v0
  with:
    working_directory: ./repo
    system_prompt: "Fix the issue described in the environment variables."
    context_vars: "ISSUE_TITLE,ISSUE_BODY"
    fork_owner: my-bot
    fork_repo: my-repo-fork
    fork_push_token: ${{ secrets.FORK_PAT }}
    pr_create_token: ${{ secrets.PR_PAT }}
  env:
    ISSUE_TITLE: ${{ github.event.issue.title }}
    ISSUE_BODY: ${{ github.event.issue.body }}
```

**Inputs:**

| Name                 | Default                          | Description                                                                                                     |
| -------------------- | -------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `claude_cli_version` | `2.1.79`                         | Claude CLI version to install (e.g. `2.1.79` or `latest`)                                                      |
| `system_prompt`      | **one required**                 | Trusted instructions for Claude describing the task. Do not embed untrusted user input — use `context_vars`. At least one of `system_prompt` or `skill` is required. |
| `skill`              | **one required**                 | Path to a skill/prompt file relative to `GITHUB_WORKSPACE`. At least one of `system_prompt` or `skill` is required. |
| `context_vars`       | `""`                             | Comma-separated list of environment variable names to pass through to Claude for untrusted user input           |
| `allowed_tools`      | [see below](#allowed_tools-default) | Claude `--allowedTools` string                                                                               |
| `model`              | `claude-opus-4-6`                | Claude model ID                                                                                                  |
| `max_retries`        | `3`                              | Maximum implementation attempts                                                                                  |
| `pr_target_repo`     | `${{ github.repository }}`       | Repository where the PR is created (`owner/repo`). Set this when the PR should target a different repo than the one running the workflow. |
| `pr_base_branch`     | `main`                           | Base branch for the PR                                                                                           |
| `pr_labels`          | `autosolve`                      | Comma-separated labels to apply to the PR                                                                        |
| `pr_draft`           | `true`                           | Whether to create the PR as a draft                                                                              |
| `fork_owner`         | **required**                     | GitHub username or org that owns the fork                                                                        |
| `fork_repo`          | **required**                     | Repository name of the fork                                                                                      |
| `fork_push_token`    | **required**                     | PAT with `contents: write` on the fork repository                                                                |
| `pr_create_token`    | **required**                     | PAT with `pull_requests: write` on the target repo (see [Token permissions](#token-permissions))                 |
| `blocked_paths`      | `""`                             | Comma-separated path prefixes that cannot be modified (case-sensitive). `.github/` is always blocked.                             |
| `git_user_name`      | `autosolve[bot]`                 | Git author/committer name                                                                                        |
| `git_user_email`     | `autosolve[bot]@users.noreply.github.com` | Git author/committer email                                                                            |
| `branch_prefix`      | `autosolve/`                     | Prefix for the branch name                                                                                       |
| `branch_suffix`      | `""`                             | Suffix for branch name. Defaults to timestamp.                                                                   |
| `commit_signature`   | `Co-Authored-By: Claude <noreply@anthropic.com>` | Signature line appended to commit messages                                                        |
| `pr_footer`          | [see below](#pr_footer-default)  | Footer appended to the PR body                                                                                   |
| `log_level`          | `error`                          | Controls Claude output in the step log: `error` (status only), `info` (result summary, permission denial warnings), `debug` (stream everything). |
| `working_directory`  | **required**                     | Directory the action runs in (relative to `GITHUB_WORKSPACE`). Must be a strict subdirectory — the workspace root is rejected. See [autosolve sandbox](#autosolve-sandbox). |
| `read_paths`         | `""`                             | Comma-separated extra host paths (files or directories) to bind read-only into the sandbox. See [autosolve sandbox](#autosolve-sandbox). |

<a id="allowed_tools-default"></a>
> Default `allowed_tools`:
> ```
> Read,Write,Edit,Grep,Glob,
> Bash(git add:*),Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git show:*),
> Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(make:*)
> ```

<a id="pr_footer-default"></a>
> Default `pr_footer`:
> ```
> ---
>
> *This PR was auto-generated by [claude-autosolve-action](https://github.com/cockroachdb/actions) using Claude Code.*
> *Please review carefully before approving.*
> ```

**Outputs:**

| Name          | Description                                  |
| ------------- | -------------------------------------------- |
| `status`      | `SUCCESS` or `FAILED`                        |
| `pr_url`      | URL of the created PR                        |
| `summary`     | Human-readable summary                       |
| `result`      | Full Claude result text                      |
| `branch_name` | Name of the branch pushed to the fork        |

**Features:**

- Retries implementation up to `max_retries` times on failure
- Enforces blocked-path restrictions (`.github/` is always blocked)
- Detects and rejects sensitive files (credentials, keys, `.env`)
- Runs an AI-powered security review on all changes before committing
- Pushes changes to a fork and creates a PR on the upstream repository
- Tracks Claude token usage

<a id="token-permissions"></a>
**Token permissions:**

If the forked repo is public, fine grained tokens can be used. Otherwise a
classic token is required.  This is because in order to create a PR, the PAT
must have write on the target and read on the fork.

| Token              | Fine-grained                                | Classic |
| ------------------ | ------------------------------------------- | ------- |
| `fork_push_token`  | `contents: write` on the fork repository    | `repo`  |
| `pr_create_token`  | `pull_requests: write` on the target repository | `repo`  |

Applying labels (`pr_labels`) requires `issues: write` on the target repo
(already covered by `repo` for classic tokens). If the token lacks this
permission, the action logs a warning and creates the PR without labels.

For organizations using SAML/SSO, if a classic token is used, it must be
authorized for the organization that owns the target repository. See
[GitHub docs on SSO authorization](https://docs.github.com/en/enterprise-cloud@latest/authentication/authenticating-with-saml-single-sign-on/authorizing-a-personal-access-token-for-use-with-saml-single-sign-on).

<a id="autosolve-sandbox"></a>
### autosolve sandbox

Both `autosolve/assess` and `autosolve/implement` run Claude inside a
[bubblewrap](https://github.com/containers/bubblewrap) filesystem sandbox
that denies access to anything not explicitly bound. This blocks
inadvertent reads of credentials or other repositories that earlier
steps may have dropped into `GITHUB_WORKSPACE` or `RUNNER_TEMP`.

**What's bound:**

| Path                                       | Mode | Notes                                                                          |
| ------------------------------------------ | ---- | ------------------------------------------------------------------------------ |
| `working_directory`                        | RW   | Claude's cwd; the only writable host path you control.                         |
| `read_paths`                               | RO   | Opt-in extras for shared schemas, generated protos, or other references.       |
| `GOOGLE_APPLICATION_CREDENTIALS` (file)    | RO   | Auto-bound when set so Claude can refresh Vertex tokens.                       |
| `/usr`, `/etc`, `/lib`, `/lib64`, `/opt`   | RO   | OS and tooling. Contains no per-user data.                                     |
| `/run/systemd/resolve`                     | RO   | DNS resolver state (only when present). `/run` itself is **not** bound, so `/run/docker.sock` stays out of reach. |
| Private `/tmp`                             | RW   | tmpfs; discarded after the run.                                                |

Everything else is denied — including the workspace root, `RUNNER_TEMP`,
`/home/runner`, ssh keys, and credential files dropped by other steps.

**Caveats:**

- **Linux only.** bubblewrap is Linux-only. The setup step disables
  `kernel.apparmor_restrict_unprivileged_userns` so unprivileged user
  namespaces work on Ubuntu 24.04+. This is fine on ephemeral hosted
  runners; persistent self-hosted runners should evaluate the tradeoff
  before adopting the action.
- **`working_directory` must be a strict subdirectory** of
  `GITHUB_WORKSPACE`. Check the target repo into a path like `./repo`
  and pass `working_directory: ./repo`. The workspace root itself is
  rejected because other actions (e.g. `google-github-actions/auth`)
  drop credential files there.
- **The host network namespace is shared** (`--share-net`). The sandbox
  enforces filesystem isolation, not network isolation; Claude can
  reach the same network the runner can.

### get-workflow-ref

Resolves the git ref that a caller used to invoke a reusable workflow by parsing
the caller's workflow file. Useful for reusable workflows that need to reference
other resources (actions, scripts, etc.) at the same version they were invoked with.

**Usage:**

```yaml
jobs:
  my-job:
    runs-on: ubuntu-latest
    steps:
      - uses: cockroachdb/actions/get-workflow-ref@v0
        id: ref
      - run: echo "Workflow was called with ref ${{ steps.ref.outputs.ref }}"
```

**Outputs:**

| Name  | Description                                                      |
| ----- | ---------------------------------------------------------------- |
| `ref` | Git ref used to invoke this workflow (e.g., `v1`, `main`, commit SHA) |

**Features:**

- No API calls or extra permissions needed
- Works by parsing the caller's workflow file from the event payload
- Returns the exact ref specified in the workflow call (tag, branch, or SHA)

## Workflows

### create-release-pr

Reusable workflow that automates version bump pull requests. Checks for unreleased
changes in CHANGELOG.md, determines the next semantic version, updates the changelog
with the release date, optionally runs custom update scripts, and creates a PR from
a fork to the upstream repository.

**Usage:**

```yaml
name: Create Version Bump PR

on:
  workflow_dispatch:

jobs:
  create-release-pr:
    uses: cockroachdb/actions/.github/workflows/create-release-pr.yml@v0
    with:
      fork_owner: my-release-bot
      fork_repo: my-repo-fork
      pr_base_branch: main
      release_date: 2026-03-30
      git_user_name: my-release-bot
      git_user_email: my-release-bot@users.noreply.github.com
      build_script: .github/scripts/build_script.sh
      files_to_commit: |
        package.json
        package-lock.json
    secrets:
      fork_push_token: ${{ secrets.FORK_PAT }}
      pr_create_token: ${{ secrets.PR_PAT }}
```

**Inputs:**

| Name                        | Required | Default | Description                                      |
| --------------------------- | -------- | ------- | ------------------------------------------------ |
| `fork_owner`                | Yes      |         | GitHub username or org that owns the fork        |
| `fork_repo`                 | Yes      |         | Repository name of the fork                      |
| `pr_base_branch`            | No       | `""`    | Base branch for the PR (defaults to repository default branch) |
| `build_script`              | No       | `""`    | Optional path to a bash script to execute before committing. The `VERSION` environment variable will be available. |
| `files_to_commit`           | No       | `""`    | Newline-separated list of file paths to commit (in addition to CHANGELOG.md which is always included). Paths should be relative to repository root. |
| `release_date`              | No       | `""`    | Release date in YYYY-MM-DD format (defaults to current date) |
| `git_user_name`             | No       | `github-actions[bot]` | Git user name for commits |
| `git_user_email`            | No       | `github-actions[bot]@users.noreply.github.com` | Git user email for commits |

**Secrets:**

| Name               | Required | Description                                      |
| ------------------ | -------- | ------------------------------------------------ |
| `fork_push_token`  | Yes      | PAT with push access to the fork                 |
| `pr_create_token`  | Yes      | PAT with permission to create PRs on the upstream repo |

**Outputs:**

| Name     | Description                                                      |
| -------- | ---------------------------------------------------------------- |
| `pr_url` | URL of the created pull request (empty if no unreleased changes) |

**Features:**

- Automatically detects unreleased changes in CHANGELOG.md
- Determines next version using semver principles
- Updates CHANGELOG.md with new version and customizable release date (defaults to current date)
- Supports custom bash scripts to run before committing (via `build_script` file path)
- Creates PR from fork to upstream repository
- Exits gracefully when no unreleased changes exist

## Development

Run all tests locally:

```sh
./test.sh
```

Tests also run automatically on pull requests via CI.
