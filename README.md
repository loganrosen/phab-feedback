# phab-feedback

`phab-feedback` is a small command-line client for reviewing and acting on
Phabricator and Phorge feedback. It keeps inline replies as real inline-thread
replies, exposes draft actions explicitly, and produces structured JSON suitable
for both people and automation.

## Install

Python 3.10 or newer is required.

```bash
python3 -m pip install .
phab-feedback --help
```

For development:

```bash
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
