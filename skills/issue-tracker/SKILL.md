---
name: "issue-tracker"
description: "Use when managing tickets or projects with the issue-tracker CLI."
author: "Alex Gorbatchev"
metadata:
  created_on: 2026-11-01 10:52
  last_modified: 2026-11-04 14:00
  status: current
---

## Execution rules

Set `AGENT=1` on every invocation, or export it for the session. Read exit status as well as stdout: failures exit nonzero and print `ERR:` to stderr. Successful writes print one `OK:` line. Values `1`, `true`, and `yes` enable agent output, with case and surrounding whitespace ignored.

`issue-tracker` keeps every ticket as a GitHub Issue in one private tracker repository. Code and pull requests stay in their own repositories. Each registered code repository is a project, with one `repo:<name>` label and one board with the columns Todo, Doing, and Done. A ticket belongs to a project through that label or by being on its board, so a ticket created inside a board in the GitHub web UI needs no label. If the two disagree, the label wins. A ticket's status comes from its project's board only, so a ticket with no project has none; its items on other boards count toward activity and are never changed. A directory of a registered repository can be registered as a component: a project named `<repo>/<component>` with its own `repo:<repo>/<component>` label on the repository's board. The repository's board has a single-select `Component` field with one option per component. A ticket with a component's label belongs to the component; a ticket on the board with no routing label belongs to the component its `Component` value names, or else to the repository. Project-scoped lists find unlabelled board tickets through GitHub search, so a new or just-closed one can take several seconds to move in them; `issue inspect` is always current. A ticket with neither has no project: it appears in unscoped lists and in `issue list --unrouted`, is never ready, and cannot be claimed until it gets a label or a board.

## Workflow

1. Run `issue-tracker issue claim --next` to take the most urgent ready ticket, or `issue-tracker issue claim <number>` for a specific one. Claims are atomic: when another agent holds the ticket, the command fails with `ticket is already claimed`, so pick another ticket.
2. Run `issue-tracker issue inspect <number>` to read the ticket, or pass several numbers to read related tickets in one call, and `issue-tracker issue attachment pull <number>` to download its screenshots.
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
account = "my-login"
agent_account = "my-bot"
```

| Environment variable | Setting | Meaning |
| --- | --- | --- |
| `ISSUE_TRACKER_REPO` | `repo` | Tracker repository as `owner/name`. Required. |
| `ISSUE_TRACKER_STALE_DAYS` | `stale_days` | Days without activity before a Doing ticket is stale. Default 3. |
| `ISSUE_TRACKER_REQUIRE_PRIVATE` | `require_private` | When true, commands that read the tracker fail if it is not private. Default false. |
| `ISSUE_TRACKER_ACCOUNT` | `account` | Login in the GitHub CLI's keyring to act as, instead of its active account or `GH_TOKEN`. Default: unset. |
| `ISSUE_TRACKER_AGENT_ACCOUNT` | `agent_account` | Login to act as in agent mode, instead of `account`, except in `project add`, `project rm`, `project scan`, and `project import`. Default: unset. |

Authentication reuses the GitHub CLI login: `GH_TOKEN` when it is set, otherwise the active account. A configured account replaces both, so a person can write tickets as themselves while agents write as another account. Its token comes from `gh auth token --user <login>`, which fails with `reading the token of GitHub account <login>` until that login is added with `gh auth login`. Attachment uploads through `gh` use the same account. `project add`, `project rm`, `project scan`, and `project import` change what only the tracker's owner can, such as boards under the owner and issues moved out of the owner's repositories, so they act as `account`, or the GitHub CLI's active account, even when `agent_account` applies, and print `WARN: issue-tracker <command> runs as <login>, not agent account <agent login>, because only the tracker's owner can make its changes` on stderr. That account needs write access to the tracker repository and Write access to each board, granted under the board's Settings, Manage access. Boards need the `project` scope: `issue-tracker auth refresh` grants it to the GitHub CLI's active account, so to grant it to another configured account, run `gh auth switch --user <login>` first and switch back afterwards. Project IDs are cached in `$XDG_CACHE_HOME/issue-tracker/` (default `~/.cache/issue-tracker/`). Ticket data is always read live.

## Project resolution

