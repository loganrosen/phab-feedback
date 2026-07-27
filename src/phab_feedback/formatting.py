"""Compact interactive output for read-only commands."""

from __future__ import annotations

from typing import Any


def render_text(command: str, result: dict[str, Any]) -> str:
    if command == "list":
        return _revision_list(result)
    if command == "show":
        return _revision_summary(result)
    if command == "threads":
        return _threads(result)
    if command == "timeline":
        return _timeline(result)
    raise ValueError(f"Text output is not supported for {command}")


def _revision_list(result: dict[str, Any]) -> str:
    lines = [
        f"{result['count']} revisions ({result['role']}, {result['status']})"
    ]
    for revision in result["revisions"]:
        author = _name(revision.get("author"))
        reviewers = ", ".join(
            f"{_name(item)} [{_safe(item.get('status') or 'unknown')}]"
            for item in revision["reviewers"]
        )
        lines.append(
            f"D{revision['id']} [{_status(revision.get('status'))}] "
            f"{_safe(revision.get('title') or '(untitled)')}"
        )
        lines.append(f"  author: {author}; reviewers: {reviewers or 'none'}")
        if revision.get("modified"):
            lines.append(f"  updated: {_safe(revision['modified'])}")
        if revision.get("uri"):
            lines.append(f"  {_safe(revision['uri'])}")
    cursor = result.get("cursor") or {}
    if cursor.get("after"):
        lines.append(f"next cursor: {_safe(cursor['after'])}")
    return "\n".join(lines)


def _revision_summary(result: dict[str, Any]) -> str:
    revision = result["revision"]
    feedback = result["feedback"]
    reviewers = ", ".join(
        f"{_name(item)} [{_safe(item.get('status') or 'unknown')}]"
        for item in revision["reviewers"]
    )
    lines = [
        f"D{revision['id']} [{_status(revision.get('status'))}] "
        f"{_safe(revision.get('title') or '(untitled)')}",
        f"author: {_name(revision.get('author'))}",
        f"reviewers: {reviewers or 'none'}",
        (
            f"feedback: {feedback['unresolved_threads']} unresolved, "
            f"{feedback['resolved_threads']} resolved, "
            f"{feedback['replies']} replies, "
            f"{feedback['general_comments']} general comments"
        ),
        f"older diff comments: {feedback['older_diff_comments']}",
    ]
    if revision.get("uri"):
        lines.append(_safe(revision["uri"]))
    return "\n".join(lines)


def _threads(result: dict[str, Any]) -> str:
    lines = [
        f"D{result['revision_id']}: {result['count']} {result['state']} threads"
    ]
    for thread in result["threads"]:
        root = thread["root"]
        state = "resolved" if thread["resolved"] else "unresolved"
        location = _location(root)
        lines.append(f"[{state}] #{root['id']} {location}")
        lines.append(f"  {_safe(root.get('content') or '')}")
        for reply in thread["replies"]:
            lines.append(
                f"  reply #{reply['id']} to #{reply.get('reply_to_comment_id')}: "
                f"{_safe(reply.get('content') or '')}"
            )
    for orphan in result["orphan_replies"]:
        parent = (
            orphan.get("reply_to_comment_id")
            or orphan.get("reply_to_comment_phid")
        )
        lines.append(
            f"[orphan reply] #{orphan['id']} -> {_safe(parent)}"
        )
        lines.append(f"  {_safe(orphan.get('content') or '')}")
    return "\n".join(lines)


def _timeline(result: dict[str, Any]) -> str:
    lines = [f"D{result['revision_id']}: {len(result['events'])} feedback events"]
    for event in result["events"]:
        location = f" {_location(event)}" if event["kind"] == "inline" else ""
        lines.append(
            f"[{event['kind']}] #{event['id']}{location}: "
            f"{_safe(event.get('content') or '')}"
        )
    return "\n".join(lines)


def _status(value: Any) -> str:
    if isinstance(value, dict):
        return _safe(value.get("name") or value.get("value") or "unknown")
    return _safe(value or "unknown")


def _name(handle: dict[str, Any] | None) -> str:
    if not handle:
        return "unknown"
    return _safe(handle.get("full_name") or handle.get("name") or handle["phid"])


def _location(comment: dict[str, Any]) -> str:
    path = _safe(comment.get("path") or "(unknown path)")
    line = comment.get("line")
    return f"{path}:{line}" if line is not None else path


def _safe(value: Any) -> str:
    escaped = []
    for character in str(value):
        codepoint = ord(character)
        if character == "\n":
            escaped.append(r"\n")
        elif character == "\r":
            escaped.append(r"\r")
        elif character == "\t":
            escaped.append(r"\t")
        elif codepoint < 32 or 127 <= codepoint <= 159:
            escaped.append(f"\\x{codepoint:02x}")
        else:
            escaped.append(character)
    return "".join(escaped)
