---
name: "issue-tracker"
description: "Use when managing tickets or projects with the issue-tracker CLI."
author: alexgorbatchev
metadata:
  created_on: 2026-11-01 10:52
  last_modified: 2026-11-12 13:23
  status: current
---

## Execution rules

Set `AGENT=1` on every invocation, or export it for the session. Read exit status as well as stdout: failures exit nonzero and print `ERR:` to stderr. Successful writes print `OK:` result lines; commands may print several results or additional content. Read each command's reference for its output contract. Values `1`, `true`, and `yes` enable agent output, with case and surrounding whitespace ignored.

Tickets live in a private tracker repository; code and pull requests stay in their project repositories. Inside a registered checkout, the CLI selects its project; use `--project` to override it. Read [projects](references/projects.md) for components, ambiguous routing, or an unregistered repository; follow its registration workflow before creating project work.

## Workflow

1. Resume owned unfinished work from its ticket and handoff before selecting new work. Establish ownership from a durable record of this worker's successful claim and subsequent lifecycle actions. Claims never expire and contain no worker identity; Doing, a stale marker, or an existing claim alone does not establish ownership. Resume verified owned work without claiming it again. When ownership is uncertain, resolve it with the recorded worker or user before modifying or releasing that work; continue independent authorized work. Read [handoffs](references/handoffs.md) before transferring or recovering work.
2. For a specified ticket not already verified as owned, run `issue-tracker issue claim <number>`. When queue execution is authorized, run `issue-tracker issue claim --next --project <project>` within that scope; use `--all-projects` only for an authorized cross-project queue. Claims are atomic: any existing claim, including your own, makes a new claim fail with `ticket is already claimed`. Reconcile that conflict against the ownership record above; continue eligible authorized work or report unresolved ownership; `OK: no ready tickets` ends selection. After a successful claim, record the ticket, worker/session identity, and claim result in a durable execution record before starting work; keep that record current through release, close, or handoff.
3. Run `issue-tracker issue inspect <number>` to read the ticket, or pass several numbers to read related tickets in one call, and `issue-tracker issue attachment pull <number>` to download its screenshots.
4. Work in the repository named by the ticket's project. A claim works from any directory, so a ticket for a dependency can be claimed from the application that needs it. Name new ticket branches `<type>/<number>-<short-description>`, using the repository's branch type convention, for example `fix/123-empty-input`. Include the claimed ticket's number in public and private repositories. Preserve existing published branch names when resuming work.
5. Record independently tracked discoveries with `issue-tracker issue create --title "..." --discovered-from <number>`; perform repairs covered by existing authorization. Read [ticket lifecycle](references/tickets.md#lifecycle-reconciliation) for blockers, duplicates, completion evidence, and pending cleanup.
6. Follow the project's integration policy: open a pull request in the code repository when required, or commit directly to the target branch when authorized. Record verified integration immediately in the ticket, including revision, checks, and outstanding cleanup. Complete engineering cleanup through the applicable development workflow.
7. After required work and cleanup finish, run `issue-tracker issue close <number> --pr <pull-request>` for merged PR work, or `issue-tracker issue close <number> --reason completed` for verified direct integration after recording the landed commits. Reconcile related task state under the lifecycle reference, then select the next eligible ticket only when queue execution is authorized.
8. Before stopping unfinished work, preserve a durable checkpoint and any blocker under the lifecycle and handoff references, then run `issue-tracker issue release <number>` when handing it back. Release returns the ticket to Todo, so record blockers before releasing. Verify writes and partial-success errors before retrying.

For public projects and projects with unknown visibility, keep ticket text, attachments, and tracker identity out of commits, pull requests, and branch names. Use the bare ticket number in branch names; keep ticket numbers out of commit messages and PR text. Read the CLI's `WARN:` line for the project privacy reminder; the branch-number rule above supersedes older CLI warnings that prohibit numbers in branch names.

Visibility is `private`, `public`, or `unknown`. A missing or access-denied repository, including one blocked by organization SAML enforcement, has `unknown` visibility. Its warning explains that the repository is missing or inaccessible and applies public-project privacy guidance until privacy is confirmed. Other repositories' visibility results remain usable, so an unrelated denial leaves accessible project operations available. Errors reading tracker metadata, accessing the requested repository or board, authenticating, or handling rate limits still fail the operation.

## Conditional references

Read the applicable reference before using options or behavior beyond the common workflow:

- [Tickets](references/tickets.md): creation, listing/filtering, priority, claims, status, closing, labels, comments, and blockers.
- [Projects](references/projects.md): registration, components, project selection, board routing, scanning, and imports.
- [Epics](references/epics.md): planning multi-ticket work and managing parent membership or completion.
- [Handoffs](references/handoffs.md): transferring unfinished work, resuming it, or recovering a handoff slot.
- [Attachments](references/attachments.md): uploading, downloading, removing, or pruning files.
- [Setup](references/setup.md): configuration, accounts, authentication, or environment overrides.
- [Utilities](references/utilities.md): skill installation, shell completion, and CLI help.
- [Errors](references/errors.md): a failed command or partial success; use its exact message to select recovery before retrying.
