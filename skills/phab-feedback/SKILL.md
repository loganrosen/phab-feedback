---
name: phab-feedback
description: Inspect Phabricator or Phorge feedback and perform approved review actions with the phab-feedback CLI, including feedback chronology, inline-thread replies, accidental top-level comment removal, Done drafts, draft submission, and Mozilla Review Helper ratings or AI review requests. Use when an agent needs deterministic review metadata or a specific public feedback mutation after the user has selected and approved it.
---

# Phabricator feedback

Use the installed `phab-feedback` command for deterministic operations. Do not
reimplement its HTTP requests or expose tokens and cookies in commands or output.

## Inspect feedback

Run `phab-feedback timeline D123` before classifying comments across multiple
diff versions or when IDs, Done states, reply parents, paths, or chronology
matter. Treat this command as read-only.

## Respect the mutation boundary

Obtain explicit approval for the exact revision, comment IDs, message text, and
action immediately before every mutation. Prefer `--message-file` or stdin over
shell-quoted message text.

- Post a top-level comment with `phab-feedback comment`.
- Draft a true inline-thread reply with `phab-feedback reply-inline`.
- Remove only accidental top-level comments with
  `phab-feedback remove-comment`; the CLI rejects inline comments and verifies
  removal.
- Draft Done changes with `phab-feedback mark-done`.
- Publish pending replies and Done changes with `phab-feedback submit`.

Inline replies and Done changes remain drafts until submission. Use
`reply-inline --submit` only when the user explicitly approved immediate
publication. Never combine reply, Done, rating, or submission actions
implicitly.

## Handle Mozilla Review Helper separately

Treat `mark-helpful`, `mark-unhelpful`, and `request-ai-review` as Mozilla-only
extension commands. Ratings take effect immediately. Request AI review only
after relevant changes are published and only when the workflow selected that
reviewer.

## Verify thread actions

After publishing an inline reply, run `timeline` and confirm its
`reply_to_comment_id` matches the intended parent. Do not mark that parent Done
unless the user separately approved it.
