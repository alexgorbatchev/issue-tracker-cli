# Attachments

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


## issue-tracker issue attachment add <number> <file...>

Uploads files and appends them to the ticket's body. See [Attachments](attachments.md).


## issue-tracker issue attachment rm <number> <file...>

Deletes files of that ticket from the attachments release, then replaces their links in its body and comments with `(attachment removed)`, keeping a link's text. Takes asset names, such as `626-ddd5c89e-run.log`, or their links. Deletes nothing unless every file belongs to the ticket and is in the release. Images and video in GitHub's attachment storage cannot be deleted through the API; remove their links with `issue edit`. Use it at once when an attached file leaks a secret. Prints `removed <n> file(s) from #<number>`.


## issue-tracker issue attachment prune

Prints the names of attachment release files that their ticket no longer links to, one per line, which a failed write after an upload or an edit that dropped a link leaves behind. A file counts as linked when a link to it appears in its ticket's body or comments. Without `--yes`, deletes nothing and warns with the count. Files whose names this tool did not write are never listed. Reads tickets 50 per request.

- `--yes` bool: delete the listed files.


## issue-tracker issue attachment pull <number>

Downloads the images, videos, and files linked from a ticket's body and comments and prints one path per line. Files kept in the attachments release are listed once per run, at one request per 100 assets in it.

- `--dir` string: download directory. Defaults to `~/.cache/issue-tracker/attachments/<number>/`. A directory inside a Git worktree must be ignored by it.
