---
created_on: 2026-11-01 10:52
last_modified: 2026-11-03 13:41
status: current
---

# issue-tracker-cli

CLI that manages projects and tickets held in one private GitHub repository, with boards, atomic claims, and attachments.

## Commands
- **Prepare Dependencies:** `just setup`
- **Build Local Binary:** `just build` (compiles to `bin/issue-tracker`)
- **Run CLI with Arguments:** `just run [args...]`
- **Run CLI in Agent Mode:** `just run-ai [args...]` (`AGENT=1`)
- **Run Tests:** `just test` (`go test -race -cover ./...`)
- **Check Coverage:** `just coverage` (fails under 90% statement or function coverage in any package, as CI does)
- **Run Static Analysis & Tests:** `just check`
- **Run Linter / Static Analysis:** `just vet` or `just lint` (module hygiene, `gofmt`, `go vet`)
- **Format Source Code:** `just fmt` (`go fmt ./...`)

## Setup & Environment
- **Prerequisites:** Go 1.26.7+, `just`, and `gh` for image and video uploads and for the `account` and `agent_account` settings.
- **Temporary Files:** Temporary file operations use `.tmp/` within the project root.
- **Layout:** `cmd/issue-tracker/` holds commands and output, `internal/tracker/` holds GitHub operations, `internal/config/` resolves settings, `internal/fakegithub/` is the test double, and `skills/issue-tracker/` holds the agent skill.

## Conventions
- **Output Formatting & No Decorative Headers:** All CLI output must be plain text without emojis across all modes.
- **Help Screens & Terminal Width:** Help must use `github.com/alexgorbatchev/cobra-help-tree/v4` through `SetupWithOptions`, with positional arguments and environment variables documented in `techCatalog`.
- **Agent Mode (`AGENT=1`):** Lists print tab-separated rows and results print `OK:`, `WARN:`, or `ERR:` prefixes. Use `cobrahelptree.IsAgentMode`.
- **Displayed Paths:** Print every path through `displaypath.Fold`.
- **API Budget:** Make the fewest GitHub calls that give a correct result, and follow [GITHUB-RATE-LIMITING.md](GITHUB-RATE-LIMITING.md). Batch reads into one GraphQL request. Assert a request count in tests for every command that calls GitHub.
- **Never Enable the go-gh Cache:** It can replay mutations and stored 429 responses. Cache only the IDs in `metadata`, never ticket data.
- **Claims:** A claim is the ref `refs/claims/<number>` in the tracker repository, created through REST. GitHub rejects a second create, which makes it the only atomic operation this tool has; labels, assignees, milestones, and project fields accept concurrent writers. GraphQL cannot see this namespace.
- **Readiness Follows Beads:** A ticket is ready when it has a project, is open, does not have the `blocked` label, has no open blockers, no blocked parent or grandparent, and no open sub-issues, and is not in Doing or Done. An unrouted ticket is an inbox item until someone gives it a project on GitHub; never claim it or route it from the CLI on its own. A parent with open sub-issues waits for them, so that the work gets claimed rather than its container. The `blocked` label applies only to the ticket that carries it; a parent or grandparent counts as blocked only through open blockers.
- **Project Membership:** A ticket belongs to a project through its `repo:<name>` label or by being on its board, and the label wins if they disagree. People creating tickets inside a board fill in no routing, so read commands treat board membership as routing and never write the label back. A component's label wins over its repository's. On a repository's board, an unlabelled ticket belongs to the component its `Component` field value names, or else to the repository.
- **Project Registration:** A project exists when a `repo:<name>` label's description holds `owner/name`. `project add` names a repository of the tracker's owner `<name>` and any other owner's `<owner>/<name>`, and keeps the name of a repository already registered. A component exists when a `repo:<name>/<component>` label's description holds `owner/name:<path>`, where `<name>` is its repository's project name and the component holds no slash, so a name splits at its last slash; it has no board of its own and uses its repository's, whose single-select `Component` field holds one option per component. Read that field through the board's `fields` connection, because `field(name:)` fails the whole query on a board without it. Send existing options back with their IDs when changing options, or GitHub clears every ticket's value. Write the label last so a failed `project add` can simply be repeated. Link boards to the tracker repository only, never to a code repository.
- **Component Cache Coherence:** A component registered on another machine causes no cache miss, so every query that scopes to a repository project also reads its component labels and refreshes the cache once when they differ. Do not add a request for this check.
- **Secrets on the Wire:** Send the GitHub token only to GitHub's API hosts and to URLs matching `attachmentURL`. Downloads that redirect to a storage host rely on Go dropping the token across hosts; never copy it onto a redirect.
- **Attachment Storage:** GitHub's attachment endpoint accepts only images and video, so other files are assets of the tracker's `attachments` prerelease. Upload media through `gh` so that it rewrites body references; send it the body on standard input. Asset names start with the ticket number; removal deletes assets before rewriting links, and pruning only touches names this tool wrote.
- **Owner-Only Commands:** Wrap a command in `ownerOnly` when it changes what only the tracker's owner can, such as boards under the owner, registration labels, or issues moved out of the owner's repositories. It then acts as `account` even when `agent_account` applies.
- **Hermetic Unit Tests:** All unit tests must remain offline. Use `internal/fakegithub` as the transport.

## Agent Skill
- The skill lives only at `skills/issue-tracker/SKILL.md`. Never embed it in the binary.
- `issue-tracker skill` prints install instructions through `HelpOptions.Skill`, and every agent help screen starts with an alert to read it.
- Set versions only through `-ldflags` at release time. `currentVersion` falls back to the module version `go install` records, so never hardcode one.
- Update the skill in the same change as any command, argument, flag, default, environment variable, output, error, or side-effect change. Verify it against the implementation, update `last_modified`, and run `just check`. `TestSkillDocumentsCommandInterface` compares it with the live command tree.
- Write affirmative usage instructions and factual contracts in the skill. Keep development instructions and prohibitions in this file.

## Boundaries
- **Always:** For code changes that alter executable behavior, add or update meaningful behavioral tests. Require at least 90% line coverage and 90% function coverage across the non-test source of each affected first-party package, including unexecuted files and excluding `scripts/` directories. Measure with `go test -coverprofile`, and let `scripts/check-coverage.sh` enforce it. Statement coverage stands in for line coverage. Do not weaken coverage gates or make token test edits.
- **Always:** Run `just check` before committing changes.
- **Always:** Persist standing project instructions in this file and update the relevant rule in place.
- **Never:** Record task history, progress, handoffs, or dated user amendments in `AGENTS.md`.
- **Never:** Name a private tracker repository, or include ticket content, in this public repository.
- **Never:** Commit compiled Go binaries (e.g. `bin/issue-tracker`) to git.
- **Never:** Publish releases, tags, packages, or production deployments automatically without explicit user authorization.
