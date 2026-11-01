---
created_on: 2026-11-01 10:46
last_modified: 2026-11-01 12:20
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
| `issue list`, any filter, up to 100 tickets | 1 | One query, about 4 points with parent checks |
| `issue inspect <number>` | 1 | One query, plus one per extra 100 comments |
| `issue create` | 2 | Create with routing label and board, then set Todo. Add 1 when relating tickets, and 1 for `--label` |
| `issue claim <number>` | 3 | Read the ticket, create the claim ref, set Doing |
| `issue release <number>` | 3 | Read the ticket, delete the claim ref, set Todo |
| `issue close <number> --pr <n>` | 3 | Read the ticket, read the PRs, then one write combining comment, close, and Done. Add 1 when a claim exists |
| `issue status set <number> <status>` | 2 | Read the ticket, then write |
| `issue label add <number> <label...>` | 1 | One REST write |
| `issue label rm <number> <label...>` | 1 per label | One REST delete each |
| `issue comment add`, `edit`, `blocker`, `sub` | 2 | Resolve IDs, then write |
| `issue attachment add` | Unmeasured | Runs `gh issue edit --attach` |

Claim refs use REST because GraphQL cannot see `refs/claims/`. Deleting one is therefore always a separate request.

## Cache

Store a small metadata file under `~/.cache/issue-tracker/`, with owner-only permissions, containing only:

- Project names, their `owner/repo` mappings, and whether they are public
- GitHub Project, field, and status option IDs
- The tracker repository, owner, and head commit IDs

Refresh it when a project is not found, or when a write is rejected for a stale ID, then retry once. It needs no TTL, and a project added on another machine appears as an ordinary cache miss.

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
