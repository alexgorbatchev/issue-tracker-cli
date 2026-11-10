# Projects and routing

`issue-tracker` keeps every ticket as a GitHub Issue in one private tracker repository. Code and pull requests stay in their own repositories. Each registered code repository is a project, with one `repo:<name>` label and one board with the columns Todo, Doing, and Done. A project is named after its repository: `<name>` when the tracker's owner owns it, and `<owner>/<name>` for any other owner, so repositories of the same name under different owners can both be projects. A ticket belongs to a project through that label or by being on its board, so a ticket created inside a board in the GitHub web UI needs no label. If the two disagree, the label wins. A ticket's status comes from its project's board only, so a ticket with no project has none; its items on other boards count toward activity and are never changed. A directory of a registered repository can be registered as a component: a project named `<repo>/<component>`, after its repository's project name, as in `api/docs` or `other/api/docs`, with its own `repo:<repo>/<component>` label on the repository's board. The repository's board has a single-select `Component` field with one option per component. A ticket with a component's label belongs to the component; a ticket on the board with no routing label belongs to the component its `Component` value names, or else to the repository. Project-scoped lists find unlabelled board tickets through GitHub search, so a new or just-closed one can take several seconds to move in them; `issue inspect` is always current. A ticket with neither has no project: it appears in unscoped lists and in `issue list --unrouted`, is never ready, and cannot be claimed until it gets a label or a board.

## Project resolution

Inside a registered repository, the current repository's git remote selects the project, so `--project` is optional. Inside a registered component's directory, or below it, the component is selected; when components are nested, the deepest one wins. Elsewhere in the repository, the repository's project is selected, and its lists, claims, and counts include its components' tickets. When `GH_REPO` is set, it names the repository and the directory is ignored. An explicit `--project` always wins. Outside a registered repository, `issue list`, `issue claim --next`, `epic list`, and `epic close-eligible` need `--project` or `--all-projects`, and `issue create`, `epic create`, and `project inspect` need an explicit project. They fail naming the reason: a GitHub repository that is not registered, one whose name another owner's repository already holds, a repository on another host, a checkout with no GitHub remote, or no git checkout. Each error ends with how that command takes a project. See [Errors](errors.md).


## issue-tracker project add [owner/repo]

Registers a repository, creating its board and label. Defaults to the current repository. The board's description is set as `project sync` sets it. Repeating it is safe, and a repository already registered under another name keeps it. The project is named `<name>` for a repository of the tracker's owner and `<owner>/<name>` otherwise. Fails when that name is already registered for another repository or as a component, when its label holds tickets of another removed project or registration (see Errors), or when the label name exceeds 50 characters. A label that holds tickets is taken back only by the repository it was registered for, ignoring case; one with no tickets is reused. Nothing is written before these checks pass.

With `--path`, registers that directory of an already registered repository as a component instead. It adds the component as an option of the board's `Component` field, creating the field on first use, then creates the label `repo:<repo>/<component>`, whose description `owner/repo:<path>` records the directory. It uses the repository's board. Repeating it for a component without an option adds the option. Prints `registered project <repo>/<component> for <path> in <owner/repo>`. Repeating it is safe. Fails when the repository is not registered, when the name or path is already registered to another component, when another owner's repository is registered under that name, when its label holds tickets of another removed project or registration, or when the label name exceeds 50 characters or its description, with the `removed: ` that `project rm` would add, exceeds 100.

- `--path` string: directory relative to the top of the repository, without `.`, `..`, or `:`.
- `--name` string: component name used after `<repo>/`. Defaults to the last element of `--path`. Requires `--path`.


## issue-tracker project inspect [project]

Prints `project`, `repository`, `path`, `visibility`, `board`, `open`, and `closed`. `path` is `.` for a whole repository. A repository's counts include its components' tickets. Defaults to the current repository's project.


## issue-tracker project list

Prints one tab-separated line per project: name, repository, visibility, board URL, path. The path is `.` for a whole repository. A component shows its repository's board.


## issue-tracker project rm <project>

Unregisters a project with no open tickets and keeps the label on closed tickets, with the description `removed: <owner/repo>`, or `removed: <owner/repo>:<path>` for a component, so that only that registration can take the label back. A component registered before removals were recorded whose `removed: <owner/repo>:<path>` would exceed GitHub's 100 characters keeps `removed: <owner/repo>` instead, and any component of that repository under the same name can take it back. For a repository, labels closed tickets that were only on the board, then deletes the board. A component's board belongs to its repository and stays; its `Component` option is deleted, and the field with the last option, so tickets that named it belong to the repository. Fails for a repository with registered components until they are removed.

- `--yes` bool: confirm the removal. Required.


## issue-tracker project sync [project]

Sets a project board's description to its repository's description, followed by a blank line and the repository's URL. A description too long for GitHub's 300-character limit is cut and ends with `…`, keeping the URL; a repository without a description gives just the URL. Boards that already match are left unchanged. A component shares its repository's board, so naming one syncs that board. Defaults to the current repository's project. Prints `updated <n> of <total> board descriptions`. When a project has no board or its repository is not found, the other boards are still synced, and the command then fails with `could not sync <n> projects: ` followed by each reason.

- `--all-projects` bool: sync the board of every project. Cannot be combined with a project name.


## issue-tracker project scan

Prints one tab-separated line for each of the tracker owner's unarchived, non-fork repositories with open issues: repository, visibility, open, can move, from others. Reads logins and counts only, never issue text, and leaves out the tracker repository.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.

An issue can move when its repository is private, or when the issue and all its comments come from trusted authors. A deleted author, or more than 100 comments, counts as text from other people. In a private repository, such an issue counts in both `can move` and `from others`.


## issue-tracker project import <owner/repo>

Moves a repository's open issues into the tracker, oldest first, registering the project when needed. Prints `moved owner/repo#<source> to #<ticket>` for each issue, then a summary with the counts moved, left to move, and skipped. Issues from other people stay where they are. A public issue's authors are reread just before its transfer, and it stays if someone else has commented since the run began. Moves one issue every 32 seconds, so a large repository takes hours. Rerunning continues with what is left. Fails when the tracker is not private, when the repository is the tracker, or when the repository has a different owner than the tracker.

GitHub rewrites `#N` references in a moved issue's body and comments to `owner/repo#N`, so they keep pointing at the source repository. A public issue cannot be moved back.

- `--author` stringArray: also trust text from this login. Repeatable. The tracker's owner is always trusted.
- `--dry-run` bool: report the counts without changing anything.
- `--limit` int: maximum issues to move in this run. 0 moves all. Default: 0.
