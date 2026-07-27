---
name: phab-feedback
description: Discover, inspect, and act on Phabricator or Phorge Differential feedback with the phab-feedback CLI. Use for reviewer or author revision queues, revision summaries, unresolved inline threads, chronological timelines, exact general or inline comment IDs, inline-thread reply drafts, accidental top-level comment removal, Done drafts, explicit draft submission, and Mozilla Review Helper ratings or AI review requests. Trigger when an agent needs deterministic review metadata, must classify feedback across diff versions, needs to triage review work, or is ready to perform a user-approved feedback mutation.
---

# Phabricator feedback

Use the CLI for all deterministic operations. Do not reimplement its HTTP
requests or expose tokens and cookies in commands or output.

## Select a runner

Use an installed `phab-feedback` first. Otherwise use `uvx phab-feedback`
without installing it persistently. Stop with a clear installation error if
neither runner exists; do not install tools on the user's behalf.

```bash
if command -v phab-feedback >/dev/null 2>&1; then
  PHAB_FEEDBACK=(phab-feedback)
elif command -v uvx >/dev/null 2>&1; then
  PHAB_FEEDBACK=(uvx phab-feedback)
else
  printf '%s\n' 'phab-feedback requires phab-feedback or uvx on PATH' >&2
  exit 1
fi
```

Run every example below through `"${PHAB_FEEDBACK[@]}"`.

## Inspect before acting

Use `list` to discover work and `show` to assess a revision:

```bash
"${PHAB_FEEDBACK[@]}" list --role reviewing
"${PHAB_FEEDBACK[@]}" show D123
```

Run `threads D123 --state all` for grouped conversations or `timeline D123` for
the complete chronology before classifying feedback or choosing a mutation.
Take comment IDs only from their `id` fields. Never infer them from ordering,
URLs, transaction IDs, or diff IDs. Treat `orphan_replies` as ungrouped; do not
guess their parent.

## Require approval per mutation

Immediately before each mutation, obtain approval for the exact revision,
comment IDs, message text, action, and whether it drafts or publishes. Treat
draft creation and submission as separate mutations requiring separate
approval. Prefer message files or stdin:

```bash
"${PHAB_FEEDBACK[@]}" comment D123 --message-file reply.txt
"${PHAB_FEEDBACK[@]}" reply-inline D123 456 --message-file - < reply.txt
```

- Treat `comment` as an immediate top-level post.
- Treat `remove-comment` as an immediate removal after type validation.
- Treat `reply-inline` and `mark-done` as draft creation.
- Run `submit D123` only after separate approval to publish all pending drafts.
- Use `reply-inline ... --submit` only when combined creation and publication
  were explicitly approved.
- Use `remove-comment` only for an accidental top-level comment.

Never combine reply, Done, removal, or submission actions implicitly.

## Isolate Mozilla-only actions

Treat `mark-helpful`, `mark-unhelpful`, and `request-ai-review` as Mozilla
Review Helper commands. Ratings and AI review requests take effect immediately.
Request AI review only after the relevant changes are published and the user
selected that reviewer.

## Verify published replies

After submission, run `timeline` and confirm each reply's
`reply_to_comment_id` matches the approved parent. Do not mark the parent Done
without separate approval.