Inside a registered repository, the current repository's git remote selects the project, so `--project` is optional. Inside a registered component's directory, or below it, the component is selected; when components are nested, the deepest one wins. Elsewhere in the repository, the repository's project is selected, and its lists, claims, and counts include its components' tickets. When `GH_REPO` is set, it names the repository and the directory is ignored. An explicit `--project` always wins. Outside a registered repository, `issue list` and `issue sub close-eligible` need `--project` or `--all-projects`, and fail naming the reason: a GitHub repository that is not registered, one whose name another owner's repository already holds, a repository on another host, a checkout with no GitHub remote, or no git checkout. See Errors. `issue claim --next` covers all projects there, and `issue create` and `project inspect` need an explicit project.

## Commands

### issue-tracker auth refresh

Runs `gh auth refresh --hostname github.com --scopes project` to add the `project` scope to the GitHub CLI login. It keeps the login's other scopes, and shows a one-time code to approve in a browser. Requires a terminal: it refuses in agent mode and when stdin is not a terminal, without starting `gh`. Prints `project board access granted` on success.

### issue-tracker project add [owner/repo]

Registers a repository, creating its board and label. Defaults to the current repository. Repeating it is safe. Fails when another owner's repository already uses the short name.

With `--path`, registers that directory of an already registered repository as a component instead. It adds the component as an option of the board's `Component` field, creating the field on first use, then creates the label `repo:<repo>/<component>`, whose description `owner/repo:<path>` records the directory. It uses the repository's board. Repeating it for a component without an option adds the option. Prints `registered project <repo>/<component> for <path> in <owner/repo>`. Repeating it is safe. Fails when the repository is not registered, when the name or path is already registered to another component, or when the label name exceeds 50 characters or its description exceeds 100.

- `--path` string: directory relative to the top of the repository, without `.`, `..`, or `:`.
- `--name` string: component name used after `<repo>/`. Defaults to the last element of `--path`. Requires `--path`.

### issue-tracker project inspect [project]

Prints `project`, `repository`, `path`, `visibility`, `board`, `open`, and `closed`. `path` is `.` for a whole repository. A repository's counts include its components' tickets. Defaults to the current repository's project.

### issue-tracker project list

Prints one tab-separated line per project: name, repository, visibility, board URL, path. The path is `.` for a whole repository. A component shows its repository's board.

### issue-tracker project rm <project>

Unregisters a project with no open tickets and keeps the label on closed tickets. For a repository, labels closed tickets that were only on the board, then deletes the board. A component's board belongs to its repository and stays; its `Component` option is deleted, and the field with the last option, so tickets that named it belong to the repository. Fails for a repository with registered components until they are removed.

- `--yes` bool: confirm the removal. Required.

### issue-tracker project scan

Prints one tab-separated line for each of the tracker owner's unarchived, non-fork repositories with open issues: repository, visibility, open, can move, from others. Reads logins and counts only, never issue text, and leaves out the tracker repository.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.

An issue can move when its repository is private, or when the issue and all its comments come from trusted authors. A deleted author, or more than 100 comments, counts as text from other people. In a private repository, such an issue counts in both `can move` and `from others`.

### issue-tracker project import <owner/repo>

Moves a repository's open issues into the tracker, oldest first, registering the project when needed. Prints `moved owner/repo#<source> to #<ticket>` for each issue, then a summary with the counts moved, left to move, and skipped. Issues from other people stay where they are. A public issue's authors are reread just before its transfer, and it stays if someone else has commented since the run began. Moves one issue every 32 seconds, so a large repository takes hours. Rerunning continues with what is left. Fails when the tracker is not private, when the repository is the tracker, or when the repository has a different owner than the tracker.

GitHub rewrites `#N` references in a moved issue's body and comments to `owner/repo#N`, so they keep pointing at the source repository. A public issue cannot be moved back.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.
- `--dry-run` bool: report the counts without changing anything.
- `--limit` int: maximum issues to move in this run. 0 moves all. Default: 0.

### issue-tracker issue list

Prints one tab-separated line per ticket, oldest first: number, status, priority, project, comma-separated labels, title. With `--ready`, tickets come most urgent first and oldest first within a priority. A stale ticket's status is followed by ` (stale)`. The priority column holds `P0` to `P3`, or nothing, and priority labels are left out of the labels column. See Priority.

