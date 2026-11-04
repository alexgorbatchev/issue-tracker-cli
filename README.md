`issue-tracker` keeps tickets for all your GitHub repositories in one private repository, so coding agents take their work from a place strangers cannot write to. It is for people running several agents across many repositories, where public issue trackers are a prompt injection path and shared accounts make it hard to tell agents apart.

# What It Does

- **Single tracker:** Stores every ticket in one private repository, with one board per code repository.
- **Atomic claims:** Lets any number of agents race for a ticket, with exactly one winner.
- **Ready work:** Shows tickets that can start now, skipping blocked, claimed, and finished work.
- **Attachments:** Uploads and downloads screenshots, videos, logs, and other files on tickets and comments using only your GitHub login, placing images where the text references them.
- **Merge checks:** Closes a ticket only after confirming its pull requests merged.
- **Stale reporting:** Flags claimed tickets with no recent activity.
- **Project detection:** Works out the project from the repository and directory you are in, so most commands need few arguments.
- **Monorepos:** Registers directories of one repository as components, each with its own label on the repository's board.
- **Triage inbox:** Lists tickets filed on GitHub without a project, and keeps them out of every work queue until they get one.

# How It Works

- Register a repository with `issue-tracker project add`, which creates its board and label.
- Create tickets with `issue-tracker issue create`, from that repository or anywhere else with `--project`.
- An agent runs `issue-tracker issue claim --next` to take the most urgent ready ticket, by its `priority:P0` to `priority:P3` label, then the oldest.
- The agent reads the ticket and its attachments, then does the work in the ticket's repository.
- After the pull request merges, `issue-tracker issue close <number> --pr <pull-request>` closes the ticket.

# How it Really Works

- Each project is a `repo:<name>` label and a private GitHub Project board in the tracker repository. Boards are never linked to your code repositories, and nothing is written to them, so a public repository shows no trace of the tracker.
- A ticket you create inside a board on GitHub belongs to that project without a label. If a label and a board disagree, the label wins.
- A ticket with neither a project label nor a place on a project board has no project. It is never ready and cannot be claimed until you give it a label or put it on a board in GitHub, and `issue list --unrouted` lists these tickets for triage.
- A component is a project for one directory of a registered repository, registered with `project add --path`. It has its own `repo:<repo>/<component>` label and shares the repository's board, where a `Component` field lists the components. A ticket created on the board belongs to the component picked in its `Component` field, or to the repository when the field is empty; group a board view by `Component` and a ticket created in a group gets that component. Inside that directory the component is the current project; elsewhere in the repository, the repository's lists, claims, and counts include its components' tickets.
- A claim is a Git ref named `refs/claims/<number>` in the tracker repository. GitHub refuses to create a ref that exists, so a second claimant fails at once. Refs do not appear on the Tags or Branches pages.
- Claims never expire on their own. A Doing ticket with no ticket or board activity for `stale_days` is reported as stale and is left for you to release.
- A ticket is ready when it has a project, is open, does not have the `blocked` label, has no open blockers, has no blocked parent or grandparent, has no open sub-issues, and is not in Doing or Done. The `blocked` label applies only to the ticket that carries it; a parent or grandparent counts as blocked only through open blockers. A ticket with sub-issues works as an epic: `issue sub list` shows its progress, and `issue sub close-eligible` closes it once they are all closed.
- Closing a ticket adds a link to each pull request. On a public pull request, the back-reference is visible to people with access to the tracker, and anonymous visitors did not see it in testing.
- Project and board IDs are cached in `~/.cache/issue-tracker/` with owner-only permissions. Ticket data is read live and never stored, and each command makes few GitHub requests, as listed in [GITHUB-RATE-LIMITING.md](GITHUB-RATE-LIMITING.md). When GitHub rate limits a request, the command waits as GitHub asks, or stops and prints when to retry.
- Images and video upload through `gh` to GitHub's attachment storage, which takes nothing else. Other files, such as logs, become assets of an `attachments` prerelease in the tracker repository, so only people with tracker access can download them. A Markdown reference to an attached file's path, such as `![after](./after.png)`, is replaced with the upload.
- Files in the `attachments` prerelease stay until removed: `issue attachment rm` deletes a ticket's files, for example a log that leaked a secret, and marks their links removed; `issue attachment prune` finds files no ticket links to anymore and deletes them with `--yes`.
- Attachments download to `~/.cache/issue-tracker/attachments/<number>/`. A `--dir` inside a Git worktree must be ignored by it, so a pulled screenshot cannot reach a commit by accident.
- Lists print a table for people, with board statuses in upper case and no Project column when every ticket belongs to the project the list is scoped to, and tab-separated rows with the board's status names when `AGENT=1` is set. A table wider than the terminal shrinks its columns and wraps their text, so every line fits; `COLUMNS` overrides the detected width. Results go to stdout, errors go to stderr, and failures exit with status 1.

# Installation

