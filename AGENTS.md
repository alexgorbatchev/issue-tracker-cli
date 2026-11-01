---
created_on: 2026-11-01 10:52
last_modified: 2026-11-01 10:52
status: current
---

# issue-tracker-cli

CLI for managing projects and tickets in a private GitHub issue tracker

## Commands
- **Build Local Binary:** `just build` (compiles to `bin/issue-tracker`)
- **Run CLI with Arguments:** `just run [args...]`
- **Run CLI in Agent Mode:** `just run-ai [args...]` (`AGENT=1`)
- **Run Tests:** `just test` (`go test -v ./...`)
- **Run Static Analysis & Tests:** `just check` (`go vet ./... && go test -v ./...`)
- **Run Linter / Static Analysis:** `just vet` or `just lint` (`go vet ./...`)
- **Format Source Code:** `just fmt` (`go fmt ./...`)

## Setup & Environment
- **Prerequisites:** Go 1.26.2+, `just`.
- **Temporary Files:** Temporary file operations use `.tmp/` within the project root.

## Conventions
- **Output Formatting & No Decorative Headers:** All CLI output must be plain text without emojis across all modes.
- **Help Screens & Terminal Width:** CLI help output (`--help`) must display an aligned hierarchical tree view with `├─` and `╰─` glyphs powered by `github.com/alexgorbatchev/cobra-help-tree/v2` (`cobrahelptree.Setup(rootCmd)`), with command descriptions automatically trimmed to the terminal width using ellipsis (`...`).
- **Agent Mode (`AGENT=1`):** When `AGENT=1` is set, output compact key-value pairs or bullets.
- **Hermetic Unit Tests:** All unit tests must remain 100% offline and hermetic.

## Embedded Agent Skill

- `issue-tracker skill` prints `cmd/issue-tracker/SKILL.md` verbatim, embedded with `//go:embed`.
- Every agent-mode help screen starts with an alert to read `AGENT=1 issue-tracker skill` first.
- Keep the skill in sync in the same change whenever public commands, arguments, flags, shorthands, types, defaults, accepted values, environment variables, outputs, errors, or side effects change. Include generated help and completion commands.
- Verify against the implementation and pinned dependencies, update `last_modified`, and run `just check`. Maintain `TestSkillDocumentsCommandInterface` coverage of the live command tree.
- Write affirmative usage instructions and factual contracts in the skill. Keep development instructions and negative prohibitions in this file.
- Do not use the usage skill for developing the Go implementation, invent unsupported operations, read repository files at runtime to print the skill, or maintain a second copy of its text.

## Boundaries
- **Always:** Maintain >= 90% statement code coverage across Go packages.
- **Always:** Run `just test` and `just vet` before committing changes.
- **Never:** Commit compiled Go binaries (e.g. `bin/issue-tracker`) to git.