- `--project` string: limit to a project. Defaults to the current repository's project.
- `--all-projects` bool: list every project. Required outside a registered repository unless `--project` or `--unrouted` is given.
- `--unrouted` bool: only tickets with no project, for triage. Reads one search page per 100 tickets with no routing label, whatever the limit. Cannot be combined with `--project`, `--all-projects`, or `--ready`.
- `--state` string: `open`, `closed`, or `all`. Default: open.
- `--status` string: `Todo`, `Doing`, or `Done`.
- `--label` string: only tickets with this label, ignoring case as GitHub does.
- `--priority` string: only tickets whose priority is this: `P0`, `P1`, `P2`, or `P3`, ignoring case.
- `--search` string: only tickets matching this GitHub search text in their title, body, or comments. GitHub serves 1,000 results per search query, so a longer result continues with another query from the last creation time, and `--limit 0` delivers every match once. The one exception is more than 1,000 matches created in the same second, which fails with the error listed under Errors and prints no tickets. Leave `sort:` out of the search text, because continuing depends on creation order.
- `--ready` bool: only tickets that can be picked up now: with a project, open, without the `blocked` label, with no open blockers, no blocked parent or grandparent, no open sub-issues, and not in Doing or Done. A parent with open sub-issues is left out so that its children get claimed instead; `issue claim <number>` can still claim it. The `blocked` label applies only to the ticket that carries it; a parent or grandparent counts as blocked only through open blockers.
- `--stale` bool: only Doing tickets with no ticket or board activity within the stale window.
- `--stale-days` int: stale window in days, overriding the configured value.
- `--limit` int: maximum tickets. 0 shows all, and a negative value is an error. Default: 100. `--status`, `--label`, `--priority`, `--ready`, and `--stale` read every ticket whatever the limit, at one request per 100 tickets. When more tickets match, stdout holds the first `<limit>` and stderr gets `WARN: showing the first <limit> of <total> tickets: pass --limit 0 to list them all`.

### issue-tracker issue inspect <number...>

Reads up to 50 tickets in one request, in the order given; a repeated number is shown once. For each ticket, prints `number`, `title`, `state`, `status`, `project`, `url`, then `priority`, `labels`, `parent`, `blocked_by`, `blocking`, `sub_issue`, and `attachment` lines when present, then `body:` followed by the body, then each comment as `comment: <author> <time>` followed by its text. A blank line separates tickets. The `WARN:` line for a public project comes first, once per project, followed by a `WARN:` line for each ticket with more than one priority label.

### issue-tracker issue create

Creates a ticket on its project's board with status Todo. If a later step fails, the error names the created ticket, so finish that ticket instead of creating another.

- `--title` string: ticket title. Required.
- `--body` string: ticket text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--project` string: project. Defaults to the current repository's project.
- `--attach` stringArray: file to upload. Repeatable. See Attachments.
- `--label` stringArray: label to apply. Repeatable.
- `--parent` int: parent ticket number.
- `--discovered-from` int: adds a line naming the ticket that surfaced this work. It does not block anything.
- `--blocked-by` intSlice: tickets that must close first. Repeatable.
- `--blocking` intSlice: tickets waiting on this one. Repeatable.

### issue-tracker issue edit <number>

Changes the title or body, and attaches files to the body. Attaching without `--body` or `--body-file` appends the files to the current body.

- `--title` string: new title.
- `--body` string: new text.
- `--body-file` string: file holding the new text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--attach` stringArray: file to upload. Repeatable. See Attachments.

### issue-tracker issue claim [number]

Atomically takes a ticket and sets it to Doing. Pass a number or `--next`. Claims never expire: they end with `issue close` or `issue release`.

- `--next` bool: claim the most urgent ready ticket, oldest first within a priority, moving past tickets other agents win. Prints `OK: no ready tickets` when none remain.
- `--project` string: limit `--next` to a project. Defaults to the current repository's project.

### issue-tracker issue release <number>

Gives up a claim and returns the ticket to Todo.

### issue-tracker issue close <number>

Closes a ticket, sets it to Done, and removes its claim. Pass one of `--pr`, `--landed-pr`, or `--reason`.

- `--pr` stringArray: merged pull request. Repeatable. A bare number belongs to the ticket's project; `project#number` names another registered project. The command refuses to close when any pull request is unmerged. Links to the pull requests are added to the ticket.
- `--landed-pr` string: closed pull request that GitHub does not report as merged, as a number or `project#number`. The command closes the ticket as completed only when the pull request's head commit is on its base branch, and records the pull request and commit on the ticket. It refuses an open pull request, a merged one, which takes `--pr`, and a head that is not on the base branch.
- `--reason` string: close without a pull request: `completed`, `not-planned`, or `duplicate`.

### issue-tracker issue reopen <number>

Reopens a ticket and sets it to Todo.

### issue-tracker issue move <number> <project>

Reassigns a ticket to another project and keeps its status. Moving between a repository and its components keeps the board item: a ticket routed by the board changes only its `Component` value, and a labelled ticket changes its label and its `Component` value. `issue create` sets the `Component` value of a component's ticket. A ticket already in that project with no board status gets Todo, which finishes an import that failed partway.

