---
name: phab-feedback
description: Discover, inspect, verify, and act on Phabricator or Phorge Differential feedback with the phab-feedback CLI. Use for reviewer or author revision queues, revision summaries, unresolved inline threads, chronological timelines, exact general or inline comment IDs, inline-thread reply drafts, batch reply and Done manifests, accidental top-level comment removal, explicit draft submission, reply-link and visible Done verification, and Mozilla Review Helper ratings or AI review requests. Trigger when an agent needs deterministic review metadata, must classify feedback across diff versions, needs to triage review work, or is ready to perform a user-approved feedback mutation.
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
- Before any submission, warn that Phabricator publishes every eligible pending
  inline draft owned by the current user on that revision, including unrelated
  drafts created earlier in the browser or by another command.
- Do not override Phabricator submission warnings. `Empty Comment` is the
  benign no-drafts outcome; treat every other dialog, including
  `Action(s) With No Effect`, as blocked publication. Surface the bounded
  plain-text dialog to the user. When an inline is still being edited, have the
  user save or close that editor, re-inspect pending drafts, and approve a
  retry.
- Use `D123 reply ... --submit` only when combined creation and publication
  were explicitly approved.
- Use `D123 reply ... --done` only when the reply and Done action were both
  explicitly approved. Add `--submit` only when publication was also approved.
- Use `D123 done ... --submit` only when drafting the Done states and publishing
  all pending drafts were both explicitly approved.
- Use `remove-comment` only for an accidental top-level comment.

Never combine reply, Done, removal, or submission actions implicitly.
If a `done` command is interrupted, inspect the comment and rerun `done` before
any later submission; the upstream toggle may have left a pending undo-Done
draft without returning a result.

## Batch approved actions

For several approved inline actions on one revision, write a JSON manifest with
the exact revision, comment IDs, reply text, and optional `done: true` values:

```json
{
  "revision": "D123",
  "actions": [
    {"comment_id": 456, "reply": "Updated as requested.", "done": true},
    {"comment_id": 457, "done": true}
  ]
}
```

Validate it before acting:

```bash
"${PHAB_FEEDBACK[@]}" batch actions.json --dry-run --format json
```

Run without `--submit` to create drafts only. Add `--submit` only when one final
publication of all pending drafts was explicitly approved. The CLI validates
the complete manifest and all target comments before mutation, creates drafts
in order, submits at most once, and reports unavoidable remote partial failures.
The final submission is revision-wide for the current user, not scoped to the
manifest. If all requested Done states are already published and the batch
creates no new draft, the CLI reports `outcome: "not-attempted"` so it does not
publish unrelated drafts; use the standalone `submit` command only after
separate approval if those existing drafts should be published.
Treat `outcome: "blocked"` as a failed pre-request prerequisite, not a benign
skip; fix the reported credential or CSRF problem before asking to retry.

## Isolate Mozilla-only actions

Treat `rate --helpful`, `rate --unhelpful`, and `ai-review` as Mozilla Review
Helper actions. Ratings and AI review requests take effect immediately. Request
AI review only after the relevant changes are published and the user selected
that reviewer.

## Verify reply linkage and visible Done state

After submission, use `verify` with each expected reply-parent pair and Done
state:

```bash
"${PHAB_FEEDBACK[@]}" D123 verify \
  --reply 901:456 \
  --done 456 \
  --format json
```

The command exits nonzero if a visible reply is missing, the direct parent does
not match, or Conduit reports a requested comment as not Done. It does not
independently prove reply publication. Upstream Conduit also reports
`isDone=true` for both published Done and a pending undo-Done draft, so treat
Done verification status `observed` and the per-comment
`done-or-pending-undo` state as visible-state checks, not definitive
publication proof. Inspect the revision before submission when pending state
matters. Do not mark the parent Done without separate approval.
