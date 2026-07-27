import json
import unittest
from urllib.parse import parse_qs

from phab_feedback.api import ConduitClient, WebClient
from phab_feedback.errors import APIError, ValidationError
from phab_feedback.service import FeedbackService, comment_id, revision_number

from .helpers import (
    CSRF_RESPONSE,
    FakeTransport,
    conduit_result,
    response,
    transaction,
)


class ServiceTests(unittest.TestCase):
    host = "https://phab.example"

    def service(self, responses):
        transport = FakeTransport(list(responses))
        return (
            FeedbackService(
                conduit=ConduitClient(self.host, "token", transport),
                web=WebClient(self.host, "phsid=cookie", transport),
            ),
            transport,
        )

    def test_identifier_parsing(self) -> None:
        self.assertEqual(12, revision_number("d12"))
        self.assertEqual(34, comment_id("34"))
        with self.assertRaises(ValidationError):
            revision_number("D0")
        with self.assertRaises(ValidationError):
            comment_id("not-an-id")

    def test_paginated_transactions(self) -> None:
        service, transport = self.service(
            [
                conduit_result(
                    {"data": [{"id": 1}], "cursor": {"after": "next"}}
                ),
                conduit_result(
                    {"data": [{"id": 2}], "cursor": {"after": None}}
                ),
            ]
        )
        self.assertEqual([{"id": 1}, {"id": 2}], service.revision_transactions("D7"))
        second = json.loads(transport.requests[1].form()["params"][0])
        self.assertEqual("next", second["after"])

    def test_timeline_contains_sorted_combined_events_and_reply_parent(self) -> None:
        parent = transaction(
            1,
            "inline",
            10,
            fields={
                "diff": {"id": 4, "phid": "PHID-DIFF-current"},
                "path": "a.py",
                "line": 3,
                "isDone": False,
            },
        )
        reply = transaction(
            2,
            "inline",
            11,
            fields={
                "diff": {"id": 3, "phid": "PHID-DIFF-old"},
                "path": "a.py",
                "line": 3,
                "isDone": True,
                "replyToCommentPHID": "PHID-CMT-10",
            },
        )
        general = transaction(3, "comment", 12)
        service, _ = self.service(
            [
                conduit_result(
                    {"data": [{"fields": {"diffPHID": "PHID-DIFF-current"}}]}
                ),
                conduit_result(
                    {
                        "data": [
                            {
                                "id": 4,
                                "fields": {"dateCreated": 100},
                            }
                        ]
                    }
                ),
                conduit_result(
                    {"data": [reply, parent, general], "cursor": {"after": None}}
                ),
            ]
        )
        result = service.timeline("D9")
        self.assertEqual(["inline", "inline", "general"], [e["kind"] for e in result["events"]])
        self.assertEqual(10, result["inline_comments"][1]["reply_to_comment_id"])
        self.assertFalse(result["inline_comments"][1]["on_current_diff"])

    def test_reply_creation_uses_parent_phid_then_saves(self) -> None:
        service, transport = self.service(
            [
                conduit_result(
                    {
                        "data": [transaction(1, "inline", 44)],
                        "cursor": {"after": None},
                    }
                ),
                CSRF_RESPONSE,
                response({"payload": {"inline": {"id": 55}}}),
                response({"payload": {}}),
            ]
        )
        result = service.draft_inline_reply("D8", "44", "A reply")
        create_form = transport.requests[2].form()
        save_form = transport.requests[3].form()
        self.assertEqual(["PHID-CMT-44"], create_form["replyToCommentPHID"])
        self.assertEqual(["reply"], create_form["op"])
        self.assertEqual(["55"], save_form["id"])
        self.assertEqual(["save"], save_form["op"])
        self.assertEqual(55, result["draft_comment_id"])

    def test_reply_rejects_general_comment_before_web_request(self) -> None:
        service, transport = self.service(
            [
                conduit_result(
                    {
                        "data": [transaction(1, "comment", 44)],
                        "cursor": {"after": None},
                    }
                )
            ]
        )
        with self.assertRaisesRegex(ValidationError, "not inline"):
            service.draft_inline_reply("D8", "44", "A reply")
        self.assertEqual(1, len(transport.requests))

    def test_remove_rejects_inline_and_verifies_general_removal(self) -> None:
        inline_service, _ = self.service(
            [
                conduit_result(
                    {
                        "data": [transaction(1, "inline", 12)],
                        "cursor": {"after": None},
                    }
                )
            ]
        )
        with self.assertRaisesRegex(ValidationError, "not comment"):
            inline_service.remove_comment("D1", 12)

        removed = transaction(2, "comment", 13, removed=True)
        service, _ = self.service(
            [
                conduit_result(
                    {
                        "data": [transaction(2, "comment", 13)],
                        "cursor": {"after": None},
                    }
                ),
                CSRF_RESPONSE,
                response({"payload": {}}),
                conduit_result({"data": [removed], "cursor": {"after": None}}),
            ]
        )
        self.assertTrue(service.remove_comment("D1", 13)["removed"])

    def test_remove_fails_when_server_does_not_confirm(self) -> None:
        active = transaction(2, "comment", 13)
        service, _ = self.service(
            [
                conduit_result({"data": [active], "cursor": {"after": None}}),
                CSRF_RESPONSE,
                response({"payload": {}}),
                conduit_result({"data": [active], "cursor": {"after": None}}),
            ]
        )
        with self.assertRaisesRegex(APIError, "did not confirm"):
            service.remove_comment("D1", 13)

    def test_done_rating_submit_and_request_payloads(self) -> None:
        inline = transaction(1, "inline", 20)
        service, transport = self.service(
            [
                conduit_result({"data": [inline], "cursor": {"after": None}}),
                CSRF_RESPONSE,
                response({"payload": {"isChecked": True, "draftState": True}}),
                conduit_result({"data": [inline], "cursor": {"after": None}}),
                response({"payload": {"message": "Thanks"}}),
                response({"payload": {"redirect": "/D1"}}),
                response({"payload": {"dialog": "successfully requested"}}),
            ]
        )
        done = service.mark_done("D1", [20])
        rated = service.rate("D1", [20], helpful=False)
        submitted = service.submit("D1")
        requested = service.request_ai_review("D1")
        self.assertTrue(done["comments"][0]["draft"])
        self.assertFalse(rated["comments"][0]["helpful"])
        self.assertTrue(submitted["submitted"])
        self.assertEqual("requested", requested["status"])
        self.assertEqual(["done"], transport.requests[2].form()["op"])
        self.assertEqual(["down"], transport.requests[4].form()["feedbackType"])
        self.assertEqual(
            ["[]"], transport.requests[5].form()["editengine.actions"]
        )
        self.assertEqual(["6"], transport.requests[6].form()["__metablock__"])
        self.assertEqual(
            ["/reviewhelper/request/1/"],
            [transport.requests[6].url.removeprefix(self.host)],
        )

    def test_api_errors_do_not_include_token(self) -> None:
        transport = FakeTransport(
            [
                response(
                    {
                        "result": None,
                        "error_code": "ERR-FAIL",
                        "error_info": "very-secret-token was rejected",
                    }
                )
            ]
        )
        client = ConduitClient(self.host, "very-secret-token", transport)
        with self.assertRaises(APIError) as context:
            client.call("transaction.search", {})
        self.assertNotIn("very-secret-token", str(context.exception))
        self.assertIn("[redacted]", str(context.exception))


if __name__ == "__main__":
    unittest.main()
