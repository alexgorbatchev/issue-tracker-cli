---
name: "issue-tracker"
description: "Use when managing tickets or projects with the issue-tracker CLI."
author: "Alex Gorbatchev"
metadata:
  created_on: 2026-11-01 10:52
  last_modified: 2026-11-09 16:08
  status: current
---

## Execution rules

Set `AGENT=1` on every invocation, or export it for the session. Read exit status as well as stdout: failures exit nonzero and print `ERR:` to stderr. Successful writes print one `OK:` line. Values `1`, `true`, and `yes` enable agent output, with case and surrounding whitespace ignored.

`issue-tracker` keeps every ticket as a GitHub Issue in one private tracker repository. Code and pull requests stay in their own repositories. Each registered code repository is a project, with one `repo:<name>` label and one board with the columns Todo, Doing, and Done. A project is named after its repository: `<name>` when the tracker's owner owns it, and `<owner>/<name>` for any other owner, so repositories of the same name under different owners can both be projects. A ticket belongs to a project through that label or by being on its board, so a ticket created inside a board in the GitHub web UI needs no label. If the two disagree, the label wins. A ticket's status comes from its project's board only, so a ticket with no project has none; its items on other boards count toward activity and are never changed. A directory of a registered repository can be registered as a component: a project named `<repo>/<component>`, after its repository's project name, as in `api/docs` or `other/api/docs`, with its own `repo:<repo>/<component>` label on the repository's board. The repository's board has a single-select `Component` field with one option per component. A ticket with a component's label belongs to the component; a ticket on the board with no routing label belongs to the component its `Component` value names, or else to the repository. Project-scoped lists find unlabelled board tickets through GitHub search, so a new or just-closed one can take several seconds to move in them; `issue inspect` is always current. A ticket with neither has no project: it appears in unscoped lists and in `issue list --unrouted`, is never ready, and cannot be claimed until it gets a label or a board.

## Workflow

1. Run `issue-tracker issue claim --next` to take the most urgent ready ticket, or `issue-tracker issue claim <number>` for a specific one. Claims are atomic: when another agent holds the ticket, the command fails with `ticket is already claimed`, so pick another ticket.
2. Run `issue-tracker issue inspect <number>` to read the ticket, or pass several numbers to read related tickets in one call, and `issue-tracker issue attachment pull <number>` to download its screenshots.
3. Work in the repository named by the ticket's project. A claim works from any directory, so a ticket for a dependency can be claimed from the application that needs it.
4. Record unrelated problems with `issue-tracker issue create --title "..." --discovered-from <number>`.
5. Open the pull request in the code repository.
6. After the merge, run `issue-tracker issue close <number> --pr <pull-request>`.
7. To stop without finishing, run `issue-tracker issue release <number>`.

To hand unfinished work to the next agent, push the branch, then run `issue-tracker handoff create --title "..." --body-file notes.md` in its checkout; it records the current branch and `HEAD`. A project holds one open handoff. The receiving agent runs `issue-tracker handoff take`, which claims and closes the handoff in one step and prints the branch and commit to resume from. An agent that took a handoff it cannot work on runs `issue-tracker handoff untake <number>` to give it back. Run `issue-tracker handoff show` to read a handoff without taking it, and `issue-tracker handoff drop --reason "..."` to close one nobody should take.

To plan work that takes several tickets, run `issue-tracker epic create --title "..."`, then `issue-tracker issue create --epic <number>` for each piece of work. Agents claim an epic's tickets, never the epic, and closing its last open ticket closes the epic. Run `issue-tracker epic inspect <number>` to see an epic's tickets and progress. An epic holds at most 100 tickets, GitHub's limit per parent, so split larger work into several epics. An epic holds tickets only: an epic never belongs to another ticket, and none of its tickets is an epic.

Read and follow the `WARN:` line printed for public projects: keep ticket text, attachments, ticket numbers, and the tracker repository name out of that project's commits, pull requests, and branch names.

