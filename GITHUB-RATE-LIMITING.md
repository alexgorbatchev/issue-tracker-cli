---
created_on: 2026-11-01 10:46
last_modified: 2026-11-03 14:14
status: current
---

# GitHub Rate Limiting

How `issue-tracker` stays within GitHub's API limits. Read this before changing code that calls GitHub, and before writing automation around the CLI.

## Rules

1. Make the fewest API calls that return a correct result.
2. Batch reads into one GraphQL request. Do not read a list and then query each item.
3. Fetch all ticket data live. Cache only the metadata listed under [Cache](#cache).
4. Run writes one at a time. Never send them concurrently.
5. Do not poll in tight loops. Every agent on the account draws from the same budget.
6. Treat every rate-limit response as a stop signal and follow [Handling limit responses](#handling-limit-responses).

## Limits

The primary limits are shared across every token, machine, and agent acting for the account.

| Limit | Value |
| --- | --- |
| REST primary | 5,000 requests per hour |
| GraphQL primary | 5,000 points per hour |
| `GITHUB_TOKEN` in Actions, GraphQL | 1,000 points per hour per repository |
| Concurrent requests | 100, shared across REST and GraphQL |
| REST secondary | 900 points per minute, per endpoint |
| GraphQL secondary | 2,000 points per minute |
| CPU time | 90 seconds per 60 seconds of real time, at most 60 from GraphQL |
| Content creation | About 80 requests per minute and 500 per hour, stricter on some endpoints |

Secondary limit point costs:

| Request | Points |
| --- | --- |
| REST `GET`, `HEAD`, `OPTIONS` | 1 |
| REST `POST`, `PATCH`, `PUT`, `DELETE` | 5 |
| GraphQL without mutations | 1 |
| GraphQL with mutations | 5 |

Web interface actions also count toward content creation.

Sources: [REST rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api), [GraphQL rate limits](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).

## GraphQL cost

The primary cost of a query is calculated in three steps:

1. Count the requests needed for each connection, assuming each one returns its full `first` or `last` count.
2. Add them together and divide by 100.
3. Round to the nearest whole number, with a minimum of 1 point.

Every connection needs a `first` or `last` value from 1 to 100, and one call cannot request more than 500,000 nodes.

Example: 100 issues with `first: 20` on labels, comments, and blockers needs 1 + (100 × 3) = 301 requests, which costs about 3 points.

Request only the fields a command needs, and keep nested `first` values small. Read the actual cost from `rateLimit { cost remaining resetAt }`.

## Expected command cost

Tests assert these budgets with a recording fake, so a change that adds a request fails. Cached metadata is assumed.

| Command | Requests | Reads and writes |
| --- | --- | --- |
| `issue list`, up to 100 tickets | 1 | One query, about 6 points with parent and sub-issue checks. GitHub reports the total on that page |
| `issue list`, `claim --next`, `close-eligible`, `project inspect`, or `project rm` in one repository project | Add 1 per 100 board tickets with no routing label | The issues connection filters by label only, so a search finds board tickets without any of the repository's or its components' labels. Its minimum is 1. A component makes the same search and keeps the tickets whose `Component` value names it |
| The same commands in a repository project whose components changed on another machine | Add one metadata refresh, 2 requests, and repeat the read once | The first read also returns the repository's component labels, at no extra request, and differs from the cache |
| Any read that meets a ticket on an unregistered board, or labelled for an unregistered project | Add one metadata refresh per command | The board or project may have been registered on another machine, so the cached IDs are refreshed once |
| `issue list` with `--status`, `--label`, `--priority`, `--ready`, or `--stale` | 1 per 100 tickets | Reads every page, whatever `--limit` says, because GitHub cannot count these filters and the total has to be counted here. `--ready` sorts every match by priority before applying the limit, at no extra request |
| `issue list --unrouted` | 1 per 100 open tickets with no routing label | One search excluding every registered routing label, read to the end whatever `--limit` says. Tickets routed by their board are dropped locally; an unregistered board adds the metadata refresh above |
| `issue list --search` | 1 per 100 matches | One search query. GitHub ends a query at 1,000 results, so each further 1,000 starts a new query from the last creation time |
| `issue inspect <number...>` | 1 | One query for up to 50 tickets, with one aliased `issue` field each, plus one per extra 100 comments on a ticket. A dry run cost 1 point for one ticket, 2 for 20, and 5 for 50 |
| `issue create` | 3 | Create with routing label, add to the board, set Todo. Add 1 when relating tickets, 1 for `--label`, and 1 to 3 when GitHub has already added the item, which its sub-issue workflow can do. Passing the board to `createIssue` would save a request but makes that collision routine |
| `issue claim <number>` | 3 | Read the ticket, create the claim ref, set Doing |
| `issue release <number>` | 3 | Read the ticket, delete the claim ref, set Todo |
| `issue close <number> --pr <n>` | 3 | Read the ticket, read the PRs, then one write combining comment, close, and Done. Add 1 when a claim exists |
| `project add --path <dir>` | 5 to 6 | Refresh metadata with its visibility read, create the `Component` field or read its options and write them back with the new one, read the label, create it. The new component is written to the cache without another refresh. 2 when it is already registered with an option, 3 to 4 when its option is missing |
| `project rm <component>` | 5 to 7 | Search the board, count tickets, read the `Component` options and write them back without its option (or delete the field with its last option), clear the label, then refresh metadata with its visibility read |
| `project rm <project>` | 6 to 7 | Search the board, count tickets, delete the board, clear the label, then refresh metadata with its visibility read. One more request labels every closed ticket that was only on the board |
| `issue close <number> --landed-pr <n>` | 4 | Read the ticket, read the PR, compare its head with its base branch, then one write combining comment, close, and Done. Add 1 when a claim exists |
| `issue status set <number> <status>` | 2 | Read the ticket, then write |
| `issue label add <number> <label...>` | 1 | One REST write |
| `issue label rm <number> <label...>` | 1 per label | One REST delete each |
| `issue comment add`, `edit`, `blocker`, `sub add`, `sub rm` | 2 | Resolve IDs, then write |
| `issue sub list <parent>` | 1 | One query |
| `issue sub close-eligible` | 1 per 100 tickets | Reads every page, then 2 for each parent closed. `--dry-run` writes nothing |
| `--attach` with images or video, and `issue attachment add` | Unmeasured | Runs `gh issue edit` or `gh issue comment` with `--attach`, passing any body on standard input |
| `--attach` with other files | 2 per file, plus 1 | Read the `attachments` release (creating it adds 1), then one upload per file. The body is then written by `gh`, or by one GraphQL request, 2 for an edit. `issue attachment add` without media reads the current body first |
| `issue attachment rm <number> <file...>` | 3 + 1 per 100 assets, then 1 per file and 1 write | Read the ticket, the release, and its assets, delete each file, then rewrite every changed body and comment in one request. Each delete is a REST write, 5 secondary points |
| `issue attachment prune` | 2 + 1 per 100 assets + 1 per 50 tickets with files | Read the release and its assets, then the tickets their names point at, with further comment pages as `issue inspect` reads them. `--yes` adds one REST delete per listed file |
| `issue attachment pull` with release files | Add 2, plus 1 per further 100 assets, then 1 per file | Read the release, list its assets, then download each file through the API |

Claim refs use REST because GraphQL cannot see `refs/claims/`. Deleting one is therefore always a separate request.

`project import` makes three write requests holding four mutations per issue and waits 32 seconds between issues, which stays near 450 mutations an hour. Public issues add one read each to recheck authors. An issue GitHub already added to the board adds up to three reads to find its item. `project scan` costs about 10 points for each 10 repositories with open issues, plus one point for each further page of 100 issues, and one request per 100 repositories to find them.

## Cache

Store a small metadata file under `~/.cache/issue-tracker/`, with owner-only permissions, containing only:

- Project names, their `owner/repo` mappings, and whether they are public
- GitHub Project, field, and status option IDs
- The tracker repository, owner, and head commit IDs

Refresh it when a project is not found, or when a write is rejected for a stale ID, then retry once. It needs no TTL, and a project added on another machine appears as an ordinary cache miss. A component added on another machine causes no miss, so every read scoped to a repository project also reads that repository's component labels and refreshes once when they differ from the cache.

A refresh reads labels 100 at a time with the search `repo:`, which also returns some labels without that prefix. A tracker with more than 100 such labels needs one more request per refresh. Each board's fields are read 50 at a time to find its `Component` field, which adds about one point to a refresh and no request.

Never cache:

- Ticket bodies, comments, labels, or status, which would put private text on disk and give agents stale work queues
- `issue list --ready` results, which could give two agents the same stale ticket
- Pull request merge state, which `issue close` must confirm live
- Signed attachment URLs, which are short-lived credentials

### Do not use the `go-gh` cache

Keep `EnableCache` off in `go-gh` `ClientOptions`. Its [cache rules](https://github.com/cli/go-gh/blob/trunk/pkg/api/cache.go) are unsafe here:

- `isCacheableRequest` accepts every `POST` to `/graphql`, and mutations use the same request. An identical repeated write can return a stored response without reaching GitHub.
- `isCacheableResponse` rejects only 403 and 5xx responses, so a 429 can be stored. `CacheTTL` defaults to 24 hours, which can replay a rate-limit error long after the limit resets.

## Handling limit responses

**Primary limit:** the response is 403 or 429 with `x-ratelimit-remaining: 0`. Do not retry until the time in `x-ratelimit-reset`, given in UTC epoch seconds.

**Secondary limit:** the response is 403 or 429 with a message naming the secondary limit. Apply the first case that matches:

1. If `retry-after` is present, wait that many seconds.
2. If `x-ratelimit-remaining` is `0`, wait until `x-ratelimit-reset`.
3. Otherwise, wait at least one minute.

If requests keep failing, back off exponentially and stop after a fixed number of retries. GitHub warns that continuing to send requests while limited can get an integration banned.

Use the `x-ratelimit-*` headers for pacing, but do not depend on exact remaining counts, because GitHub says values can vary by region.

## Checking usage

```bash
gh api rate_limit --jq '.resources | {core, graphql}'
```

## Unverified

- GitHub's documentation does not say whether `304` responses count toward limits, or whether GraphQL supports conditional requests. Do not design around either.
- GitHub's documentation does not say how a single request with several mutations counts toward content creation. Assume each mutation counts.
- The API cost of `gh ... --attach` uploads is unmeasured.
