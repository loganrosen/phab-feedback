# phab-feedback

`phab-feedback` is a small command-line client for reviewing and acting on
Phabricator and Phorge feedback. It keeps inline replies as real inline-thread
replies, exposes draft actions explicitly, and produces structured JSON suitable
for both people and automation.

## How this differs

[`arc`](https://we.phorge.it/book/phorge/article/arcanist/) and
[`moz-phab`](https://github.com/mozilla-conduit/review) handle author-side
Differential workflows such as creating or updating revisions from local
commits, with `arc` also providing landing workflows.
[`phabfive`](https://github.com/dynamist/phabfive) provides broader
Conduit-based access to Phabricator and Phorge applications such as Maniphest,
Paste, Diffusion, Passphrase, and User.

`phab-feedback` complements those tools by focusing on structured Differential
feedback, inline threads, explicit draft actions, and browser-only mutations
that Conduit does not expose. Its optional Mozilla Review Helper rating and
AI-review commands remain isolated from the generic Phabricator and Phorge
behavior.

## Install

Python 3.10 or newer is required.

After the first PyPI release is published, run the CLI without installing it:

```bash
uvx phab-feedback --help
```

For a persistent installation, `uv` is recommended:

```bash
uv tool install phab-feedback
phab-feedback --help
```

`pipx` is an alternative:

```bash
pipx install phab-feedback
phab-feedback --help
```

To try unreleased development from GitHub:

```bash
uv tool install git+https://github.com/loganrosen/phab-feedback.git
```

For local source development:

```bash
git clone https://github.com/loganrosen/phab-feedback.git
cd phab-feedback
python3 -m pip install -e .
python3 -m unittest discover -s tests
```

## Configuration

Choose a host with `--host`, `PHAB_FEEDBACK_HOST`, or
`~/.config/phab-feedback/config.json`, in that order:

```json
{
  "host": "https://phabricator.example.com",
  "cookie_name": "phsid"
}
```

If no host is configured and `~/.arcrc` contains exactly one host,
`phab-feedback` uses it. Conduit tokens come from `PHAB_FEEDBACK_TOKEN` or the
matching `~/.arcrc` entry. Tokens are never accepted as command-line arguments.

Internal web actions need a logged-in browser session. Set
`PHAB_FEEDBACK_SESSION_COOKIE` to a complete `Cookie` header value, or to the
value of the configured session cookie. The value is never printed. Mozilla
Phabricator users can instead pass `--firefox-cookies` to discover the session
from a local Firefox profile; `--firefox-profile` selects a specific profile.

`XDG_CONFIG_HOME` and `PHAB_FEEDBACK_ARCRC` are respected. The config file is
for non-secret settings; keep tokens in `.arcrc` or the environment and session
cookies in the environment or browser store.

## Commands

All successful commands write JSON to stdout. Message-taking commands accept
exactly one of `--message`, `--message-file PATH`, or `--message-file -`.
When stdin is redirected, it is also accepted without an option.

```bash
# Read the complete chronological feedback timeline.
phab-feedback timeline D123

# Post an immediate top-level revision comment through Conduit.
phab-feedback comment D123 --message-file reply.txt

# Create a true inline-thread reply draft, then publish it separately.
printf '%s\n' 'Handled in the latest update.' |
  phab-feedback reply-inline D123 456
phab-feedback submit D123

# Explicitly create and publish an inline reply in one invocation.
phab-feedback reply-inline D123 456 --message 'Done.' --submit

# Remove an accidental top-level comment after validating its type.
phab-feedback remove-comment D123 789

# Create Done drafts, then submit them.
phab-feedback mark-done D123 456 457
phab-feedback submit D123
```

`timeline`, `comment`, and the metadata validation used by mutations rely on
standard Conduit APIs. Inline reply drafting, top-level comment removal, Done
drafting, and draft submission use internal web endpoints available in upstream
Phabricator and Phorge. Those endpoints are less stable than Conduit and may
change between server releases.

These commands are **Mozilla-only** because they use the Review Helper
extension, not upstream Phabricator:

```bash
phab-feedback mark-helpful D123 456
phab-feedback mark-unhelpful D123 457
phab-feedback request-ai-review D123
```

Helpful and unhelpful ratings take effect immediately. Inline replies and Done
states remain drafts until `submit`; `reply-inline --submit` is the only
intentional combined workflow. Rating, Done, and reply actions are never
combined implicitly.

The repository also includes an optional thin agent skill. It contains workflow
and approval guidance only; the CLI remains the single implementation of all
deterministic behavior. Install it through the open Skills CLI:

```bash
npx skills add loganrosen/phab-feedback@phab-feedback -g
```

The Skills CLI handles the supported agent-specific installation paths. The
Python package does not modify agent configuration or install the skill
automatically.

## Security

Credentials are sent only in request headers or bodies to the configured host.
Errors omit request bodies, tokens, and cookies. Avoid enabling shell tracing
while setting credential environment variables.

## License

MIT