Visibility checks treat a missing or access-denied repository, including one blocked by organization SAML enforcement, as public for warnings. Other repositories' visibility results remain usable, so an unrelated denial leaves accessible project operations available. Errors reading tracker metadata, accessing the requested repository or board, authenticating, or handling rate limits still fail the operation.

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
| `ISSUE_TRACKER_AGENT_ACCOUNT` | `agent_account` | Login to act as in agent mode, instead of `account`, except in `project add`, `project rm`, `project sync`, `project scan`, and `project import`. Default: unset. |

Authentication reuses the GitHub CLI login: `GH_TOKEN` when it is set, otherwise the active account. A configured account replaces both, so a person can write tickets as themselves while agents write as another account. Its token comes from `gh auth token --user <login>`, which fails with `reading the token of GitHub account <login>` until that login is added with `gh auth login`. Attachment uploads through `gh` use the same account. `project add`, `project rm`, `project sync`, `project scan`, and `project import` change what only the tracker's owner can, such as boards under the owner and issues moved out of the owner's repositories, so they act as `account`, or else the default login (`GH_TOKEN`, or the GitHub CLI's active account), even when `agent_account` applies, and print `WARN: issue-tracker <command> runs as <login>, not agent account <agent login>, because only the tracker's owner can make its changes` on stderr. That account needs write access to the tracker repository and Write access to each board, granted under the board's Settings, Manage access. Boards need the `project` scope: `issue-tracker auth refresh` grants it to the GitHub CLI's active account, so to grant it to another configured account, run `gh auth switch --user <login>` first and switch back afterwards. Project IDs are cached in `$XDG_CACHE_HOME/issue-tracker/` (default `~/.cache/issue-tracker/`). Ticket data is always read live.

Metadata caches are isolated by tracker repository and effective authentication credential. Switching accounts or rotating a token starts a fresh cache; returning to the same credential reuses its cache without an identity lookup. Cache filenames use a SHA-256 digest, and cached files contain metadata only. Repository-only caches from older versions remain untouched and are ignored.

## Project resolution

Inside a registered repository, the current repository's git remote selects the project, so `--project` is optional. Inside a registered component's directory, or below it, the component is selected; when components are nested, the deepest one wins. Elsewhere in the repository, the repository's project is selected, and its lists, claims, and counts include its components' tickets. When `GH_REPO` is set, it names the repository and the directory is ignored. An explicit `--project` always wins. Outside a registered repository, `issue list`, `issue claim --next`, `epic list`, and `epic close-eligible` need `--project` or `--all-projects`, and `issue create`, `epic create`, and `project inspect` need an explicit project. They fail naming the reason: a GitHub repository that is not registered, one whose name another owner's repository already holds, a repository on another host, a checkout with no GitHub remote, or no git checkout. Each error ends with how that command takes a project. See Errors.

## Commands

### issue-tracker auth refresh

Runs `gh auth refresh --hostname github.com --scopes project` to add the `project` scope to the GitHub CLI login. It keeps the login's other scopes, and shows a one-time code to approve in a browser. Requires a terminal: it refuses in agent mode and when stdin is not a terminal, without starting `gh`. Prints `project board access granted` on success.

### issue-tracker project add [owner/repo]

Registers a repository, creating its board and label. Defaults to the current repository. The board's description is set as `project sync` sets it. Repeating it is safe, and a repository already registered under another name keeps it. The project is named `<name>` for a repository of the tracker's owner and `<owner>/<name>` otherwise. Fails when that name is already registered for another repository or as a component, when its label holds tickets of another removed project or registration (see Errors), or when the label name exceeds 50 characters. A label that holds tickets is taken back only by the repository it was registered for, ignoring case; one with no tickets is reused. Nothing is written before these checks pass.

With `--path`, registers that directory of an already registered repository as a component instead. It adds the component as an option of the board's `Component` field, creating the field on first use, then creates the label `repo:<repo>/<component>`, whose description `owner/repo:<path>` records the directory. It uses the repository's board. Repeating it for a component without an option adds the option. Prints `registered project <repo>/<component> for <path> in <owner/repo>`. Repeating it is safe. Fails when the repository is not registered, when the name or path is already registered to another component, when another owner's repository is registered under that name, when its label holds tickets of another removed project or registration, or when the label name exceeds 50 characters or its description, with the `removed: ` that `project rm` would add, exceeds 100.

- `--path` string: directory relative to the top of the repository, without `.`, `..`, or `:`.
- `--name` string: component name used after `<repo>/`. Defaults to the last element of `--path`. Requires `--path`.

### issue-tracker project inspect [project]

Prints `project`, `repository`, `path`, `visibility`, `board`, `open`, and `closed`. `path` is `.` for a whole repository. A repository's counts include its components' tickets. Defaults to the current repository's project.

### issue-tracker project list

Prints one tab-separated line per project: name, repository, visibility, board URL, path. The path is `.` for a whole repository. A component shows its repository's board.

### issue-tracker project rm <project>

Unregisters a project with no open tickets and keeps the label on closed tickets, with the description `removed: <owner/repo>`, or `removed: <owner/repo>:<path>` for a component, so that only that registration can take the label back. A component registered before removals were recorded whose `removed: <owner/repo>:<path>` would exceed GitHub's 100 characters keeps `removed: <owner/repo>` instead, and any component of that repository under the same name can take it back. For a repository, labels closed tickets that were only on the board, then deletes the board. A component's board belongs to its repository and stays; its `Component` option is deleted, and the field with the last option, so tickets that named it belong to the repository. Fails for a repository with registered components until they are removed.

- `--yes` bool: confirm the removal. Required.

### issue-tracker project sync [project]

Sets a project board's description to its repository's description, followed by a blank line and the repository's URL. A description too long for GitHub's 300-character limit is cut and ends with `…`, keeping the URL; a repository without a description gives just the URL. Boards that already match are left unchanged. A component shares its repository's board, so naming one syncs that board. Defaults to the current repository's project. Prints `updated <n> of <total> board descriptions`. When a project has no board or its repository is not found, the other boards are still synced, and the command then fails with `could not sync <n> projects: ` followed by each reason.

- `--all-projects` bool: sync the board of every project. Cannot be combined with a project name.

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

Prints one tab-separated line per ticket, oldest first: number, status, priority, project, comma-separated labels, title. With `--ready`, tickets come most urgent first; within a priority, the tickets of an epic that has a ticket in Doing come first, then the oldest. A stale ticket's status is followed by ` (stale)`. The priority column holds `P0` to `P3`, or nothing, and priority labels are left out of the labels column. See Priority.

- `--project` string: limit to a project. Defaults to the current repository's project.
- `--all-projects` bool: list every project. Required outside a registered repository unless `--project` or `--unrouted` is given.
- `--unrouted` bool: only tickets with no project, for triage. Reads one search page per 100 tickets with no routing label, whatever the limit. Cannot be combined with `--project`, `--all-projects`, or `--ready`.
- `--state` string: `open`, `closed`, or `all`. Default: open.
- `--status` string: `Todo`, `Doing`, or `Done`.
- `--label` string: only tickets with this label, ignoring case as GitHub does.
- `--priority` string: only tickets whose priority is this: `P0`, `P1`, `P2`, or `P3`, ignoring case.
- `--search` string: only tickets matching this GitHub search text in their title, body, or comments. GitHub serves 1,000 results per search query, so a longer result continues with another query from the last creation time, and `--limit 0` delivers every match once. The one exception is more than 1,000 matches created in the same second, which fails with the error listed under Errors and prints no tickets. Leave `sort:` out of the search text, because continuing depends on creation order.
- `--ready` bool: only tickets that can be picked up now: with a project, open, without the `blocked` label, with no open blockers, no blocked parent or grandparent, no open sub-issues, not in Doing or Done, not a handoff, and not an epic. An epic is left out even before it has tickets, and a parent with open sub-issues so that its children get claimed instead; `issue claim <number>` can still claim a parent that is not an epic. An epic counts as active when one of its tickets among the tickets read is in Doing, so `--project` sees only that project's tickets of an epic. The `blocked` label applies only to the ticket that carries it; a parent or grandparent counts as blocked only through open blockers.
- `--handoff` bool: only handoffs, the tickets with the `handoff` label. Cannot be combined with `--ready`.
- `--stale` bool: only Doing tickets with no ticket or board activity within the stale window.
- `--stale-days` int: stale window in days, overriding the configured value.
- `--limit` int: maximum tickets. 0 shows all, and a negative value is an error. Default: 100. `--status`, `--label`, `--priority`, `--ready`, `--handoff`, and `--stale` read every ticket whatever the limit, at one request per 100 tickets. When more tickets match, stdout holds the first `<limit>` and stderr gets `WARN: showing the first <limit> of <total> tickets: pass --limit 0 to list them all`.

### issue-tracker issue inspect <number...>

Reads up to 50 tickets in one request, in the order given; a repeated number is shown once. For each ticket, prints `number`, `title`, `state`, `status`, `project`, `url`, then `priority`, `labels`, `epic` or `parent`, `blocked_by`, `blocking`, `sub_issue`, and `attachment` lines when present, then `body:` followed by the body, then each comment as `comment: <author> <time>` followed by its text. `epic` names the ticket's epic; `parent` names a parent that is not an epic. A blank line separates tickets. The `WARN:` line for a public project comes first, once per project, followed by a `WARN:` line for each ticket with more than one priority label.

### issue-tracker issue create

Creates a ticket on its project's board with status Todo. If a later step fails, the error names the created ticket, so finish that ticket instead of creating another.

- `--title` string: ticket title. Required.
- `--body` string: ticket text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--project` string: project. Defaults to the current repository's project.
- `--attach` stringArray: file to upload. Repeatable. See Attachments.
- `--label` stringArray: label to apply. Repeatable. The `epic` and `handoff` labels are refused; see Errors.
- `--epic` int: epic the ticket belongs to. It must be an open epic with fewer than 100 tickets.
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

Atomically takes a ticket and sets it to Doing. Pass a number or `--next`. Claims never expire: they end with `issue close` or `issue release`. An epic cannot be claimed; claim one of its tickets.

- `--next` bool: claim the most urgent ready ticket, then within a priority one of an epic that has a ticket in Doing, then the oldest, moving past tickets other agents win. Prints `OK: no ready tickets` when none remain. Handoffs are never claimed this way; for each open handoff it read, it prints `WARN: open handoff #<number> in <project>, <age> old: review it with issue-tracker handoff show --project <project>` on stderr, and still claims.
- `--project` string: limit `--next` to a project. Defaults to the current repository's project.
- `--all-projects` bool: let `--next` claim from every project. Required outside a registered repository unless `--project` is given.

### issue-tracker issue release <number>

Gives up a claim and returns the ticket to Todo.

### issue-tracker issue close <number>

Closes a ticket, sets it to Done, and removes its claim. Pass one of `--pr`, `--landed-pr`, or `--reason`. An epic is refused; use `epic close`. When the ticket was the last open ticket of its epic, the epic closes too, as completed, and a second line `closed epic #<number> in <project>: <title>` follows `closed #<number>`; an epic in Doing, with open blockers, or with the `blocked` label stays open.

- `--pr` stringArray: merged pull request. Repeatable. A bare number belongs to the ticket's project; `project#number` names another registered project. The command refuses to close when any pull request is unmerged. Links to the pull requests are added to the ticket.
- `--landed-pr` string: closed pull request that GitHub does not report as merged, as a number or `project#number`. The command closes the ticket as completed only when the pull request's head commit is on its base branch, and records the pull request and commit on the ticket. It refuses an open pull request, a merged one, which takes `--pr`, and a head that is not on the base branch.
- `--reason` string: close without a pull request: `completed`, `not-planned`, or `duplicate`.

### issue-tracker issue reopen <number>

Reopens a ticket and sets it to Todo.

### issue-tracker issue move <number> <project>

Reassigns a ticket to another project and keeps its status. Moving between a repository and its components keeps the board item: a ticket routed by the board changes only its `Component` value, and a labelled ticket changes its label and its `Component` value. `issue create` sets the `Component` value of a component's ticket. A ticket already in that project with no board status gets Todo, which finishes an import that failed partway.

### issue-tracker issue label add <number> <label...>

Adds labels. GitHub creates a label it does not know yet. `repo:` labels are refused, because `issue move` owns them, and so are `epic`, which the `epic` commands own, and `handoff`, which `handoff create` owns, in any case.

### issue-tracker issue label rm <number> <label...>

Removes labels. The same labels are refused.

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

### issue-tracker epic

Groups tickets under epics. An epic is a ticket with the `epic` label, routed to its project and on its board, whose sub-issues are its tickets. Its tickets can belong to any project. An epic is never ready work and cannot be claimed, and it stays in Todo until it closes. An epic never belongs to another ticket, and none of its tickets is an epic. An epic holds at most 100 tickets, open or closed, because GitHub allows 100 sub-issues per parent; `epic create --add`, `epic add`, and `issue create --epic` refuse to go past that before writing anything. Plan larger work as several epics.

### issue-tracker epic create

Creates an epic in Todo on its project's board. The create sets the `epic` label, which is created on first use, so the epic is never ready work. Prints `created epic #<number> in <project>: <url>`. Every ticket in `--add` is checked before anything is written.

- `--title` string: epic title. Required.
- `--body` string: epic text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--project` string: project. Defaults to the current repository's project.
- `--add` intSlice: existing ticket that becomes one of the epic's tickets. Repeatable. A ticket that is an epic or already has a parent is refused, and so are more than 100 tickets.
- `--blocked-by` intSlice: tickets that must close first. Repeatable. The epic's tickets are not ready while it has open blockers.
- `--label` stringArray: label to apply. Repeatable.
- `--attach` stringArray: file to upload. Repeatable. See Attachments.

### issue-tracker epic list

Prints one tab-separated line per epic, oldest first: number, project, progress as `<done>/<total>`, status, title. A ticket closed for any reason counts as done. Inside a registered repository it covers that project; elsewhere it needs `--project` or `--all-projects`.

- `--project` string: limit to a project.
- `--all-projects` bool: list every project. Required outside a registered repository unless `--project` is given.
- `--state` string: `open`, `closed`, or `all`. Default: open.

### issue-tracker epic inspect <epic>

Prints the epic as `issue inspect` does, without its `sub_issue` lines, then one tab-separated line per ticket: number, state, status, project, title. Then prints `#<epic>: <done> of <total> done, <open> open`. A ticket closed for any reason counts as done. Reads everything in one request.

### issue-tracker epic add <epic> <ticket...>

Makes tickets the epic's tickets. Prints `added <n> ticket(s) to epic #<epic>`; a ticket already in the epic is left alone and not counted. The epic must be open. A ticket that is an epic, or that belongs to another parent, is refused before anything is written, and so is any addition that would take the epic past 100 tickets. Tickets already in the epic take no new room.

- `--move` bool: take tickets that belong to another parent from it.

### issue-tracker epic rm <epic> <ticket...>

Detaches tickets from the epic. Prints `removed <n> ticket(s) from epic #<epic>`. Every ticket must belong to the epic.

### issue-tracker epic close <epic>

Closes an epic and sets it to Done. Prints `closed epic #<epic>`.

- `--reason` string: `completed`, which needs every ticket of the epic closed, or `not-planned`, which leaves its open tickets as they are. Required.

### issue-tracker epic close-eligible

Closes open epics whose tickets are all closed, as completed. Closing an epic's last ticket with `issue close` already closes it, so this covers tickets closed in the GitHub web UI. Prints `closed #<number> in <project>: <title>` for each, or `no eligible epics`. Skips an epic that is in Doing, has open blockers, or has the `blocked` label. Each epic is read again just before closing; one that stopped being eligible prints `skipped #<number>: no longer eligible`. A failed close stops the command. Inside a registered repository it covers that project; elsewhere it needs `--project` or `--all-projects`.

- `--all-projects` bool: cover every project. Required outside a registered repository unless `--project` is given.
- `--dry-run` bool: print `would close #<number> in <project>: <title>` for each epic and close nothing.
- `--project` string: limit to a project.

### issue-tracker epic promote <ticket>

Makes an open ticket an epic by adding the `epic` label. Its sub-issues become the epic's tickets. Prints `promoted #<number> to an epic`. Refuses an epic, a closed ticket, a handoff, a ticket in Doing, a ticket that belongs to another ticket, and a ticket with a sub-issue that is an epic.

### issue-tracker epic demote <epic>

Makes an epic an ordinary ticket by removing the `epic` label. Prints `demoted epic #<epic>`. Refuses an epic that still has tickets; detach them with `epic rm` first.

### issue-tracker handoff

Hands work to the next agent. A handoff is a ticket with the `handoff` label, routed to its project and in Todo on the board, whose body starts with `Branch: <branch>` and `Commit: <sha>` lines, then `Ticket: #<number>` when given, then a blank line and its text. A repository and its components share one slot: a project holds at most one open handoff, enforced by the ref `refs/handoffs/<owner>/<repo>` in the tracker repository, which exists while the handoff is open. Handoffs are not ready work: `issue list --ready` and `issue claim --next` leave them out. Each subcommand acts on the current repository's project, or on `--project`.

### issue-tracker handoff create

Opens the project's handoff. Before writing anything it checks, in one request, that the branch exists in the project's repository on GitHub and contains the commit, and resolves the commit to its full SHA. It then takes the slot, and creates the ticket with its routing label and the `handoff` label, which it creates on first use, on the board in Todo. Prints `created handoff #<number> in <project> at <branch> <sha>: <url>`. Fails without writing anything when the project already has an open handoff, naming it. When creating the ticket fails, it frees the slot again; when the ticket was created but adding it to the board failed, the error names the ticket.

- `--title` string: handoff title. Required.
- `--body` string: handoff text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters, including the header lines.
- `--project` string: project. Defaults to the current repository's project.
- `--branch` string: branch the work is on. Defaults to the current checkout's branch, when the current checkout is the project's repository.
- `--sha` string: commit the work stopped at, full or abbreviated. Defaults to the current checkout's `HEAD`, when the current checkout is the project's repository.
- `--ticket` int: ticket the work belongs to, recorded in the handoff. Taking the handoff does not claim it.

### issue-tracker handoff show

Prints the project's open handoff without taking it. For people: lines `handoff: #<number> <title>`, `project:`, `branch:`, `commit:`, `ticket:` when one is recorded, `age:`, and `url:`, then a blank line and the text. For agents: `OK: <number>\t<project>\t<branch>\t<sha>`, then a blank line and the text when there is any. Prints `OK: no open handoff in <project>` when there is none.

### issue-tracker handoff take

Picks up the project's open handoff: claims it with the same atomic claim as `issue claim`, checks that the branch still contains the commit, closes it as completed, and frees the slot. Prints the handoff as `handoff show` does. When two agents take the same handoff, one wins and the other fails with `ticket is already claimed`. When the branch no longer contains the commit, or no longer exists, it still takes the handoff and prints `WARN: branch <branch> no longer contains commit <sha>: check where the work went before resuming` on stderr; when the check itself fails, it prints `WARN: could not check that branch <branch> still contains commit <sha>: <reason>` and still takes it. When closing fails, it gives the claim back, so the handoff can be taken again. When deleting the claim or freeing the slot fails after the close, it still prints the handoff, then fails with an error that starts `took handoff #<number>, but`; a slot left behind ends the error with `run issue-tracker handoff drop --free-slot`, and a claim left on the closed handoff needs nothing. It does not claim the ticket named by `Ticket:`.

### issue-tracker handoff untake

Gives back a handoff that `handoff take` closed, so the next agent can take it again. It reads the ticket, takes the slot of the ticket's project, deletes any claim that `take` left on the ticket, then reopens it in Todo on the board and posts `--reason`, in one write. Prints `returned handoff #<number> to <project> at <branch> <sha>: <title>`. The handoff keeps its branch, commit, and text; push any work done since taking it and say where it went in `--reason`. Fails without writing anything when the ticket is not a handoff, is open, was closed as anything but completed, or when the project already holds another open handoff, naming it. When deleting the claim or reopening fails, it frees the slot again.

- `<number>` int: the handoff that `handoff take` closed. Required.
- `--reason` string: why the handoff is given back, posted as a comment.

### issue-tracker handoff drop

Closes the project's open handoff as not planned without claiming it, and frees the slot so that a new handoff can be created. Prints `dropped handoff #<number> in <project>: <title>`. Fails with `no open handoff in <project>` when there is none, leaving the slot alone, because a create that has taken the slot but not yet created its ticket also has no open handoff.

- `--reason` string: why the handoff is dropped, posted as a comment.
- `--free-slot` bool: with no open handoff, free the slot that a create which failed partway left behind, and print `freed the handoff slot of <project>, which held no open handoff`. Use it only when no `handoff create` for the project is running.

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
- `label repo:<name> holds the <n> tickets of removed project <registration>: rename the label on GitHub before registering <owner/repo>`: another repository's project was removed under this name, and registering this one would move its tickets here. Ask the user to rename the label.
- `label repo:<name> has no registration and holds <n> tickets, perhaps of a project removed before removals were recorded: ...`: which repository the label's tickets belong to is unknown. Ask the user whether they belong to this repository: if so, they set the description the message names; otherwise they rename the label.
- `label repo:<name> holds <n> tickets and its description "<text>" is not a registration: ...`: a hand-made label with this name already holds tickets. Ask the user to rename it.
- `ticket #<number> is an epic: claim one of its tickets`: epics are never claimed. Run `epic inspect <number>` and claim an open ticket of the epic.
- `ticket #<number> is an epic: use epic close`: close an epic with `epic close`.
- `epic #<number> has <n> open ticket(s): close them first, or close the epic as not-planned`: nothing was written.
- `ticket #<number> is not an epic: run epic promote <number> first`: `issue create --epic` and `epic add` take only an epic. Nothing was written. Promote the ticket, or name the right epic.
- `epic #<number> is closed: reopen it first`: nothing was written.
- `ticket #<number> is an epic: epics do not nest`, `ticket #<number> belongs to #<parent>: epics do not nest`, or `sub-issue #<number> of #<epic> is an epic: epics do not nest`: nothing was written.
- `epic #<number> would have <n> tickets, over GitHub's limit of 100 per epic: start another epic for the rest, or detach closed tickets with epic rm`, or `the epic would have <n> tickets, ...` from `epic create --add`: nothing was written. GitHub counts closed tickets too. Create another epic for the remaining work, or detach closed tickets with `epic rm` when their history in this epic does not matter.
- `ticket #<number> already belongs to #<parent>: pass --move to move it`: nothing was written. Rerun `epic add` with `--move` when the ticket should leave its parent. From `epic create --add`, create the epic without that ticket, then run `epic add --move`.
- `closed #<number>, but closing its epic #<epic> failed: <reason>`: the ticket closed and its epic stayed open. Run `epic close-eligible` later to close it.
- `label "<label>" makes a ticket an epic: use epic create, epic promote, or epic demote instead` or `label "<label>" makes a ticket a handoff: use handoff create instead`: nothing was written.
- `ticket is already claimed`: another agent holds the ticket. Pick another. From `handoff take`, another agent took the handoff first.
- `no open handoff in <project>`: `handoff take` or `handoff drop` found no open handoff, or `handoff drop --free-slot` found no slot to free either. Nothing was changed.
- `<project> already has open handoff #<number>: take or drop it first`: a project holds one open handoff. Nothing was written. Read it with `handoff show`, then take it, or ask the user before dropping it.
- `the handoff slot of <project> is taken, but it has no open handoff: another handoff create may still be running, so retry in a moment; if none is, a create failed partway, and issue-tracker handoff drop --free-slot frees the slot`: retry the create after a few seconds. When it fails the same way again and no other agent is creating a handoff for the project, run `handoff drop --free-slot`, then create again.
- `ticket #<number> is not a handoff`, `handoff #<number> is open, so nobody has taken it`, or `handoff #<number> was dropped, not taken: create a new handoff instead`: `handoff untake` gives back only a handoff that `handoff take` closed. Nothing was written.
- `took handoff #<number>, but ...`: the handoff was taken and printed, but a cleanup step failed. Resume the work from the printed branch and commit. When the error ends `run issue-tracker handoff drop --free-slot`, run it so that the project can take a new handoff.
- `branch <branch> of <owner/repo> does not contain commit <sha>: push the commit first` or `branch <branch> or commit <sha> not found in <owner/repo>: push them first`: the handoff would point at work that exists only on this machine. Nothing was written. Push the branch and rerun.
- `no branch or commit to hand off: ...`: `handoff create` ran outside a checkout of the project's repository, or in one with no current branch or commits, without `--branch` and `--sha`. Pass them, or run it in the checkout.
- `handoff #<number> has no "Branch:" and "Commit:" lines at the top of its body: drop it and create it again`: the handoff's body was edited by hand. Ask the user before dropping it.
- `project <name> is registered but its board is missing or inaccessible to <account>`: the project registration exists, but the selected account cannot resolve its board, even after re-reading the tracker's projects from GitHub once. Ask the tracker owner to verify that the board exists and that this account has Write access under the board's Settings, Manage access. A missing or inaccessible board is distinct from an unregistered project; repeating `project add` does not grant access.
- `<owner/repo> is not a registered project: register it with issue-tracker project add <owner/repo>, or <remedy>`: a command ran in a repository that is not a project. `<remedy>` is `pass --project <name>, or --all-projects for every project` for `issue list`, `issue claim --next`, `epic list`, and `epic close-eligible`, `pass --project <name>` for `issue create`, and `name a project` for `project inspect`. Follow it, or ask the user whether to register the repository.
- `<owner/repo> is not a registered project, and cannot be: project <name> already belongs to <other/repo>: <remedy>`: the name this repository would be registered under is held by another registration, such as a component or a repository registered before other owners' repositories got `<owner>/<name>`. Follow the remedy.
- `not inside a registered repository: <reason>: <remedy>`: the command ran where no github.com repository was found. The reason is `no git checkout here`, `this checkout has no git remote`, `no git remote points to a GitHub host the GitHub CLI is logged in to`, `<host>/<owner>/<repo> is on <host>, and projects are github.com repositories`, or, for any other failure, git's or the GitHub CLI's own reason on one line, such as a missing git or a checkout git refuses under its `safe.directory` rule. Follow the remedy, or ask the user to fix git first when the reason is about git itself.
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
