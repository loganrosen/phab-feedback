---
name: phab-feedback
description: Discover, inspect, and act on Phabricator or Phorge Differential feedback with the phab-feedback CLI. Use for reviewer or author revision queues, revision summaries, unresolved inline threads, chronological timelines, exact general or inline comment IDs, inline-thread reply drafts, accidental top-level comment removal, Done drafts, explicit draft submission, and Mozilla Review Helper ratings or AI review requests. Trigger when an agent needs deterministic review metadata, must classify feedback across diff versions, needs to triage review work, or is ready to perform a user-approved feedback mutation.
---

# Phabricator feedback

Use the CLI for all deterministic operations. Do not reimplement its HTTP
requests or expose tokens and cookies in commands or output.

## Select a runner

Use an installed `phab-feedback` first. Otherwise use `go run` without
installing an executable. Stop with a clear installation error if neither
runner exists; do not install tools on the user's behalf.

```bash
if command -v phab-feedback >/dev/null 2>&1; then
  PHAB_FEEDBACK=(phab-feedback)
elif command -v go >/dev/null 2>&1; then
  PHAB_FEEDBACK=(go run github.com/loganrosen/phab-feedback/cmd/phab-feedback@latest)
else
  printf '%s\n' 'phab-feedback requires phab-feedback or go on PATH' >&2
  exit 1
fi
```

Run every example below through `"${PHAB_FEEDBACK[@]}"`.

## Inspect before acting

Use `list` to discover work and the revision-first overview to assess it:

```bash
"${PHAB_FEEDBACK[@]}" list --role reviewing --format json
"${PHAB_FEEDBACK[@]}" D123 --format json
```

Run `D123 --threads=all --format json` for grouped conversations or
`D123 --timeline --format json` for the complete chronology before classifying
feedback or choosing a mutation. The CLI defaults to human-readable text, so
agents should request JSON whenever they need fields or IDs. Take comment IDs
only from their `id` fields. Never infer them from ordering, URLs, transaction
IDs, or diff IDs. Treat `orphan_replies` as ungrouped; do not guess their parent.

## Require approval per mutation

Immediately before each mutation, obtain approval for the exact revision,
comment IDs, message text, action, and whether it drafts or publishes. Treat
draft creation and submission as separate mutations requiring separate
approval. Prefer message files or stdin:

```bash
"${PHAB_FEEDBACK[@]}" D123 comment --message-file reply.txt
"${PHAB_FEEDBACK[@]}" D123 reply 456 --message-file - < reply.txt
```

- Treat `comment` as an immediate top-level post.
- Treat `remove-comment` as an immediate removal after type validation.
- Treat `reply` and `done` as draft creation.
- Run `D123 submit` only after separate approval to publish all pending drafts.
- Use `D123 reply ... --submit` only when combined creation and publication
  were explicitly approved.
- Use `remove-comment` only for an accidental top-level comment.

Never combine reply, Done, removal, or submission actions implicitly.

## Isolate Mozilla-only actions

Treat `rate --helpful`, `rate --unhelpful`, and `ai-review` as Mozilla Review
Helper actions. Ratings and AI review requests take effect immediately. Request
AI review only after the relevant changes are published and the user selected
that reviewer.

## Verify published replies

After submission, run `D123 --timeline` and confirm each reply's
`reply_to_comment_id` matches the approved parent. Do not mark the parent Done
without separate approval.
