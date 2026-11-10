---
name: "issue-tracker"
description: "Use when managing tickets or projects with the issue-tracker CLI."
author: "Alex Gorbatchev"
metadata:
  created_on: 2026-11-01 10:52
  last_modified: 2026-11-10 14:42
  status: current
---

## Execution rules

Set `AGENT=1` on every invocation, or export it for the session. Read exit status as well as stdout: failures exit nonzero and print `ERR:` to stderr. Successful writes print one `OK:` line. Values `1`, `true`, and `yes` enable agent output, with case and surrounding whitespace ignored.

Tickets live in a private tracker repository; code and pull requests stay in their project repositories. Inside a registered checkout, the CLI selects its project; use `--project` to override it. Read the projects reference for components or ambiguous routing.

## Workflow

1. Run `issue-tracker issue claim --next` to take the most urgent ready ticket, or `issue-tracker issue claim <number>` for a specific one. Claims are atomic: when another agent holds the ticket, the command fails with `ticket is already claimed`, so pick another ticket.
2. Run `issue-tracker issue inspect <number>` to read the ticket, or pass several numbers to read related tickets in one call, and `issue-tracker issue attachment pull <number>` to download its screenshots.
3. Work in the repository named by the ticket's project. A claim works from any directory, so a ticket for a dependency can be claimed from the application that needs it. Name new ticket branches `<type>/<number>-<short-description>`, using the repository's branch type convention, for example `fix/123-empty-input`. Include the claimed ticket's number in public and private repositories. Preserve existing published branch names when resuming work.
4. Record unrelated problems with `issue-tracker issue create --title "..." --discovered-from <number>`.
5. Open the pull request in the code repository.
6. After the merge, run `issue-tracker issue close <number> --pr <pull-request>`.
7. To stop without finishing, run `issue-tracker issue release <number>`.

For public projects, keep ticket text, attachments, and tracker identity out of commits, pull requests, and branch names. Use the bare ticket number in branch names; keep ticket numbers out of commit messages and PR text. Read the CLI's `WARN:` line for the public-project privacy reminder; the branch-number rule above supersedes older CLI warnings that prohibit numbers in branch names.

Visibility checks treat a missing or access-denied repository, including one blocked by organization SAML enforcement, as public for warnings. Other repositories' visibility results remain usable, so an unrelated denial leaves accessible project operations available. Errors reading tracker metadata, accessing the requested repository or board, authenticating, or handling rate limits still fail the operation.

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
