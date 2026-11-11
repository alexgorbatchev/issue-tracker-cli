# Handoffs

To hand unfinished work to the next agent when publication is authorized, push the branch, then run `issue-tracker handoff create --title "..." --body-file notes.md` in its checkout; it records the current branch and `HEAD`. A project holds one open handoff. The receiving agent runs `issue-tracker handoff take`, which claims and closes the handoff in one step and prints the branch and commit to resume from. An agent that took a handoff it cannot work on runs `issue-tracker handoff untake <number>` to give it back. Run `issue-tracker handoff show` to read a handoff without taking it, and `issue-tracker handoff drop --reason "..."` to close one nobody should take.


## Checkpoint and ownership

Include the authorized scope, underlying ticket, completed delivery and validation evidence, remaining work, cleanup resources and ownership, active restrictions, blockers, and exact next action. Use `--ticket <number>` to link the underlying task. Record integration separately from pending cleanup so the receiver can verify what already landed.

Create the handoff before releasing the underlying ticket. Taking a handoff closes only the handoff; claim the underlying ticket separately before modifying its work. Verify branch and revision warnings against the repository and retained resources before resuming. A handoff that points to already integrated work can still carry required cleanup.

Handoff creation requires a published branch containing the recorded commit. Honor publication restrictions: when publication is unavailable or unauthorized, preserve local work and record its location and recovery requirements on the underlying ticket instead. Confirm that checkpoint is durable before releasing ownership. Use an existing project handoff without overwriting another worker's state; the one-slot limit does not authorize dropping it. Inspect partial-success output before retrying any operation.


## issue-tracker handoff

Hands work to the next agent. A handoff is a ticket with the `handoff` label, routed to its project and in Todo on the board, whose body starts with `Branch: <branch>` and `Commit: <sha>` lines, then `Ticket: #<number>` when given, then a blank line and its text. A repository and its components share one slot: a project holds at most one open handoff, enforced by the ref `refs/handoffs/<owner>/<repo>` in the tracker repository, which exists while the handoff is open. Handoffs are not ready work: `issue list --ready` and `issue claim --next` leave them out. Each subcommand acts on the current repository's project, or on `--project`.


## issue-tracker handoff create

Opens the project's handoff. Before writing anything it checks, in one request, that the branch exists in the project's repository on GitHub and contains the commit, and resolves the commit to its full SHA. It then takes the slot, and creates the ticket with its routing label and the `handoff` label, which it creates on first use, on the board in Todo. Prints `created handoff #<number> in <project> at <branch> <sha>: <url>`. Fails without writing anything when the project already has an open handoff, naming it. When creating the ticket fails, it frees the slot again; when the ticket was created but adding it to the board failed, the error names the ticket.

- `--title` string: handoff title. Required.
- `--body` string: handoff text.
- `--body-file` string: file holding the text; `-` reads standard input. Bodies hold at most 65,536 characters, including the header lines.
- `--project` string: project. Defaults to the current repository's project.
- `--branch` string: branch the work is on. Defaults to the current checkout's branch, when the current checkout is the project's repository.
- `--sha` string: commit the work stopped at, full or abbreviated. Defaults to the current checkout's `HEAD`, when the current checkout is the project's repository.
- `--ticket` int: ticket the work belongs to, recorded in the handoff. Taking the handoff does not claim it.


## issue-tracker handoff show

Prints the project's open handoff without taking it. For people: lines `handoff: #<number> <title>`, `project:`, `branch:`, `commit:`, `ticket:` when one is recorded, `age:`, and `url:`, then a blank line and the text. For agents: `OK: <number>\t<project>\t<branch>\t<sha>`, then a blank line and the text when there is any. Prints `OK: no open handoff in <project>` when there is none.


## issue-tracker handoff take

Picks up the project's open handoff: claims it with the same atomic claim as `issue claim`, checks that the branch still contains the commit, closes it as completed, and frees the slot. Prints the handoff as `handoff show` does. When two agents take the same handoff, one wins and the other fails with `ticket is already claimed`. When the branch no longer contains the commit, or no longer exists, it still takes the handoff and prints `WARN: branch <branch> no longer contains commit <sha>: check where the work went before resuming` on stderr; when the check itself fails, it prints `WARN: could not check that branch <branch> still contains commit <sha>: <reason>` and still takes it. When closing fails, it gives the claim back, so the handoff can be taken again. When deleting the claim or freeing the slot fails after the close, it still prints the handoff, then fails with an error that starts `took handoff #<number>, but`; a slot left behind ends the error with `run issue-tracker handoff drop --free-slot`, and a claim left on the closed handoff needs nothing. It does not claim the ticket named by `Ticket:`.


## issue-tracker handoff untake

Gives back a handoff that `handoff take` closed, so the next agent can take it again. It reads the ticket, takes the slot of the ticket's project, deletes any claim that `take` left on the ticket, then reopens it in Todo on the board and posts `--reason`, in one write. Prints `returned handoff #<number> to <project> at <branch> <sha>: <title>`. The handoff keeps its branch, commit, and text; push any work done since taking it and say where it went in `--reason`. Fails without writing anything when the ticket is not a handoff, is open, was closed as anything but completed, or when the project already holds another open handoff, naming it. When deleting the claim or reopening fails, it frees the slot again.

- `<number>` int: the handoff that `handoff take` closed. Required.
- `--reason` string: why the handoff is given back, posted as a comment.


## issue-tracker handoff drop

Closes the project's open handoff as not planned without claiming it, and frees the slot so that a new handoff can be created. Prints `dropped handoff #<number> in <project>: <title>`. Fails with `no open handoff in <project>` when there is none, leaving the slot alone, because a create that has taken the slot but not yet created its ticket also has no open handoff.

- `--reason` string: why the handoff is dropped, posted as a comment.
- `--free-slot` bool: with no open handoff, free the slot that a create which failed partway left behind, and print `freed the handoff slot of <project>, which held no open handoff`. Use it only when no `handoff create` for the project is running.
