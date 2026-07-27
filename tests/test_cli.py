import io
import json
import tempfile
import unittest
from pathlib import Path

from phab_feedback.cli import build_parser, read_message, run
from phab_feedback.config import Credentials

from .helpers import FakeTransport, conduit_result, revision


class StubResolver:
    def resolve(self, **kwargs: object) -> Credentials:
        return Credentials(
            host="https://phab.example",
            conduit_token="token",
            cookie_header="phsid=cookie",
        )


class CliTests(unittest.TestCase):
    def test_new_read_command_parsing(self) -> None:
        parser = build_parser()
        listed = parser.parse_args(
            [
                "list",
                "--role",
                "reviewing",
                "--modified-after",
                "2025-01-02T03:04:05Z",
                "--limit",
                "5",
                "--format",
                "text",
            ]
        )
        self.assertEqual("reviewing", listed.role)
        self.assertEqual(5, listed.limit)
        self.assertEqual("text", listed.format)
        self.assertIsInstance(listed.modified_after, int)

        threads = parser.parse_args(
            ["threads", "D1", "--state", "all", "--current-diff-only"]
        )
        self.assertEqual("all", threads.state)
        self.assertTrue(threads.current_diff_only)

    def test_direct_file_and_stdin_messages(self) -> None:
        parser = build_parser()
        direct = parser.parse_args(["comment", "D1", "--message", "hello"])
        self.assertEqual("hello", read_message(direct, io.StringIO()))

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "message.txt"
            path.write_text("from file", encoding="utf-8")
            file_args = parser.parse_args(
                ["comment", "D1", "--message-file", str(path)]
            )
            self.assertEqual("from file", read_message(file_args, io.StringIO()))

        stdin_args = parser.parse_args(["comment", "D1", "--message-file", "-"])
        self.assertEqual("from stdin", read_message(stdin_args, io.StringIO("from stdin")))

    def test_comment_command_posts_message_and_outputs_json(self) -> None:
        transport = FakeTransport([conduit_result({"object": {"id": 1}})])
        stdout = io.StringIO()
        stderr = io.StringIO()
        status = run(
            ["comment", "D1", "--message", "hello"],
            stdin=io.StringIO(),
            stdout=stdout,
            stderr=stderr,
            resolver=StubResolver(),
            transport=transport,
        )
        self.assertEqual(0, status)
        self.assertTrue(json.loads(stdout.getvalue())["posted"])
        self.assertEqual("", stderr.getvalue())
        params = json.loads(transport.requests[0].form()["params"][0])
        self.assertEqual("hello", params["transactions"][0]["value"])

    def test_list_command_supports_compact_text_output(self) -> None:
        transport = FakeTransport(
            [
                conduit_result(
                    {
                        "phid": "PHID-USER-viewer",
                        "userName": "viewer",
                        "realName": "Viewer",
                    }
                ),
                conduit_result(
                    {"data": [revision(17)], "cursor": {"after": None}}
                ),
                conduit_result(
                    {
                        "PHID-USER-author": {"fullName": "Author"},
                        "PHID-USER-reviewer": {"fullName": "Reviewer"},
                        "PHID-REPO-main": {"fullName": "Repository"},
                    }
                ),
            ]
        )
        stdout = io.StringIO()
        status = run(
            ["list", "--role", "reviewing", "--format", "text"],
            stdin=io.StringIO(),
            stdout=stdout,
            stderr=io.StringIO(),
            resolver=StubResolver(),
            transport=transport,
        )
        self.assertEqual(0, status)
        self.assertIn("D17 [Needs Review] Revision 17", stdout.getvalue())
        self.assertIn("Reviewer [accepted]", stdout.getvalue())

    def test_compact_text_output_escapes_terminal_controls(self) -> None:
        item = revision(17)
        item["fields"]["title"] = "unsafe\x1b]52;c;clipboard\x07\nnext"
        transport = FakeTransport(
            [
                conduit_result(
                    {
                        "phid": "PHID-USER-viewer",
                        "userName": "viewer",
                        "realName": "Viewer",
                    }
                ),
                conduit_result({"data": [item], "cursor": {"after": None}}),
                conduit_result(
                    {
                        "PHID-USER-author": {"fullName": "Author"},
                        "PHID-USER-reviewer": {"fullName": "Reviewer"},
                        "PHID-REPO-main": {"fullName": "Repository"},
                    }
                ),
            ]
        )
        stdout = io.StringIO()
        status = run(
            ["list", "--role", "reviewing", "--format", "text"],
            stdin=io.StringIO(),
            stdout=stdout,
            stderr=io.StringIO(),
            resolver=StubResolver(),
            transport=transport,
        )
        output = stdout.getvalue()
        self.assertEqual(0, status)
        self.assertNotIn("\x1b", output)
        self.assertNotIn("\x07", output)
        self.assertIn(r"unsafe\x1b]52;c;clipboard\x07\nnext", output)

    def test_safe_cli_error_does_not_print_credentials(self) -> None:
        transport = FakeTransport(
            [
                conduit_result(
                    {"data": [], "cursor": {"after": None}}
                )
            ]
        )
        stderr = io.StringIO()
        status = run(
            ["remove-comment", "D1", "9"],
            stdin=io.StringIO(),
            stdout=io.StringIO(),
            stderr=stderr,
            resolver=StubResolver(),
            transport=transport,
        )
        self.assertEqual(1, status)
        self.assertNotIn("token", stderr.getvalue())
        self.assertNotIn("cookie", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
