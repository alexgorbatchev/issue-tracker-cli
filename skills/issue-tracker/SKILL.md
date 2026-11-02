---
name: "issue-tracker"
description: "Use when managing tickets or projects with the issue-tracker CLI."
author: "Alex Gorbatchev"
metadata:
  created_on: 2026-11-01 10:52
  last_modified: 2026-11-01 23:45
  status: current
---

## Execution rules

Set `AGENT=1` on every invocation, or export it for the session. Read exit status as well as stdout: failures exit nonzero and print `ERR:` to stderr. Successful writes print one `OK:` line. Values `1`, `true`, and `yes` enable agent output, with case and surrounding whitespace ignored.

`issue-tracker` keeps every ticket as a GitHub Issue in one private tracker repository. Code and pull requests stay in their own repositories. Each registered code repository is a project, with one `repo:<name>` label and one board with the columns Todo, Doing, and Done.

## Workflow

1. Run `issue-tracker issue claim --next` to take the oldest ready ticket, or `issue-tracker issue claim <number>` for a specific one. Claims are atomic: when another agent holds the ticket, the command fails with `ticket is already claimed`, so pick another ticket.
2. Run `issue-tracker issue inspect <number>` to read the ticket, and `issue-tracker issue attachment pull <number>` to download its screenshots.
3. Work in the repository named by the ticket's project. A claim works from any directory, so a ticket for a dependency can be claimed from the application that needs it.
4. Record unrelated problems with `issue-tracker issue create --title "..." --discovered-from <number>`.
5. Open the pull request in the code repository.
6. After the merge, run `issue-tracker issue close <number> --pr <pull-request>`.
7. To stop without finishing, run `issue-tracker issue release <number>`.

Read and follow the `WARN:` line printed for public projects: keep ticket text, attachments, ticket numbers, and the tracker repository name out of that project's commits, pull requests, and branch names.

## Configuration

Settings come from flags, then environment variables, then `$XDG_CONFIG_HOME/issue-tracker/config.toml` (default `~/.config/issue-tracker/config.toml`), then defaults.

```toml
repo = "owner/name"
stale_days = 3
require_private = false
```

| Environment variable | Setting | Meaning |
| --- | --- | --- |
| `ISSUE_TRACKER_REPO` | `repo` | Tracker repository as `owner/name`. Required. |
| `ISSUE_TRACKER_STALE_DAYS` | `stale_days` | Days without activity before a Doing ticket is stale. Default 3. |
| `ISSUE_TRACKER_REQUIRE_PRIVATE` | `require_private` | When true, commands that read the tracker fail if it is not private. Default false. |

Authentication reuses the GitHub CLI login. Boards need the `project` scope: `gh auth refresh -s project`. Project IDs are cached in `$XDG_CACHE_HOME/issue-tracker/` (default `~/.cache/issue-tracker/`). Ticket data is always read live.

## Project resolution

Inside a registered repository, the current repository's git remote selects the project, so `--project` is optional. An explicit `--project` always wins. Outside a registered repository, `issue list` covers all projects, and `issue create` and `project inspect` need an explicit project.

## Commands

### issue-tracker project add [owner/repo]

Registers a repository, creating its board and label. Defaults to the current repository. Repeating it is safe. Fails when another owner's repository already uses the short name.

### issue-tracker project inspect [project]

Prints `project`, `repository`, `visibility`, `board`, `open`, and `closed`. Defaults to the current repository's project.

### issue-tracker project list

Prints one tab-separated line per project: name, repository, visibility, board URL.

### issue-tracker project rm <project>

Unregisters a project with no open tickets. Deletes its board and keeps the label on closed tickets.

- `--yes` bool: confirm deleting the board. Required.

### issue-tracker project scan

Prints one tab-separated line for each of the tracker owner's unarchived, non-fork repositories with open issues: repository, visibility, open, can move, from others. Reads logins and counts only, never issue text, and leaves out the tracker repository.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.

An issue can move when its repository is private, or when the issue and all its comments come from trusted authors. A deleted author, or more than 100 comments, counts as text from other people. In a private repository, such an issue counts in both `can move` and `from others`.

### issue-tracker project import <owner/repo>

Moves a repository's open issues into the tracker, oldest first, registering the project when needed. Prints `moved owner/repo#<source> to #<ticket>` for each issue, then a summary with the counts moved, left to move, and skipped. Issues from other people stay where they are. A public issue's authors are reread just before its transfer, and it stays if someone else has commented since the run began. Moves one issue every 24 seconds, so a large repository takes hours. Rerunning continues with what is left. Fails when the tracker is not private, when the repository is the tracker, or when the repository has a different owner than the tracker.

GitHub rewrites `#N` references in a moved issue's body and comments to `owner/repo#N`, so they keep pointing at the source repository. A public issue cannot be moved back.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.
- `--dry-run` bool: report the counts without changing anything.
- `--limit` int: maximum issues to move in this run. 0 moves all. Default: 0.

### issue-tracker issue list

Prints one tab-separated line per ticket, oldest first: number, status, project, comma-separated labels, title. A stale ticket's status is followed by ` (stale)`.

