# phab-feedback

`phab-feedback` is a small command-line client for inspecting and acting on
Phabricator and Phorge review feedback. It preserves inline replies as real
inline-thread replies, keeps draft actions explicit, and writes structured JSON
for people and automation.

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

Once a Phabricator host and Conduit token are available in `~/.arcrc`, inspect
a revision:

```bash
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
| `timeline`, `comment` | Required | No |
| `reply-inline`, `remove-comment`, `mark-done` | Required | Required |
| `submit` | No | Required |
| `mark-helpful`, `mark-unhelpful` (Mozilla only) | Required | Required |
| `request-ai-review` (Mozilla only) | No | Required |

The mutation commands that require both use Conduit to validate IDs and the web
session to perform the browser-only action. `XDG_CONFIG_HOME` and
`PHAB_FEEDBACK_ARCRC` are respected. Keep only non-secret settings in the JSON
config file.

## Commands and draft behavior

Successful commands write JSON to stdout. `comment` and `reply-inline` read
message text from `--message`, `--message-file PATH`, `--message-file -`, or
redirected stdin. File or stdin input avoids shell-quoting mistakes.

Start with the timeline and use its comment `id` values for later commands. Do
not substitute transaction IDs or infer IDs from ordering.

```bash
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

`comment` and `remove-comment` take effect immediately. `reply-inline` and
`mark-done` only create drafts. `submit` publishes pending draft actions and
comments. The combined form is available only when immediate publication is
intentional:

```bash
phab-feedback reply-inline D123 456 --message-file reply.txt --submit
```

`timeline` and `comment` use standard Conduit APIs. Inline reply drafting,
top-level comment removal, Done drafting, and draft submission use internal web
endpoints present in upstream Phabricator and Phorge. Those endpoints are not a
stable public API and may require compatibility updates after a server release.

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
python3 -m pip install -e .
python3 -m unittest discover
```

## Security

Credentials are sent only in request headers or bodies to the configured host.
Errors omit request bodies, tokens, and cookies. Avoid shell tracing while
setting credential environment variables.

## License

MIT
