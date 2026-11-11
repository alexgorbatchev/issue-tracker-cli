# Tickets

## Contents

- [Lifecycle reconciliation](#lifecycle-reconciliation)
- [Priority](#priority)
- [issue-tracker issue list](#issue-tracker-issue-list)
- [issue-tracker issue inspect <number...>](#issue-tracker-issue-inspect-number)
- [issue-tracker issue create](#issue-tracker-issue-create)
- [issue-tracker issue edit <number>](#issue-tracker-issue-edit-number)
- [issue-tracker issue claim [number]](#issue-tracker-issue-claim-number)
- [issue-tracker issue release <number>](#issue-tracker-issue-release-number)
- [issue-tracker issue close <number>](#issue-tracker-issue-close-number)
- [issue-tracker issue reopen <number>](#issue-tracker-issue-reopen-number)
- [issue-tracker issue move <number> <project>](#issue-tracker-issue-move-number-project)
- [issue-tracker issue label add <number> <label...>](#issue-tracker-issue-label-add-number-label)
- [issue-tracker issue label rm <number> <label...>](#issue-tracker-issue-label-rm-number-label)
- [issue-tracker issue status set <number> <status>](#issue-tracker-issue-status-set-number-status)
- [issue-tracker issue comment add <number>](#issue-tracker-issue-comment-add-number)
- [issue-tracker issue blocker add <number> <blocker...>](#issue-tracker-issue-blocker-add-number-blocker)
- [issue-tracker issue blocker rm <number> <blocker...>](#issue-tracker-issue-blocker-rm-number-blocker)

## Lifecycle reconciliation

- Record verified integration as a comment immediately: repository, landed revision or merged PR, validation evidence, and outstanding cleanup with exact resources, ownership, prerequisites, and next action. Keep incomplete work open. After interruption, compare this checkpoint with actual repository and resource state and resume only what remains.
- Let the engineering workflow own process, artifact, branch, and worktree cleanup. If cleanup needs a decision, record it and preserve the resources; continue independent authorized work. Track a repair separately when it is independently actionable, with a discovery link and concrete acceptance criteria, rather than closing unfinished work by relabelling it.
- Represent ticket dependencies with `issue blocker add` and remove relationships only when the dependency no longer applies. Use the `blocked` label for a non-ticket blocker and record the exact unblock condition in a comment; remove the label when that condition is verified resolved. Reconcile dependencies when work completes so newly eligible tickets can be selected.
- Before closing a duplicate with `--reason duplicate`, link the surviving ticket in a comment and preserve unique requirements, evidence, and dependency relationships on the survivor. Close resolved work only after verifying its acceptance criteria and required cleanup. Closing the last open ticket may also close its epic; verify the result rather than issuing redundant writes.
- Before releasing unfinished work, record scope, completed steps, validation, remaining actions, blockers, branch/commit, retained resources, and ownership. Read [handoffs](handoffs.md) when another execution must resume it. A stale marker is a review signal: claims persist until close or release, so confirm a worker has relinquished ownership before reclaiming its ticket or resources.

## Priority

A `priority:P0`, `priority:P1`, `priority:P2`, or `priority:P3` label sets a ticket's priority, with `P0` the most urgent; label names ignore case, and any other `priority:` label is an ordinary label. `issue list --ready` and `issue claim --next` take the most urgent tickets first, then the oldest, and a ticket with no priority label comes after `P3`. A ticket with more than one priority label ranks by its most urgent one, and `issue list` (on stderr) and `issue inspect` print `WARN: #<number> has priorities <P0, P2>: ranked as <P0>` until the extra labels are removed with `issue label rm`. Set a priority with `issue label add <number> priority:P1`.


## issue-tracker issue list

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


## issue-tracker issue inspect <number...>

Reads up to 50 tickets in one request, in the order given; a repeated number is shown once. For each ticket, prints `number`, `title`, `state`, `status`, `project`, `url`, then `priority`, `labels`, `epic` or `parent`, `blocked_by`, `blocking`, `sub_issue`, and `attachment` lines when present, then `body:` followed by the body, then each comment as `comment: <author> <time>` followed by its text. `epic` names the ticket's epic; `parent` names a parent that is not an epic. A blank line separates tickets. The `WARN:` line for a public project comes first, once per project, followed by a `WARN:` line for each ticket with more than one priority label.


## issue-tracker issue create

Creates a ticket on its project's board with status Todo. If a later step fails, the error names the created ticket, so finish that ticket instead of creating another.

- `--title` string: ticket title. Required.
- `--body` string: ticket text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--project` string: project. Defaults to the current repository's project.
- `--attach` stringArray: file to upload. Repeatable. See [Attachments](attachments.md).
- `--label` stringArray: label to apply. Repeatable. The `epic` and `handoff` labels are refused; see [Errors](errors.md).
- `--epic` int: epic the ticket belongs to. It must be an open epic with fewer than 100 tickets.
- `--discovered-from` int: adds a line naming the ticket that surfaced this work. It does not block anything.
- `--blocked-by` intSlice: tickets that must close first. Repeatable.
- `--blocking` intSlice: tickets waiting on this one. Repeatable.


## issue-tracker issue edit <number>

Changes the title or body, and attaches files to the body. Attaching without `--body` or `--body-file` appends the files to the current body.

- `--title` string: new title.
- `--body` string: new text.
- `--body-file` string: file holding the new text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--attach` stringArray: file to upload. Repeatable. See [Attachments](attachments.md).


## issue-tracker issue claim [number]

Atomically takes a ticket and sets it to Doing. Pass a number or `--next`. Claims never expire: they end with `issue close` or `issue release`. An epic cannot be claimed; claim one of its tickets.

- `--next` bool: claim the most urgent ready ticket, then within a priority one of an epic that has a ticket in Doing, then the oldest, moving past tickets other agents win. Prints `OK: no ready tickets` when none remain. Handoffs are never claimed this way; for each open handoff it read, it prints `WARN: open handoff #<number> in <project>, <age> old: review it with issue-tracker handoff show --project <project>` on stderr, and still claims.
- `--project` string: limit `--next` to a project. Defaults to the current repository's project.
- `--all-projects` bool: let `--next` claim from every project. Required outside a registered repository unless `--project` is given.


## issue-tracker issue release <number>

Gives up a claim and returns the ticket to Todo.


## issue-tracker issue close <number>

Closes a ticket, sets it to Done, and removes its claim. Pass one of `--pr`, `--landed-pr`, or `--reason`. An epic is refused; use `epic close`. When the ticket was the last open ticket of its epic, the epic closes too, as completed, and a second line `closed epic #<number> in <project>: <title>` follows `closed #<number>`; an epic in Doing, with open blockers, or with the `blocked` label stays open.

- `--pr` stringArray: merged pull request. Repeatable. A bare number belongs to the ticket's project; `project#number` names another registered project. The command refuses to close when any pull request is unmerged. Links to the pull requests are added to the ticket.
- `--landed-pr` string: closed pull request that GitHub does not report as merged, as a number or `project#number`. The command closes the ticket as completed only when the pull request's head commit is on its base branch, and records the pull request and commit on the ticket. It refuses an open pull request, a merged one, which takes `--pr`, and a head that is not on the base branch.
- `--reason` string: close without a pull request: `completed`, `not-planned`, or `duplicate`.


## issue-tracker issue reopen <number>

Reopens a ticket and sets it to Todo.


## issue-tracker issue move <number> <project>

Reassigns a ticket to another project and keeps its status. Moving between a repository and its components keeps the board item: a ticket routed by the board changes only its `Component` value, and a labelled ticket changes its label and its `Component` value. `issue create` sets the `Component` value of a component's ticket. A ticket already in that project with no board status gets Todo, which finishes an import that failed partway.


## issue-tracker issue label add <number> <label...>

Adds labels. GitHub creates a label it does not know yet. `repo:` labels are refused, because `issue move` owns them, and so are `epic`, which the `epic` commands own, and `handoff`, which `handoff create` owns, in any case.


## issue-tracker issue label rm <number> <label...>

Removes labels. The same labels are refused.


## issue-tracker issue status set <number> <status>

Moves an unclaimed ticket to `Todo` or `Done`. Doing belongs to `issue claim`, and a claimed ticket changes through `issue release` or `issue close`.


## issue-tracker issue comment add <number>

Adds a comment. Needs text, files, or both.

- `--body` string: comment text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters.
- `--attach` stringArray: file to upload. Repeatable. See [Attachments](attachments.md).


## issue-tracker issue blocker add <number> <blocker...>

Marks a ticket blocked by other tickets.


## issue-tracker issue blocker rm <number> <blocker...>

Removes blockers.