- `--project` string: limit to a project. Defaults to the current repository's project.
- `--all-projects` bool: list every project from inside a registered repository.
- `--state` string: `open`, `closed`, or `all`. Default: open.
- `--status` string: `Todo`, `Doing`, or `Done`.
- `--label` string: only tickets with this label.
- `--search` string: only tickets matching this GitHub search text in their title, body, or comments.
- `--ready` bool: only tickets that can be picked up now: open, with no open blockers, no blocked parent or grandparent, and not in Doing or Done. A parent stays ready while its sub-issues are open.
- `--stale` bool: only Doing tickets with no ticket or board activity within the stale window.
- `--stale-days` int: stale window in days, overriding the configured value.
- `--limit` int: maximum tickets. 0 shows all. Default: 100.

### issue-tracker issue inspect <number>

Prints `number`, `title`, `state`, `status`, `project`, `url`, then `labels`, `parent`, `blocked_by`, `blocking`, `sub_issue`, and `attachment` lines when present, then `body:` followed by the body, then each comment as `comment: <author> <time>` followed by its text.

### issue-tracker issue create

Creates a ticket on its project's board with status Todo. If a later step fails, the error names the created ticket, so finish that ticket instead of creating another.

- `--title` string: ticket title. Required.
- `--body` string: ticket text.
- `--body-file` string: file holding the text; `-` reads standard input.
- `--project` string: project. Defaults to the current repository's project.
- `--attach` stringArray: image or video to upload. Repeatable.
- `--label` stringArray: label to apply. Repeatable.
- `--parent` int: parent ticket number.
- `--discovered-from` int: adds a line naming the ticket that surfaced this work. It does not block anything.
- `--blocked-by` intSlice: tickets that must close first. Repeatable.
- `--blocking` intSlice: tickets waiting on this one. Repeatable.

### issue-tracker issue edit <number>

- `--title` string: new title.
- `--body` string: new text.
- `--body-file` string: file holding the new text; `-` reads standard input.

### issue-tracker issue claim [number]

Atomically takes a ticket and sets it to Doing. Pass a number or `--next`. Claims never expire: they end with `issue close` or `issue release`.

- `--next` bool: claim the oldest ready ticket, moving past tickets other agents win. Prints `OK: no ready tickets` when none remain.
- `--project` string: limit `--next` to a project. Defaults to the current repository's project.

### issue-tracker issue release <number>

Gives up a claim and returns the ticket to Todo.

### issue-tracker issue close <number>

Closes a ticket, sets it to Done, and removes its claim. Pass `--pr` or `--reason`.

- `--pr` stringArray: merged pull request. Repeatable. A bare number belongs to the ticket's project; `project#number` names another registered project. The command refuses to close when any pull request is unmerged. Links to the pull requests are added to the ticket.
- `--reason` string: close without a pull request: `completed`, `not-planned`, or `duplicate`.

### issue-tracker issue reopen <number>

Reopens a ticket and sets it to Todo.

### issue-tracker issue move <number> <project>

Reassigns a ticket to another project and keeps its status.

### issue-tracker issue label add <number> <label...>

Adds labels. GitHub creates a label it does not know yet. `repo:` labels are refused, because `issue move` owns them.

### issue-tracker issue label rm <number> <label...>

Removes labels.

### issue-tracker issue status set <number> <status>

Moves an unclaimed ticket to `Todo` or `Done`. Doing belongs to `issue claim`, and a claimed ticket changes through `issue release` or `issue close`.

### issue-tracker issue comment add <number>

- `--body` string: comment text.
- `--body-file` string: file holding the text; `-` reads standard input.

### issue-tracker issue attachment add <number> <file...>

Uploads images or videos and appends them to the ticket. Requires the `gh` binary.

### issue-tracker issue attachment pull <number>

Downloads a ticket's attachments and prints one path per line.

- `--dir` string: download directory. Defaults to `~/.cache/issue-tracker/attachments/<number>/`. A directory inside a Git worktree must be ignored by it.

### issue-tracker issue blocker add <number> <blocker...>

Marks a ticket blocked by other tickets.

### issue-tracker issue blocker rm <number> <blocker...>

Removes blockers.

### issue-tracker issue sub add <parent> <child...>

Attaches sub-issues to a parent.

### issue-tracker issue sub rm <parent> <child...>

Detaches sub-issues.

### issue-tracker skill

Prints how to install this skill. It takes no arguments.

### issue-tracker completion bash

Generates the bash completion script. `--no-descriptions` bool leaves out descriptions.

### issue-tracker completion fish

Generates the fish completion script. `--no-descriptions` bool leaves out descriptions.

### issue-tracker completion powershell

Generates the PowerShell completion script. `--no-descriptions` bool leaves out descriptions.

### issue-tracker completion zsh

Generates the zsh completion script. `--no-descriptions` bool leaves out descriptions.

### issue-tracker help [command]

Prints help for a command. `--help`, `-h` bool prints help on any command, and `--version`, `-v` bool prints the version.

## Errors

- `ticket is already claimed`: another agent holds the ticket. Pick another.
- `GitHub rate limit reached: retry after <time>`: wait until that time.
- `grant board access with gh auth refresh -s project`: the token lacks the `project` scope.
- `tracker <repo> is not private`: `require_private` is on and the tracker is not private.
