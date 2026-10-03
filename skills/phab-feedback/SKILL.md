---
name: phab-feedback
description: Discover, inspect, verify, and act on Phabricator or Phorge Differential feedback with the phab-feedback CLI. Use for reviewer or author revision queues, revision summaries, unresolved inline threads, chronological timelines, exact general or inline comment IDs, inline-thread reply drafts, batch reply and Done manifests, accidental top-level comment removal, explicit draft submission, reply-link and visible Done verification, and Mozilla Review Helper ratings or AI review requests. Trigger when an agent needs deterministic review metadata, must classify feedback across diff versions, needs to triage review work, or is ready to perform a user-approved feedback mutation.
---

# Phabricator feedback

Before doing any phab-feedback work, run `phab-feedback skill show` and read
the complete workflow guide. It ships with the CLI so its instructions match
the version you will run. Follow its inspection, approval, draft, publication,
recovery, and verification safeguards.

If `phab-feedback` is not on PATH but Go is available, read the guide with
`go run github.com/loganrosen/phab-feedback/cmd/phab-feedback@latest skill show`
and use that same runner for subsequent commands. If neither runner is
available, stop with a clear installation error; do not install tools on the
user's behalf. If `skill show` is unavailable, report that the CLI needs an
update instead of proceeding with guessed workflow instructions.