### issue-tracker issue label add <number> <label...>

Adds labels. GitHub creates a label it does not know yet. `repo:` labels are refused, because `issue move` owns them.

### issue-tracker issue label rm <number> <label...>

Removes labels.

### issue-tracker issue status set <number> <status>

Moves an unclaimed ticket to `Todo` or `Done`. Doing belongs to `issue claim`, and a claimed ticket changes through `issue release` or `issue close`.

### issue-tracker issue comment add <number>

Adds a comment. Needs text, files, or both.

- `--body` string: comment text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--attach` stringArray: file to upload. Repeatable. See Attachments.

### issue-tracker issue attachment add <number> <file...>

Uploads files and appends them to the ticket's body. See Attachments.

### issue-tracker issue attachment rm <number> <file...>

Deletes files of that ticket from the attachments release, then replaces their links in its body and comments with `(attachment removed)`, keeping a link's text. Takes asset names, such as `626-ddd5c89e-run.log`, or their links. Deletes nothing unless every file belongs to the ticket and is in the release. Images and video in GitHub's attachment storage cannot be deleted through the API; remove their links with `issue edit`. Use it at once when an attached file leaks a secret. Prints `removed <n> file(s) from #<number>`.

### issue-tracker issue attachment prune

Prints the names of attachment release files that their ticket no longer links to, one per line, which a failed write after an upload or an edit that dropped a link leaves behind. A file counts as linked when a link to it appears in its ticket's body or comments. Without `--yes`, deletes nothing and warns with the count. Files whose names this tool did not write are never listed. Reads tickets 50 per request.

- `--yes` bool: delete the listed files.

### issue-tracker issue attachment pull <number>

Downloads the images, videos, and files linked from a ticket's body and comments and prints one path per line. Files kept in the attachments release are listed once per run, at one request per 100 assets in it.

- `--dir` string: download directory. Defaults to `~/.cache/issue-tracker/attachments/<number>/`. A directory inside a Git worktree must be ignored by it.

### issue-tracker issue blocker add <number> <blocker...>

Marks a ticket blocked by other tickets.

### issue-tracker issue blocker rm <number> <blocker...>

Removes blockers.

### issue-tracker issue sub add <parent> <child...>

Attaches sub-issues to a parent.

### issue-tracker issue sub rm <parent> <child...>

Detaches sub-issues.

### issue-tracker issue sub list <parent>

Shows a parent's sub-issues and progress. Prints one tab-separated line per sub-issue: number, state, title. Then prints `#<parent>: <done> of <total> done, <open> open`. A sub-issue closed for any reason counts as done.

### issue-tracker issue sub close-eligible

Closes open parents whose sub-issues are all closed, as completed, with no pull request check. Prints `closed #<number> in <project>: <title>` for each, or `no eligible parents`. Skips a parent that is in Doing, has open blockers, or has the `blocked` label. Each parent is read again just before closing; one that stopped being eligible prints `skipped #<number>: no longer eligible`. A failed close stops the command. Inside a registered repository it covers that project; elsewhere it needs `--project` or `--all-projects`.

- `--all-projects` bool: cover every project. Required outside a registered repository unless `--project` is given.
- `--dry-run` bool: print `would close #<number> in <project>: <title>` for each parent and close nothing.
- `--project` string: limit to a project.

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

## Priority

A `priority:P0`, `priority:P1`, `priority:P2`, or `priority:P3` label sets a ticket's priority, with `P0` the most urgent; label names ignore case, and any other `priority:` label is an ordinary label. `issue list --ready` and `issue claim --next` take the most urgent tickets first, then the oldest, and a ticket with no priority label comes after `P3`. A ticket with more than one priority label ranks by its most urgent one, and `issue list` (on stderr) and `issue inspect` print `WARN: #<number> has priorities <P0, P2>: ranked as <P0>` until the extra labels are removed with `issue label rm`. Set a priority with `issue label add <number> priority:P1`.

## Attachments

`--attach` on `issue create`, `issue edit`, and `issue comment add`, and `issue attachment add`, take any file:

