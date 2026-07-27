"""Command-line interface."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Sequence, TextIO

from .api import ConduitClient, WebClient
from .config import ConfigResolver
from .errors import PhabFeedbackError, ValidationError
from .service import FeedbackService
from .transport import Transport, UrllibTransport


CONDUIT_COMMANDS = {
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

    timeline = subparsers.add_parser(
        "timeline", help="Show structured general and inline feedback"
    )
    timeline.add_argument("revision")

    comment = subparsers.add_parser(
        "comment", help="Post an immediate top-level revision comment"
    )
    comment.add_argument("revision")
    _add_message_options(comment)

    reply = subparsers.add_parser(
        "reply-inline", help="Draft a true reply to an inline comment"
    )
    reply.add_argument("revision")
    reply.add_argument("comment_id")
    _add_message_options(reply)
    reply.add_argument(
        "--submit",
        action="store_true",
        help="Explicitly publish the new reply draft immediately",
    )

    remove = subparsers.add_parser(
        "remove-comment", help="Remove an accidental top-level comment"
    )
    remove.add_argument("revision")
    remove.add_argument("comment_id")

    done = subparsers.add_parser(
        "mark-done", help="Mark inline comments Done as drafts"
    )
    done.add_argument("revision")
    done.add_argument("comment_ids", nargs="+")

    submit = subparsers.add_parser(
        "submit", help="Submit pending draft actions and comments"
    )
    submit.add_argument("revision")

    helpful = subparsers.add_parser(
        "mark-helpful",
        help="Rate Review Helper feedback helpful (Mozilla only)",
    )
    helpful.add_argument("revision")
    helpful.add_argument("comment_ids", nargs="+")

    unhelpful = subparsers.add_parser(
        "mark-unhelpful",
        help="Rate Review Helper feedback unhelpful (Mozilla only)",
    )
    unhelpful.add_argument("revision")
    unhelpful.add_argument("comment_ids", nargs="+")

    request = subparsers.add_parser(
        "request-ai-review",
        help="Request a Review Helper AI review (Mozilla only)",
    )
    request.add_argument("revision")

    return parser


def _add_message_options(parser: argparse.ArgumentParser) -> None:
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--message", help="Message text")
    group.add_argument(
        "--message-file",
        type=Path,
        help="Read message from a file, or use - for stdin",
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
    resolver: ConfigResolver | None = None,
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

        if command == "timeline":
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
    json.dump(result, stdout, indent=2, sort_keys=True)
    stdout.write("\n")
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    return run(argv)
