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
a complete `Cookie` header. To read it from a local Firefox profile instead,
put `--firefox-cookies` before the command. Auto-discovery checks modern Firefox
install defaults before legacy profile defaults and searches the discovered
profiles for a matching host cookie, including cookies still in Firefox's live
write-ahead log. Firefox can remain open during discovery; the database snapshot
is retried if Firefox changes it while it is being copied.
`--firefox-profile PATH` selects only that profile and implies cookie discovery:

```bash
phab-feedback --firefox-cookies D123 submit
```

Credential requirements vary by command:

| Actions | Conduit token | Web session |
| --- | --- | --- |
| Queue listing, revision overview, `--threads`, `--timeline`, `comment` | Required | No |
| `reply`, `remove-comment`, `done` | Required | Required |
| `submit` | No | Required |
| `rate` (Mozilla only) | Required | Required |
| `ai-review` (Mozilla only) | No | Required |

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

# Publish all pending replies and Done changes in a separate action.
phab-feedback D123 submit

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
phab-feedback D123 --format json
phab-feedback D123 --threads=all --format json
phab-feedback D123 --timeline --format json
phab-feedback D123 done 456 457 --format json
```

`comment` and `remove-comment` take effect immediately. `reply` and `done` only
create drafts. `submit` publishes pending draft actions and comments. The
combined form is available only when immediate publication is intentional:

```bash
phab-feedback D123 reply 456 --message-file reply.txt --submit
```

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
- If a web command reports that it needs a session, set
  `PHAB_FEEDBACK_SESSION_COOKIE` or use a logged-in Firefox profile with
  `--firefox-cookies`.
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