- Images and video (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.svg`, `.mp4`, `.mov`, `.webm`) upload through the `gh` binary to GitHub's attachment storage.
- Every other file, such as logs, traces, and archives, becomes an asset of the tracker's `attachments` prerelease, created on first use. Only people with access to the tracker can download it.
- A Markdown image or link whose target is the path passed to `--attach` is pointed at the upload, so images sit where the text discusses them. A file the text does not reference is appended. For `--attach ./after.png --attach ./run.log`:

```markdown
The panel overflows after resizing:

![after resize](./after.png)

Full output: [run log](./run.log)
```

Put long output such as logs in a file and attach it rather than pasting it into a body.

## Errors

- `pull request <repo>#<number> head <sha> is not on <branch>: refusing to close`: the code did not land. Land it, or close with `--reason` if the work is abandoned.
- `pull request <repo>#<number> is still open: refusing to close`: land it and use `--pr`.
- `pull request <repo>#<number> is merged: use --pr`: GitHub reports the merge, so `--landed-pr` is not needed.
- `listing tickets: more than 1000 tickets were created at <time>: search cannot page past them`: narrow the search text.
- `ticket #<number> not found in <tracker>` or `tickets #<number>, #<number> not found in <tracker>`: no ticket has that number, and `issue inspect` printed nothing. Check the numbers and rerun.
- `accepts between 1 and 50 arg(s)`: `issue inspect` takes at most 50 tickets. Split the numbers across calls.
- `body is <n> characters, over GitHub's 65536: attach long output as a file with --attach`: GitHub caps a ticket or comment body at 65,536 characters. Nothing was sent. Shorten the text, or move long output such as logs into a file and attach it.
- `ticket is already claimed`: another agent holds the ticket. Pick another.
- `<owner/repo> is not a registered project: register it with issue-tracker project add <owner/repo>, or pass --all-projects for every project`: `issue list` or `issue sub close-eligible` ran in a repository that is not a project. Pass `--project` or `--all-projects`, or ask the user whether to register it.
- `<owner/repo> is not a registered project, and cannot be: project <name> already belongs to <other/repo>: pass --project <name>, or --all-projects for every project`: projects are named after their repository, and another owner's repository of that name holds it. Pass `--project` or `--all-projects`.
- `not inside a registered repository: <reason>: pass --project <name>, or --all-projects for every project`: `issue list` or `issue sub close-eligible` ran where no github.com repository was found. The reason is `no git checkout here`, `this checkout has no git remote`, `no git remote points to a GitHub host the GitHub CLI is logged in to`, `<host>/<owner>/<repo> is on <host>, and projects are github.com repositories`, or, for any other failure, git's or the GitHub CLI's own reason on one line, such as a missing git or a checkout git refuses under its `safe.directory` rule. Pass one of those flags, or ask the user to fix git first when the reason is about git itself.
- `ticket #<number> has no project: give it a repo: label on GitHub or run issue move <number> <project>`: the ticket is unrouted, and no claim was created. Ask the user which project owns it, or pick another ticket.
- `GitHub rate limit reached: retry after <time>`: wait until that time.
- `your GitHub login has no access to project boards: run issue-tracker auth refresh`: the token lacks the `project` scope. Ask the user to run `issue-tracker auth refresh` in a terminal.
- `GitHub account <login> has no access to project boards: run gh auth switch --user <login> and issue-tracker auth refresh, then switch back`: the configured account lacks the `project` scope. Ask the user to run those commands in a terminal.
- `reading the token of GitHub account <login>`: the configured account is not logged in to the GitHub CLI, or `gh` printed no token for it. Ask the user to run `gh auth login --hostname github.com` for it, or fix the `account` or `agent_account` setting.
- `auth refresh needs a person at a terminal to approve the login in a browser`: `auth refresh` ran in agent mode or without a terminal. Ask the user to run it.
- `repository <owner/repo> is not registered: run project add <owner/repo> first`: `project add --path` needs the repository registered. Run the suggested command, then repeat.
- `project "<repo>" has components: remove <repo>/<component> first`: `project rm` keeps a repository while components use its board. Remove each component first.
- `tracker <repo> is not private`: `require_private` is on and the tracker is not private.
- `moved to ticket #<number>, finish it with issue move: ticket is on the board, but its item cannot be read yet`: `project import` moved the issue, and the ticket has its label but no status. Wait a few seconds, run `issue move <number> <project>`, then rerun the import for the rest.
- `ticket #<number> was created but linking it failed: ticket is on the board, but its item cannot be read yet`: the ticket exists and has no status, and its `--blocked-by`, `--blocking`, `--label`, and `--attach` values were not applied. Keep that ticket instead of running `issue create` again. Wait a few seconds, run `issue status set <number> Todo`, then apply the rest with `issue blocker add`, `issue label add`, and `issue attachment add`.
