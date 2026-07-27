from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any, Mapping
from urllib.parse import parse_qs

from phab_feedback.transport import HttpResponse


@dataclass
class RecordedRequest:
    method: str
    url: str
    headers: Mapping[str, str]
    data: bytes | None

    def form(self) -> dict[str, list[str]]:
        return parse_qs((self.data or b"").decode())


@dataclass
class FakeTransport:
    responses: list[HttpResponse | Exception]
    requests: list[RecordedRequest] = field(default_factory=list)

    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        data: bytes | None = None,
    ) -> HttpResponse:
        self.requests.append(RecordedRequest(method, url, headers or {}, data))
        if not self.responses:
            raise AssertionError(f"Unexpected request: {method} {url}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def response(payload: Any, status: int = 200) -> HttpResponse:
    body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
    return HttpResponse(status=status, body=body, headers={})


def conduit_result(result: Any) -> HttpResponse:
    return response({"result": result, "error_code": None, "error_info": None})


CSRF_RESPONSE = response(b'<input name="__csrf__" value="B@csrf123">')


def transaction(
    transaction_id: int,
    kind: str,
    comment: int,
    *,
    removed: bool = False,
    fields: dict[str, Any] | None = None,
) -> dict[str, Any]:
    return {
        "id": transaction_id,
        "phid": f"PHID-XACT-{transaction_id}",
        "type": kind,
        "fields": fields or {},
        "comments": [
            {
                "id": comment,
                "phid": f"PHID-CMT-{comment}",
                "version": 1,
                "removed": removed,
                "dateCreated": 100 + comment,
                "content": {"raw": f"comment {comment}"},
            }
        ],
    }


def revision(
    revision_id: int,
    *,
    diff_phid: str = "PHID-DIFF-current",
    author_phid: str = "PHID-USER-author",
    repository_phid: str = "PHID-REPO-main",
) -> dict[str, Any]:
    return {
        "id": revision_id,
        "phid": f"PHID-DREV-{revision_id}",
        "fields": {
            "title": f"Revision {revision_id}",
            "uri": f"https://phab.example/D{revision_id}",
            "authorPHID": author_phid,
            "repositoryPHID": repository_phid,
            "diffPHID": diff_phid,
            "status": {"value": "needs-review", "name": "Needs Review"},
            "isDraft": False,
            "dateCreated": 100,
            "dateModified": 200,
        },
        "attachments": {
            "reviewers": {
                "reviewers": [
                    {
                        "reviewerPHID": "PHID-USER-reviewer",
                        "actorPHID": "PHID-USER-reviewer",
                        "status": "accepted",
                        "isBlocking": True,
                    }
                ]
            }
        },
    }