Download the prebuilt binary for your platform from the [latest release](https://github.com/alexgorbatchev/issue-tracker-cli/releases/latest), replacing `X.X.X` with the version shown on that page.

```bash
# macOS (Apple Silicon)
curl -sSL https://github.com/alexgorbatchev/issue-tracker-cli/releases/latest/download/issue-tracker_X.X.X_darwin_arm64.tar.gz | tar -xz -C ~/.local/bin
```

# Setup

- [GitHub CLI](https://cli.github.com/) - Provides your GitHub login, and uploads images and video. Run `gh auth login`, then `issue-tracker auth refresh` to allow board access.
- Private tracker repository - Create an empty private repository with at least one commit, then set `repo` in the configuration below.

# Quick Start

```bash
# Register the repository you are in
issue-tracker project add

# Register one of its directories as a component
issue-tracker project add --path services/api

# Create a ticket with a screenshot
issue-tracker issue create --title "Crash on resize" --body-file bug.md --attach ./crash.png

# See what can start now
issue-tracker issue list --ready

# Claim the next ticket and read it
issue-tracker issue claim --next
issue-tracker issue inspect 12

# Read several tickets in one request
issue-tracker issue inspect 12 13 14

# Close it after the pull request merges
issue-tracker issue close 12 --pr 88
```

Sample Output:
```
[OK] claimed #12 in go-tui: Crash on resize
```

# Configuration

Settings come from flags, then environment variables, then `~/.config/issue-tracker/config.toml`, then defaults. `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected.

```toml
repo = "owner/issues"
stale_days = 3
require_private = true
```

| Setting | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `repo` | `ISSUE_TRACKER_REPO` | None, required | Tracker repository as `owner/name` |
| `stale_days` | `ISSUE_TRACKER_STALE_DAYS` | `3` | Days without activity before a Doing ticket is stale |
| `require_private` | `ISSUE_TRACKER_REQUIRE_PRIVATE` | `false` | Refuse to read a tracker that is not private |

# Options & Flags

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--version` | `-v` | `false` | Print the version |
| `--help` | `-h` | `false` | Print help for any command |

`issue-tracker issue list`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--project <name>` | | Current directory's project | Limit to a project |
| `--all-projects` | | `false` | List every project from inside a registered repository |
| `--unrouted` | | `false` | Only tickets with no project, for triage. Excludes `--project`, `--all-projects`, and `--ready` |
| `--state <state>` | | `open` | `open`, `closed`, or `all` |
| `--status <status>` | | | `Todo`, `Doing`, or `Done` |
| `--label <label>` | | | Only tickets with this label, ignoring case |
| `--priority <priority>` | | | Only tickets whose most urgent priority label is this: `P0`, `P1`, `P2`, or `P3` |
| `--search <text>` | | | Only tickets matching this search text |
| `--ready` | | `false` | Only tickets that can start now, most urgent first |
| `--stale` | | `false` | Only Doing tickets with no recent activity |
| `--stale-days <days>` | | Configured value | Stale window |
| `--limit <count>` | | `100` | Maximum tickets; `0` shows all. A warning gives the total when more tickets match |

`issue-tracker issue create`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--title <title>` | | Required | Ticket title |
| `--body <text>` | | | Ticket text |
| `--body-file <path>` | | | File holding the text; `-` reads standard input |
| `--project <name>` | | Current directory's project | Project for the ticket |
| `--attach <file>` | | | File to upload; repeatable. Also on `issue edit` and `issue comment add` |
| `--label <label>` | | | Label to apply; repeatable |
| `--parent <number>` | | | Parent ticket |
| `--discovered-from <number>` | | | Note the ticket that surfaced this work |
| `--blocked-by <numbers>` | | | Tickets that must close first |
| `--blocking <numbers>` | | | Tickets waiting on this one |

`issue-tracker issue claim`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--next` | | `false` | Claim the most urgent ready ticket, oldest first within a priority |
| `--project <name>` | | Current directory's project | Limit `--next` to a project |

`issue-tracker issue close`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--pr <ref>` | | | Merged pull request as `88` or `project#88`; repeatable |
| `--landed-pr <ref>` | | | Closed pull request whose head is on its base branch, for when GitHub does not report it as merged |
| `--reason <reason>` | | | Close without a pull request: `completed`, `not-planned`, or `duplicate` |

`issue-tracker issue attachment pull`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--dir <path>` | | `~/.cache/issue-tracker/attachments/<number>/` | Download directory |

`issue-tracker project add [owner/repo]`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--path <dir>` | | | Register this directory, relative to the top of the repository, as a component |
| `--name <name>` | | Last element of `--path` | Component name after the repository's |

`issue-tracker issue attachment prune`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--yes` | | `false` | Delete the unlinked files instead of only listing them |

`issue-tracker project rm`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--yes` | | `false` | Confirm the removal, which deletes a repository's board |

`issue-tracker project scan`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--author <login>` | | | Also trust text from this login. Repeatable |

`issue-tracker project import <owner/repo>`

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--author <login>` | | | Also trust text from this login. Repeatable |
| `--dry-run` | | `false` | Report what would move without changing anything |
| `--limit <n>` | | `0` | Maximum issues to move in this run. `0` moves all |

Run `issue-tracker --help` for every command.

# License

[MIT](LICENSE) (c) 2026 Alex Gorbatchev
