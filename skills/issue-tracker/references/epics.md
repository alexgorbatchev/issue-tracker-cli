# Epics

To plan work that takes several tickets, run `issue-tracker epic create --title "..."`, then `issue-tracker issue create --epic <number>` for each piece of work. Agents claim an epic's tickets, never the epic, and closing its last open ticket closes the epic. Run `issue-tracker epic inspect <number>` to see an epic's tickets and progress. An epic holds at most 100 tickets, GitHub's limit per parent, so split larger work into several epics. An epic holds tickets only: an epic never belongs to another ticket, and none of its tickets is an epic.


## issue-tracker epic

Groups tickets under epics. An epic is a ticket with the `epic` label, routed to its project and on its board, whose sub-issues are its tickets. Its tickets can belong to any project. An epic is never ready work and cannot be claimed, and it stays in Todo until it closes. An epic never belongs to another ticket, and none of its tickets is an epic. An epic holds at most 100 tickets, open or closed, because GitHub allows 100 sub-issues per parent; `epic create --add`, `epic add`, and `issue create --epic` refuse to go past that before writing anything. Plan larger work as several epics.


## issue-tracker epic create

Creates an epic in Todo on its project's board. The create sets the `epic` label, which is created on first use, so the epic is never ready work. Prints `created epic #<number> in <project>: <url>`. Every ticket in `--add` is checked before anything is written.

- `--title` string: epic title. Required.
- `--body` string: epic text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--project` string: project. Defaults to the current repository's project.
- `--add` intSlice: existing ticket that becomes one of the epic's tickets. Repeatable. A ticket that is an epic or already has a parent is refused, and so are more than 100 tickets.
- `--blocked-by` intSlice: tickets that must close first. Repeatable. The epic's tickets are not ready while it has open blockers.
- `--label` stringArray: label to apply. Repeatable.
- `--attach` stringArray: file to upload. Repeatable. See [Attachments](attachments.md).


## issue-tracker epic list

Prints one tab-separated line per epic, oldest first: number, project, progress as `<done>/<total>`, status, title. A ticket closed for any reason counts as done. Inside a registered repository it covers that project; elsewhere it needs `--project` or `--all-projects`.

- `--project` string: limit to a project.
- `--all-projects` bool: list every project. Required outside a registered repository unless `--project` is given.
- `--state` string: `open`, `closed`, or `all`. Default: open.


## issue-tracker epic inspect <epic>

Prints the epic as `issue inspect` does, without its `sub_issue` lines, then one tab-separated line per ticket: number, state, status, project, title. Then prints `#<epic>: <done> of <total> done, <open> open`. A ticket closed for any reason counts as done. Reads everything in one request.


## issue-tracker epic add <epic> <ticket...>

Makes tickets the epic's tickets. Prints `added <n> ticket(s) to epic #<epic>`; a ticket already in the epic is left alone and not counted. The epic must be open. A ticket that is an epic, or that belongs to another parent, is refused before anything is written, and so is any addition that would take the epic past 100 tickets. Tickets already in the epic take no new room.

- `--move` bool: take tickets that belong to another parent from it.


## issue-tracker epic rm <epic> <ticket...>

Detaches tickets from the epic. Prints `removed <n> ticket(s) from epic #<epic>`. Every ticket must belong to the epic.


## issue-tracker epic close <epic>

Closes an epic and sets it to Done. Prints `closed epic #<epic>`.

- `--reason` string: `completed`, which needs every ticket of the epic closed, or `not-planned`, which leaves its open tickets as they are. Required.


## issue-tracker epic close-eligible

Closes open epics whose tickets are all closed, as completed. Closing an epic's last ticket with `issue close` already closes it, so this covers tickets closed in the GitHub web UI. Prints `closed #<number> in <project>: <title>` for each, or `no eligible epics`. Skips an epic that is in Doing, has open blockers, or has the `blocked` label. Each epic is read again just before closing; one that stopped being eligible prints `skipped #<number>: no longer eligible`. A failed close stops the command. Inside a registered repository it covers that project; elsewhere it needs `--project` or `--all-projects`.

- `--all-projects` bool: cover every project. Required outside a registered repository unless `--project` is given.
- `--dry-run` bool: print `would close #<number> in <project>: <title>` for each epic and close nothing.
- `--project` string: limit to a project.


## issue-tracker epic promote <ticket>

Makes an open ticket an epic by adding the `epic` label. Its sub-issues become the epic's tickets. Prints `promoted #<number> to an epic`. Refuses an epic, a closed ticket, a handoff, a ticket in Doing, a ticket that belongs to another ticket, and a ticket with a sub-issue that is an epic.


## issue-tracker epic demote <epic>

Makes an epic an ordinary ticket by removing the `epic` label. Prints `demoted epic #<epic>`. Refuses an epic that still has tickets; detach them with `epic rm` first.
