# Configuration and authentication


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


## issue-tracker auth refresh

Runs `gh auth refresh --hostname github.com --scopes project` to add the `project` scope to the GitHub CLI login. It keeps the login's other scopes, and shows a one-time code to approve in a browser. Requires a terminal: it refuses in agent mode and when stdin is not a terminal, without starting `gh`. Prints `project board access granted` on success.
