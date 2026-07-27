"""Command-line interface."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Sequence, TextIO

from .api import ConduitClient, WebClient
from .config import ConfigResolver, CredentialResolver
from .errors import PhabFeedbackError, ValidationError
from .formatting import render_text
from .service import FeedbackService
from .transport import Transport, UrllibTransport


CONDUIT_COMMANDS = {
    "list",
    "show",
    "threads",
    "timeline",
    "comment",
    "reply-inline",
    "remove-comment",
    "mark-done",
    "mark-helpful",
    "mark-unhelpful",
}
WEB_COMMANDS = {
    "reply-inline",
    "remove-comment",
    "mark-done",
    "mark-helpful",
    "mark-unhelpful",
    "submit",
    "request-ai-review",
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="phab-feedback",
        description="Manage Phabricator and Phorge review feedback",
    )
    parser.add_argument("--host", help="Phabricator/Phorge base URL")
    parser.add_argument(
        "--config",
        type=Path,
        help="Path to config JSON (default: XDG config directory)",
    )
    parser.add_argument(
        "--firefox-cookies",
        action="store_true",
        help="Read the web session from a local Firefox profile",
    )
    parser.add_argument(
        "--firefox-profile",
        type=Path,
        help="Firefox profile directory (implies --firefox-cookies)",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    revisions = subparsers.add_parser(
        "list", help="List revisions for the authenticated user"
    )
    revisions.add_argument(
        "--role",
        choices=("responsible", "authored", "reviewing"),
        default="responsible",
        help="Relationship to listed revisions (default: responsible)",
    )
    revisions.add_argument(
        "--status",
        choices=("open", "closed", "all"),
        default="open",
        help="Revision status filter (default: open)",
    )
    revisions.add_argument(
        "--modified-after",
        type=_parse_time,
        metavar="TIME",
        help="Only revisions updated after an ISO 8601 time or Unix timestamp",
    )
    revisions.add_argument(
        "--limit",
        type=_positive_int,
        default=25,
        help="Maximum revisions to return (default: 25)",
    )
    revisions.add_argument(
        "--after",
        help="Continue from a cursor returned by an earlier list command",
    )
    _add_format_option(revisions)

    show = subparsers.add_parser(
        "show", help="Show revision metadata and feedback counts"
    )
    _add_revision_argument(show)
    _add_format_option(show)

    threads = subparsers.add_parser(
        "threads", help="Show inline feedback grouped into threads"
    )
    _add_revision_argument(threads)
    threads.add_argument(
        "--state",
        choices=("unresolved", "resolved", "all"),
        default="unresolved",
        help="Thread state filter (default: unresolved)",
    )
    threads.add_argument(
        "--current-diff-only",
        action="store_true",
        help="Only include threads rooted on the current diff",
    )
    _add_format_option(threads)

    timeline = subparsers.add_parser(
        "timeline", help="Show structured general and inline feedback"
    )
    _add_revision_argument(timeline)
    _add_format_option(timeline)

    comment = subparsers.add_parser(
        "comment", help="Post an immediate top-level revision comment"
    )
    _add_revision_argument(comment)
    _add_message_options(comment)

    reply = subparsers.add_parser(
        "reply-inline", help="Draft a true reply to an inline comment"
    )
    _add_revision_argument(reply)
    _add_comment_argument(reply)
    _add_message_options(reply)
    reply.add_argument(
        "--submit",
        action="store_true",
        help="Explicitly publish the new reply draft immediately",
    )

    remove = subparsers.add_parser(
        "remove-comment", help="Remove an accidental top-level comment"
    )
    _add_revision_argument(remove)
    _add_comment_argument(remove)

    done = subparsers.add_parser(
        "mark-done", help="Mark inline comments Done as drafts"
    )
    _add_revision_argument(done)
    _add_comment_argument(done, multiple=True)

    submit = subparsers.add_parser(
        "submit", help="Submit pending draft actions and comments"
    )
    _add_revision_argument(submit)

    helpful = subparsers.add_parser(
        "mark-helpful",
        help="Rate Review Helper feedback helpful (Mozilla only)",
    )
    _add_revision_argument(helpful)
    _add_comment_argument(helpful, multiple=True)

    unhelpful = subparsers.add_parser(
        "mark-unhelpful",
        help="Rate Review Helper feedback unhelpful (Mozilla only)",
    )
    _add_revision_argument(unhelpful)
    _add_comment_argument(unhelpful, multiple=True)

    request = subparsers.add_parser(
        "request-ai-review",
        help="Request a Review Helper AI review (Mozilla only)",
    )
    _add_revision_argument(request)

    return parser


def _add_message_options(parser: argparse.ArgumentParser) -> None:
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--message", help="Message text")
    group.add_argument(
        "--message-file",
        type=Path,
        help="Read message from a file, or use - for stdin",
    )


def _add_format_option(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--format",
        choices=("json", "text"),
        default="json",
        help="Output format (default: json)",
    )


def _positive_int(value: str) -> int:
    try:
        number = int(value)
    except ValueError as error:
        raise argparse.ArgumentTypeError("must be an integer") from error
    if number < 1:
        raise argparse.ArgumentTypeError("must be positive")
    return number


def _parse_time(value: str) -> int:
    try:
        return int(value)
    except ValueError:
        pass
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise argparse.ArgumentTypeError(
            "must be an ISO 8601 time or Unix timestamp"
        ) from error
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return int(parsed.timestamp())


def _add_revision_argument(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "revision",
        metavar="REVISION",
        help="Revision ID, for example D123",
    )


def _add_comment_argument(
    parser: argparse.ArgumentParser, *, multiple: bool = False
) -> None:
    parser.add_argument(
        "comment_ids" if multiple else "comment_id",
        nargs="+" if multiple else None,
        metavar="COMMENT_ID",
        help="Comment ID from timeline output",
    )


def read_message(args: argparse.Namespace, stdin: TextIO) -> str:
    if args.message is not None:
        message = args.message
    elif args.message_file is not None:
        if str(args.message_file) == "-":
            message = stdin.read()
        else:
            try:
                message = args.message_file.read_text(encoding="utf-8")
            except OSError as error:
                raise ValidationError(
                    f"Could not read message file: {args.message_file}"
                ) from error
    elif not stdin.isatty():
        message = stdin.read()
    else:
        raise ValidationError(
            "Provide --message, --message-file, or redirected stdin"
        )
    if not message.strip():
        raise ValidationError("Message must not be empty")
    return message


def run(
    argv: Sequence[str] | None = None,
    *,
    stdin: TextIO = sys.stdin,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    resolver: CredentialResolver | None = None,
    transport: Transport | None = None,
) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    command = args.command
    resolver = resolver or ConfigResolver()
    transport = transport or UrllibTransport()
    try:
        credentials = resolver.resolve(
            cli_host=args.host,
            config_path=args.config,
            require_token=command in CONDUIT_COMMANDS,
            require_cookie=command in WEB_COMMANDS,
            firefox_cookies=args.firefox_cookies
            or args.firefox_profile is not None,
            firefox_profile=args.firefox_profile,
        )
        conduit = (
            ConduitClient(
                credentials.host,
                credentials.conduit_token or "",
                transport,
            )
            if command in CONDUIT_COMMANDS
            else None
        )
        web = (
            WebClient(
                credentials.host,
                credentials.cookie_header or "",
                transport,
            )
            if command in WEB_COMMANDS
            else None
        )
        service = FeedbackService(conduit=conduit, web=web)

        if command == "list":
            result = service.list_revisions(
                role=args.role,
                status=args.status,
                modified_after=args.modified_after,
                limit=args.limit,
                after=args.after,
            )
        elif command == "show":
            result = service.show(args.revision)
        elif command == "threads":
            result = service.threads(
                args.revision,
                state=args.state,
                current_diff_only=args.current_diff_only,
            )
        elif command == "timeline":
            result = service.timeline(args.revision)
        elif command == "comment":
            result = service.post_comment(
                args.revision, read_message(args, stdin)
            )
        elif command == "reply-inline":
            result = service.draft_inline_reply(
                args.revision, args.comment_id, read_message(args, stdin)
            )
            if args.submit:
                result["submission"] = service.submit(args.revision)
        elif command == "remove-comment":
            result = service.remove_comment(args.revision, args.comment_id)
        elif command == "mark-done":
            result = service.mark_done(args.revision, args.comment_ids)
        elif command == "submit":
            result = service.submit(args.revision)
        elif command == "mark-helpful":
            result = service.rate(
                args.revision, args.comment_ids, helpful=True
            )
        elif command == "mark-unhelpful":
            result = service.rate(
                args.revision, args.comment_ids, helpful=False
            )
        elif command == "request-ai-review":
            result = service.request_ai_review(args.revision)
        else:
            raise AssertionError(f"Unhandled command: {command}")
    except PhabFeedbackError as error:
        print(f"error: {error}", file=stderr)
        return 1
    if getattr(args, "format", "json") == "text":
        stdout.write(render_text(command, result))
        stdout.write("\n")
    else:
        json.dump(result, stdout, indent=2, sort_keys=True)
        stdout.write("\n")
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    return run(argv)
