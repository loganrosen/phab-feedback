# phab-feedback

`phab-feedback` is a small command-line client for discovering, inspecting, and
acting on Phabricator and Phorge review feedback. It groups inline conversations
without losing exact comment IDs, preserves inline replies as real thread
replies, keeps draft actions explicit, and provides readable output for people
with structured JSON available for automation.

## How this differs

[`arc`](https://we.phorge.it/book/phorge/article/arcanist/) and
[`moz-phab`](https://github.com/mozilla-conduit/review) handle author-side
Differential workflows such as creating or updating revisions from local
commits. [`phabfive`](https://github.com/dynamist/phabfive) provides broader
Conduit access across Phabricator and Phorge applications.

`phab-feedback` complements those tools by focusing on Differential feedback
timelines, inline threads, explicit draft actions, and a few browser-only
mutations that Conduit does not expose. Mozilla Review Helper support is kept
separate from the generic behavior.

## Install and quick start

Download a native binary from the
[latest GitHub release](https://github.com/loganrosen/phab-feedback/releases/latest),
or install from source with Go 1.27.1 or newer:

```bash
go install github.com/loganrosen/phab-feedback/cmd/phab-feedback@latest
phab-feedback --help
```

To run the latest version without installing an executable:

```bash
go run github.com/loganrosen/phab-feedback/cmd/phab-feedback@latest --help
```

Once a Phabricator host and Conduit token are available in `~/.arcrc`, discover
and inspect review work:

```bash
phab-feedback list --role reviewing
phab-feedback doctor
phab-feedback D123
phab-feedback D123 --threads=all
phab-feedback D123 --timeline
```

With `go run`, use the same command without a persistent executable:

```bash
go run github.com/loganrosen/phab-feedback/cmd/phab-feedback@latest D123 --timeline
```

## Configuration and credentials

Select a host with the global `--host` option, `PHAB_FEEDBACK_HOST`, or
`~/.config/phab-feedback/config.json`, in that order:

```json
{
  "host": "https://phabricator.example.com",
  "cookie_name": "phsid"
}
```

If none is set and `~/.arcrc` contains exactly one host, that host is used.
Conduit tokens come from `PHAB_FEEDBACK_TOKEN` or the matching `.arcrc` entry.
Tokens are not accepted as command-line arguments.

Browser-only actions also need a logged-in web session. Set
`PHAB_FEEDBACK_SESSION_COOKIE` to either the configured session-cookie value or
a complete `Cookie` header. When that variable is not set, the CLI automatically
searches local Firefox profiles for a matching host session. Discovery checks
modern Firefox install defaults before legacy profile defaults and includes
cookies still in Firefox's live write-ahead log. Firefox can remain open during
discovery; the database snapshot is retried if Firefox changes it while it is
being copied.

`--firefox-profile PATH` restricts discovery to that profile.
`--firefox-cookies` requests direct Firefox discovery diagnostics instead of
the automatic-fallback error wrapper. Explicit session-cookie environment
values take precedence over explicit profile selection, which takes precedence
over automatic discovery:

```bash
phab-feedback D123 submit
phab-feedback --firefox-profile ~/.mozilla/firefox/example.default-release D123 submit
```

Credential requirements vary by command:

| Actions | Conduit token | Web session |
| --- | --- | --- |
| Queue listing, revision overview, `--threads`, `--timeline`, `comment` | Required | No |
| `reply`, `remove-comment`, `done`, `batch` mutations | Required | Required |
| `batch --dry-run` | Required | No |
| `submit` | No | Required |
| `verify` | Required | No |
| `rate` (Mozilla only) | Required | Required |
| `ai-review` (Mozilla only) | No | Required |

Run `phab-feedback doctor` to check host resolution, Conduit authentication,
the browser session and CSRF token, and whether Review Helper is visible on the
host homepage. The command only performs read-only requests and supports
`--format json` for automated diagnostics. Missing optional browser credentials
are reported as warnings; invalid configured credentials fail the command.
Review Helper detection is best-effort because not every installation links it
from the host homepage.

The mutation commands that require both use Conduit to validate IDs and the web
session to perform the browser-only action. `XDG_CONFIG_HOME` and
`PHAB_FEEDBACK_ARCRC` are respected. Keep only non-secret settings in the JSON
config file.

## Commands and draft behavior

Successful commands write compact, human-readable text to stdout. Every command
supports `--format json` for agents, scripts, and other structured consumers.
Interactive terminals use color and emphasis for IDs, statuses, and metadata.
Styling is removed when output is redirected, and color can be disabled with
`NO_COLOR`.
`comment` and `reply` read message text from `--message`,
`--message-file PATH`, `--message-file -`, or redirected stdin. File or stdin
input avoids shell-quoting mistakes.

Running `phab-feedback` lists the default responsible/open queue. Pass a
revision first to establish context; the default revision view combines its
summary with unresolved threads. Use `--threads` or `--timeline` to obtain the
exact comment `id` values required by later actions. Do not substitute
transaction IDs or infer IDs from ordering.

```bash
# List open revisions where the authenticated user is responsible.
phab-feedback

# List revisions where the user is a reviewer, with a reusable page cursor.
phab-feedback list --role reviewing --limit 25
phab-feedback list --role reviewing --after CURSOR_FROM_PREVIOUS_RESULT

# Filter by status and update time.
phab-feedback list --role authored --status all \
  --modified-after 2025-01-01T00:00:00Z

# Summarize metadata, reviewer state, feedback counts, and unresolved threads.
phab-feedback D123

# Group roots and replies, retaining exact IDs for every comment.
phab-feedback D123 --threads
phab-feedback D123 --threads=all --current-diff-only

# Read the complete chronological feedback timeline.
phab-feedback D123 --timeline

# Post an immediate top-level revision comment through Conduit.
phab-feedback D123 comment --message-file reply.txt

# Create a true inline-thread reply draft from a timeline comment ID.
phab-feedback D123 reply 456 --message-file - < reply.txt

# Create Done drafts from timeline inline-comment IDs.
phab-feedback D123 done 456 457

# Create Done drafts and explicitly publish pending drafts once.
phab-feedback D123 done 456 457 --submit

# Publish all pending replies and Done changes in a separate action.
phab-feedback D123 submit

# Verify visible reply linkage and Conduit's Done indicator.
phab-feedback D123 verify --reply 901:456 --done 456 --done 457

# Remove an accidental top-level comment after the CLI validates its type.
phab-feedback D123 remove-comment 789
```

`list` supports `responsible`, `authored`, and `reviewing` roles. With
`--format json`, its response contains normalized revision records plus the
server `cursor`; pass a non-null `cursor.after` value back through `--after` to
continue. The default revision overview reports unresolved and resolved root
thread counts, replies, orphan replies, general comments, comments on older
diffs, and the unresolved thread details.
When Mozilla's merge-conflict custom field is available, revision records also
include `merge_conflict_status`, and text output highlights conflicts, unknown
results, and checks being recomputed.

The `open` and `closed` filters normally use Phorge's status datasource
functions. If a server rejects those function tokens with HTTP 406, the CLI
retries with canonical status keys; this fallback treats Accepted as open,
matching the default Phorge policy.

`--threads` defaults to unresolved roots. Each entry contains a `root`, ordered
`replies`, and a `resolved` flag. A reply whose parent is missing or cyclic is
reported under `orphan_replies` rather than attached by guesswork. Every root,
reply, and orphan retains its timeline `id`, PHID, diff, path, line, and direct
parent fields.

For structured output:

```bash
phab-feedback list --role reviewing --format json
phab-feedback doctor --format json
phab-feedback D123 --format json
phab-feedback D123 --threads=all --format json
phab-feedback D123 --timeline --format json
phab-feedback D123 done 456 457 --format json
phab-feedback D123 verify --reply 901:456 --done 456 --format json
```

`comment` and `remove-comment` take effect immediately. `reply` and `done` only
create drafts. `submit` publishes pending draft actions and comments. The
combined forms are available only when immediate publication is intentional:

```bash
phab-feedback D123 reply 456 --message-file reply.txt --submit
phab-feedback D123 reply 456 --message-file reply.txt --done --submit
phab-feedback D123 done 456 457 --submit
```

`reply --done` always creates and saves the reply draft before attempting the
Done draft. With `--submit`, the CLI submits once only after both draft
operations succeed. During that final submission, Phabricator publishes
eligible inline drafts before applying their Done-state transition and commits
the transaction set together. Draft preparation still requires separate web
requests, so a failure before submission can leave unpublished drafts behind.
The command exits nonzero and its text or JSON output identifies any mutation
whose remote state may need inspection. If a Done retry is interrupted, the
output identifies whether the first confirmed toggle created a pending
undo-Done draft or cleared a pending Done draft and gives the required recovery
step.

There is an unavoidable interruption window between the two Done toggle
requests used to reconcile ambiguous upstream state. If the process is killed
or loses connectivity before it can report a result, inspect the comment and
rerun `done` before any later submission.

Phabricator submission is revision-wide for the current user: `submit` and
every `--submit` form publish all eligible pending inline drafts owned by that
user on the revision, including drafts created earlier in the browser or by
another command. Inspect existing drafts before approving publication.
The CLI only reports publication when Phabricator returns its success redirect.
An upstream `Empty Comment` dialog means there was nothing to publish and is
reported as `outcome: "no-effect"`. All other server dialogs and warnings,
including `Action(s) With No Effect` confirmations, are surfaced as failures and
are never automatically overridden. If submission reports an inline still
being edited, save or close that editor in Phabricator and retry after reviewing
every pending draft. A `done --submit` or batch submission is not attempted
when every target was already published Done and the command created no new
drafts; use the standalone `submit` command if existing unrelated drafts should
still be published.

Mutation JSON includes the revision and action plus operation-specific fields
such as `created_reply_id`, `parent_comment_id`, `draft`, `published`, and
`final_done`. Submission results include `outcome`, `attempted`, `submitted`,
and, when applicable, a bounded plain-text `dialog`, `outcome_unknown`, or
`recovery`. `outcome: "not-attempted"` identifies a deliberate workflow skip,
while `outcome: "blocked"` identifies a pre-request failure such as an
unavailable CSRF token. Both have `attempted: false`; the server-confirmed
`no-effect` outcome has `attempted: true`. Batch dry-run entries instead use
`planned: true`; they do not claim draft, publication, or final Done state
before mutation.
If a Done retry fails, `observed_checked` and `observed_draft_state` describe
only the last confirmed response; normal result fields remain absent because
the final remote outcome is unknown. Multi-target Done failures list later
comment IDs under `not_attempted`.

The `verify` command exits nonzero when a requested reply is missing, its direct
parent does not match, or Conduit reports `isDone: false`. Reply verification
checks visibility and direct-parent linkage but does not independently prove
publication. The top-level `status` is `verified` for definitive reply-only
checks, `observed` when all requested checks pass but Done state remains
ambiguous, and `failed` when a check fails. `checks_passed` controls the command
exit status. Done verification reports `state: "done-or-pending-undo"` and sets
the result-level `done_state_ambiguous` flag: upstream Conduit represents both
published `DONE` and a pending `UNDRAFT` transition as `isDone: true`, so it
cannot prove that no pending undo-Done draft exists. Text output prints the
same limitation.

### Batch action manifests

Use `batch` to validate and execute an ordered set of inline replies and Done
changes. The manifest contains the revision and one entry per target comment:

```json
{
  "revision": "D307925",
  "actions": [
    {
      "comment_id": 123456,
      "reply": "Updated this to preserve the existing behavior.",
      "done": true
    },
    {
      "comment_id": 123457,
      "reply": "Added the requested test."
    },
    {
      "comment_id": 123458,
      "done": true
    }
  ]
}
```

Unknown fields, duplicate targets, empty replies, unsupported `done: false`
values, missing comments, removed comments, and non-inline targets are rejected
before any mutation. Preview the complete plan first:

```bash
phab-feedback batch actions.json --dry-run
```

Without `--submit`, the command creates drafts in manifest order. With
`--submit`, it creates every draft first and makes exactly one submission after
all draft operations succeed:

```bash
phab-feedback batch actions.json
phab-feedback batch actions.json --submit --format json
```

That final submission is not scoped to the manifest. It publishes every
eligible pending inline draft owned by the current user on the revision,
including pre-existing browser drafts.
If every requested Done state is already published and the batch creates no
new draft, the CLI does not attempt the final submission and reports
`state: "unchanged"` rather than publishing unrelated drafts.

Validation failures never mutate the revision. Phabricator applies the final
published inline transactions together, but the preceding draft-creation calls
are separate requests and can not be rolled back as a unit. A network or server
failure can therefore leave earlier drafts behind. In that case the command
exits nonzero and reports `state: "partial"`, attempted or completed mutations,
and the failed action; it never submits after a draft operation fails. Each
mutation has a unique `mutation_index` plus its source manifest
`action_index`, so combined reply-and-Done entries remain unambiguous.
Phabricator can also return an HTTP-success dialog instead of accepting a
submission. The CLI treats that as a partial failure, preserves the last
confirmed draft state, and reports the dialog rather than claiming that the
mutations were published.

Queue listing, revision inspection, and `comment` use standard Conduit APIs.
Inline reply drafting, top-level comment removal, Done drafting, and draft
submission use internal web endpoints present in upstream Phabricator and
Phorge. Those endpoints are not a stable public API and may require
compatibility updates after a server release.

These commands depend on Mozilla's Review Helper extension and are not generic
Phabricator or Phorge features:

```bash
phab-feedback D123 rate 456 --helpful
phab-feedback D123 rate 457 --unhelpful
phab-feedback D123 ai-review
```

Helpful and unhelpful ratings take effect immediately. An AI review request is
also sent immediately. These actions are never combined implicitly with reply,
Done, or submission actions.

## Optional agent skill

The agent skill and CLI install separately. The skill provides workflow
and approval guidance; it does not install the package or reimplement the CLI.
Install the skill with:

```bash
npx skills add loganrosen/phab-feedback@phab-feedback -g
```

At runtime the skill uses an installed `phab-feedback` command when available,
or `go run` as a non-persistent fallback.

## Troubleshooting

- If multiple `.arcrc` hosts exist, select one explicitly, for example
  `phab-feedback --host https://phabricator.example.com D123 --timeline`.
- Run `phab-feedback doctor` to identify whether a failure is in host
  configuration, Conduit authentication, or the browser session.
- If a web command reports that it needs a session, set
  `PHAB_FEEDBACK_SESSION_COOKIE` or use a logged-in Firefox profile with
  `--firefox-profile`.
- If Conduit commands work but a browser-only mutation fails after a server
  upgrade, the internal endpoint may have changed.

## Development

```bash
git clone https://github.com/loganrosen/phab-feedback.git
cd phab-feedback
go test ./...
go vet ./...
go build ./cmd/phab-feedback
```

## Security

Credentials are sent only in request headers or bodies to the configured host.
Errors omit request bodies, tokens, and cookies. Avoid shell tracing while
setting credential environment variables.

## License

MIT
