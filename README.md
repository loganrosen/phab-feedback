# phab-feedback

`phab-feedback` is a small command-line client for discovering, inspecting, and
acting on Phabricator and Phorge review feedback. It groups inline conversations
without losing exact comment IDs, preserves inline replies as real thread
replies, keeps draft actions explicit, and writes structured JSON for people and
automation.

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

Python 3.10 or newer is required. To run the
[PyPI package](https://pypi.org/project/phab-feedback/) without installing it
persistently:

```bash
uvx phab-feedback --help
```

For a persistent installation, use `uv`:

```bash
uv tool install phab-feedback
phab-feedback --help
```

`pipx` is a fallback when `uv` is unavailable:

```bash
pipx install phab-feedback
phab-feedback --help
```

Once a Phabricator host and Conduit token are available in `~/.arcrc`, discover
and inspect review work:

```bash
phab-feedback list --role reviewing
phab-feedback show D123
phab-feedback threads D123
phab-feedback timeline D123
```

With `uvx`, use the same command without the persistent install:

```bash
uvx phab-feedback timeline D123
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
put `--firefox-cookies` before the command; `--firefox-profile PATH` selects a
profile and implies cookie discovery:

```bash
phab-feedback --firefox-cookies submit D123
```

Credential requirements vary by command:

| Commands | Conduit token | Web session |
| --- | --- | --- |
| `list`, `show`, `threads`, `timeline`, `comment` | Required | No |
| `reply-inline`, `remove-comment`, `mark-done` | Required | Required |
| `submit` | No | Required |
| `mark-helpful`, `mark-unhelpful` (Mozilla only) | Required | Required |
| `request-ai-review` (Mozilla only) | No | Required |

The mutation commands that require both use Conduit to validate IDs and the web
session to perform the browser-only action. `XDG_CONFIG_HOME` and
`PHAB_FEEDBACK_ARCRC` are respected. Keep only non-secret settings in the JSON
config file.

## Commands and draft behavior

Successful commands write JSON to stdout. The read-only commands also support
`--format text` for compact interactive output. `comment` and `reply-inline`
read message text from `--message`, `--message-file PATH`, `--message-file -`,
or redirected stdin. File or stdin input avoids shell-quoting mistakes.

Discover revisions with `list`, use `show` for a summary, and use `threads` or
`timeline` to obtain the exact comment `id` values required by later commands.
Do not substitute transaction IDs or infer IDs from ordering.

```bash
# List open revisions where the authenticated user is responsible.
phab-feedback list

# List revisions where the user is a reviewer, with a reusable page cursor.
phab-feedback list --role reviewing --limit 25
phab-feedback list --role reviewing --after CURSOR_FROM_PREVIOUS_RESULT

# Filter by status and update time.
phab-feedback list --role authored --status all \
  --modified-after 2025-01-01T00:00:00Z

# Summarize metadata, reviewer state, and feedback counts.
phab-feedback show D123

# Group roots and replies, retaining exact IDs for every comment.
phab-feedback threads D123
phab-feedback threads D123 --state all --current-diff-only

# Read the complete chronological feedback timeline.
phab-feedback timeline D123

# Post an immediate top-level revision comment through Conduit.
phab-feedback comment D123 --message-file reply.txt

# Create a true inline-thread reply draft from a timeline comment ID.
phab-feedback reply-inline D123 456 --message-file - < reply.txt

# Create Done drafts from timeline inline-comment IDs.
phab-feedback mark-done D123 456 457

# Publish all pending replies and Done changes in a separate action.
phab-feedback submit D123

# Remove an accidental top-level comment after the CLI validates its type.
phab-feedback remove-comment D123 789
```

`list` supports `responsible`, `authored`, and `reviewing` roles. Its JSON
response contains normalized revision records plus the server `cursor`; pass a
non-null `cursor.after` value back through `--after` to continue. `show` reports
unresolved and resolved root threads, replies, orphan replies, general comments,
and comments on older diffs.

The `open` and `closed` filters normally use Phorge's status datasource
functions. If a server rejects those function tokens with HTTP 406, the CLI
retries with canonical status keys; this fallback treats Accepted as open,
matching the default Phorge policy.

`threads` defaults to unresolved roots. Each entry contains a `root`, ordered
`replies`, and a `resolved` flag. A reply whose parent is missing or cyclic is
reported under `orphan_replies` rather than attached by guesswork. Every root,
reply, and orphan retains its timeline `id`, PHID, diff, path, line, and direct
parent fields.

For interactive output:

```bash
phab-feedback list --role reviewing --format text
phab-feedback show D123 --format text
phab-feedback threads D123 --state all --format text
phab-feedback timeline D123 --format text
```

`comment` and `remove-comment` take effect immediately. `reply-inline` and
`mark-done` only create drafts. `submit` publishes pending draft actions and
comments. The combined form is available only when immediate publication is
intentional:

```bash
phab-feedback reply-inline D123 456 --message-file reply.txt --submit
```

`list`, `show`, `threads`, `timeline`, and `comment` use standard Conduit APIs.
Inline reply drafting, top-level comment removal, Done drafting, and draft
submission use internal web endpoints present in upstream Phabricator and
Phorge. Those endpoints are not a stable public API and may require
compatibility updates after a server release.

These commands depend on Mozilla's Review Helper extension and are not generic
Phabricator or Phorge features:

```bash
phab-feedback mark-helpful D123 456
phab-feedback mark-unhelpful D123 457
phab-feedback request-ai-review D123
```

Helpful and unhelpful ratings take effect immediately. An AI review request is
also sent immediately. These actions are never combined implicitly with reply,
Done, or submission actions.

## Optional agent skill

The agent skill and Python CLI install separately. The skill provides workflow
and approval guidance; it does not install the package or reimplement the CLI.
Install the skill with:

```bash
npx skills add loganrosen/phab-feedback@phab-feedback -g
```

At runtime the skill uses an installed `phab-feedback` command when available,
or `uvx phab-feedback` as a non-persistent fallback.

## Troubleshooting

- If multiple `.arcrc` hosts exist, select one explicitly, for example
  `phab-feedback --host https://phabricator.example.com timeline D123`.
- If a web command reports that it needs a session, set
  `PHAB_FEEDBACK_SESSION_COOKIE` or use a logged-in Firefox profile with
  `--firefox-cookies`.
- If Conduit commands work but a browser-only mutation fails after a server
  upgrade, the internal endpoint may have changed.

## Development

```bash
git clone https://github.com/loganrosen/phab-feedback.git
cd phab-feedback
uv sync --locked
uv run ty check --error-on-warning
uv run pytest
```

## Security

Credentials are sent only in request headers or bodies to the configured host.
Errors omit request bodies, tokens, and cookies. Avoid shell tracing while
setting credential environment variables.

## License

MIT
