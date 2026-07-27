import io
import json
import tempfile
import unittest
from pathlib import Path

from phab_feedback.cli import build_parser, read_message, run
from phab_feedback.config import Credentials

from .helpers import FakeTransport, conduit_result


class StubResolver:
    def resolve(self, **kwargs: object) -> Credentials:
        return Credentials(
            host="https://phab.example",
            conduit_token="token",
            cookie_header="phsid=cookie",
        )


class CliTests(unittest.TestCase):
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
