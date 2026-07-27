"""Feedback timeline and mutation workflows."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Iterable

from .api import ConduitClient, WebClient
from .errors import APIError, ValidationError


def revision_number(revision: str) -> int:
    value = revision.strip()
    if value[:1].lower() == "d":
        value = value[1:]
    if not value.isdigit() or int(value) < 1:
        raise ValidationError(f"Invalid revision identifier: {revision}")
    return int(value)


def comment_id(value: str | int) -> int:
    text = str(value)
    if not text.isdigit() or int(text) < 1:
        raise ValidationError(f"Invalid comment ID: {value}")
    return int(text)


def active_comment(transaction: dict[str, Any]) -> dict[str, Any] | None:
    versions = transaction.get("comments") or []
    if not isinstance(versions, list) or not versions:
        return None
    latest = max(versions, key=lambda item: item.get("version", 0))
    return None if latest.get("removed") else latest


class FeedbackService:
    def __init__(
        self,
        *,
        conduit: ConduitClient | None = None,
        web: WebClient | None = None,
    ) -> None:
        self.conduit = conduit
        self.web = web

    def revision_transactions(self, revision: str) -> list[dict[str, Any]]:
        conduit = self._conduit()
        identifier = f"D{revision_number(revision)}"
        transactions: list[dict[str, Any]] = []
        after: str | None = None
        while True:
            params: dict[str, Any] = {
                "objectIdentifier": identifier,
                "limit": 100,
            }
            if after:
                params["after"] = after
            result = conduit.call("transaction.search", params)
            page = result.get("data", [])
            if not isinstance(page, list):
                raise APIError("transaction.search returned invalid data")
            transactions.extend(page)
            cursor = result.get("cursor") or {}
            after = cursor.get("after")
            if not after:
                return transactions

    def timeline(self, revision: str) -> dict[str, Any]:
        conduit = self._conduit()
        revision_id = revision_number(revision)
        revisions = conduit.call(
            "differential.revision.search",
            {"constraints": {"ids": [revision_id]}},
        ).get("data", [])
        if not revisions:
            raise ValidationError(f"D{revision_id} was not found")
        current_diff_phid = revisions[0].get("fields", {}).get("diffPHID")
        if not current_diff_phid:
            raise APIError(f"D{revision_id} returned no current diff")
        diffs = conduit.call(
            "differential.diff.search",
            {"constraints": {"phids": [current_diff_phid]}},
        ).get("data", [])
        if not diffs:
            raise APIError(f"Current diff for D{revision_id} was not found")
        current_diff = diffs[0]
        transactions = self.revision_transactions(revision)
        by_phid = {
            version["phid"]: version["id"]
            for transaction in transactions
            for version in transaction.get("comments", [])
            if version.get("phid")
        }

        general: list[dict[str, Any]] = []
        inline: list[dict[str, Any]] = []
        for transaction in transactions:
            kind = transaction.get("type")
            if kind not in {"comment", "inline"}:
                continue
            comment = active_comment(transaction)
            if comment is None:
                continue
            base = {
                "kind": "general" if kind == "comment" else "inline",
                "id": comment.get("id"),
                "phid": comment.get("phid"),
                "transaction_id": transaction.get("id"),
                "transaction_phid": transaction.get("phid"),
                "created": _timestamp(comment.get("dateCreated")),
                "content": (comment.get("content") or {}).get("raw"),
            }
            if kind == "comment":
                general.append(base)
                continue
            fields = transaction.get("fields") or {}
            diff = fields.get("diff") or {}
            parent_phid = fields.get("replyToCommentPHID")
            inline.append(
                {
                    **base,
                    "diff_id": diff.get("id"),
                    "diff_phid": diff.get("phid"),
                    "on_current_diff": diff.get("phid") == current_diff_phid,
                    "path": fields.get("path"),
                    "line": fields.get("line"),
                    "is_done": fields.get("isDone"),
                    "reply_to_comment_id": by_phid.get(parent_phid),
                    "reply_to_comment_phid": parent_phid,
                }
            )
        general.sort(key=lambda item: item["created"] or "")
        inline.sort(key=lambda item: item["created"] or "")
        events = sorted(
            [*general, *inline], key=lambda item: item["created"] or ""
        )
        return {
            "revision_id": revision_id,
            "current_diff": {
                "id": current_diff.get("id"),
                "phid": current_diff_phid,
                "created": _timestamp(
                    (current_diff.get("fields") or {}).get("dateCreated")
                ),
            },
            "events": events,
            "general_comments": general,
            "inline_comments": inline,
        }

    def post_comment(self, revision: str, message: str) -> dict[str, Any]:
        revision_id = revision_number(revision)
        result = self._conduit().call(
            "differential.revision.edit",
            {
                "objectIdentifier": f"D{revision_id}",
                "transactions": [{"type": "comment", "value": message}],
            },
        )
        return {"revision_id": revision_id, "posted": True, "result": result}

    def reply_inline(
        self, revision: str, parent_id: str | int
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        return self._find_comment(revision, parent_id, "inline")

    def draft_inline_reply(
        self, revision: str, parent_id: str | int, message: str
    ) -> dict[str, Any]:
        revision_id = revision_number(revision)
        _, parent = self.reply_inline(revision, parent_id)
        path = f"/differential/comment/inline/edit/{revision_id}/"
        content = {
            "hasContentState": "1",
            "text": message,
            "suggestionText": "",
            "hasSuggestion": "0",
        }
        common = {"on_right": "1", "renderer": "2up", "__wflow__": "true", "__ajax__": "true"}
        created = self._web().post(
            path,
            {
                **common,
                **content,
                "op": "reply",
                "replyToCommentPHID": parent["phid"],
            },
        )
        reply_id = (
            ((created.get("payload") or {}).get("inline") or {}).get("id")
        )
        if not reply_id:
            raise APIError("Inline reply creation returned no comment ID")
        self._web().post(
            path,
            {**common, **content, "op": "save", "id": str(reply_id)},
        )
        return {
            "revision_id": revision_id,
            "parent_comment_id": comment_id(parent_id),
            "parent_comment_phid": parent["phid"],
            "draft_comment_id": int(reply_id),
            "draft": True,
        }

    def remove_comment(
        self, revision: str, target_id: str | int
    ) -> dict[str, Any]:
        revision_id = revision_number(revision)
        transaction, _ = self._find_comment(revision, target_id, "comment")
        self._web().post(
            f"/transactions/edit/{transaction['phid']}/",
            {"text": "", "__form__": "1", "__ajax__": "true"},
        )
        updated = next(
            (
                item
                for item in self.revision_transactions(revision)
                if item.get("id") == transaction.get("id")
            ),
            None,
        )
        versions = updated.get("comments", []) if updated else []
        latest = max(
            versions,
            key=lambda item: item.get("version", 0),
            default=None,
        )
        if latest is None or not latest.get("removed"):
            raise APIError(
                f"Server did not confirm removal of comment {target_id}"
            )
        return {
            "revision_id": revision_id,
            "comment_id": comment_id(target_id),
            "removed": True,
        }

    def mark_done(
        self, revision: str, comment_ids: Iterable[str | int]
    ) -> dict[str, Any]:
        revision_id = revision_number(revision)
        ids = self._validate_comments(revision, comment_ids, "inline")
        path = f"/differential/comment/inline/edit/{revision_id}/"
        results = []
        for identifier in ids:
            response = self._web().post(
                path,
                {
                    "op": "done",
                    "id": str(identifier),
                    "__wflow__": "true",
                    "__ajax__": "true",
                },
            )
            payload = response.get("payload") or {}
            results.append(
                {
                    "comment_id": identifier,
                    "is_done": bool(payload.get("isChecked")),
                    "draft": bool(payload.get("draftState")),
                }
            )
        return {"revision_id": revision_id, "comments": results}

    def rate(
        self,
        revision: str,
        comment_ids: Iterable[str | int],
        *,
        helpful: bool,
    ) -> dict[str, Any]:
        revision_id = revision_number(revision)
        ids = self._validate_comments(revision, comment_ids, "inline")
        results = []
        for identifier in ids:
            response = self._web().post(
                "/reviewhelper/feedback/",
                {
                    "commentID": str(identifier),
                    "feedbackType": "up" if helpful else "down",
                    "__ajax__": "true",
                },
            )
            payload = response.get("payload") or {}
            results.append(
                {
                    "comment_id": identifier,
                    "helpful": helpful,
                    "message": payload.get("message"),
                }
            )
        return {
            "revision_id": revision_id,
            "mozilla_review_helper": True,
            "comments": results,
        }

    def submit(self, revision: str) -> dict[str, Any]:
        revision_id = revision_number(revision)
        web = self._web()
        response = web.post(
            f"/differential/revision/edit/{revision_id}/comment/",
            {
                "__csrf__": web.csrf,
                "__form__": "1",
                "editengine.actions": "[]",
                "comment": "",
                "comment_metadata": "{}",
                "__ajax__": "true",
            },
        )
        return {
            "revision_id": revision_id,
            "submitted": True,
            "redirect": (response.get("payload") or {}).get("redirect"),
        }

    def request_ai_review(self, revision: str) -> dict[str, Any]:
        revision_id = revision_number(revision)
        response = self._web().post(
            f"/reviewhelper/request/{revision_id}/",
            {"__wflow__": "true", "__ajax__": "true", "__metablock__": "6"},
        )
        dialog = str((response.get("payload") or {}).get("dialog", ""))
        if "successfully" in dialog:
            status = "requested"
        elif "being processed" in dialog:
            status = "already-in-progress"
        else:
            status = "response-received"
        return {
            "revision_id": revision_id,
            "mozilla_review_helper": True,
            "status": status,
        }

    def _validate_comments(
        self,
        revision: str,
        values: Iterable[str | int],
        expected_type: str,
    ) -> list[int]:
        ids = [comment_id(value) for value in values]
        if not ids:
            raise ValidationError("At least one comment ID is required")
        transactions = self.revision_transactions(revision)
        by_id: dict[int, dict[str, Any]] = {}
        for transaction in transactions:
            for version in transaction.get("comments", []):
                raw_id = version.get("id")
                if raw_id is not None:
                    by_id[int(raw_id)] = transaction
        for identifier in ids:
            transaction = by_id.get(identifier)
            if transaction is None:
                raise ValidationError(
                    f"Comment {identifier} was not found on "
                    f"D{revision_number(revision)}"
                )
            actual = transaction.get("type") or "non-comment"
            if actual != expected_type:
                raise ValidationError(
                    f"Comment {identifier} is a {actual} transaction, "
                    f"not {expected_type}"
                )
            if active_comment(transaction) is None:
                raise ValidationError(f"Comment {identifier} has been removed")
        return ids

    def _find_comment(
        self, revision: str, target_id: str | int, expected_type: str
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        identifier = comment_id(target_id)
        for transaction in self.revision_transactions(revision):
            versions = transaction.get("comments", [])
            if not any(int(version.get("id", 0)) == identifier for version in versions):
                continue
            actual = transaction.get("type") or "non-comment"
            if actual != expected_type:
                raise ValidationError(
                    f"Comment {identifier} is a {actual} transaction, "
                    f"not {expected_type}"
                )
            comment = active_comment(transaction)
            if comment is None:
                raise ValidationError(f"Comment {identifier} has been removed")
            return transaction, comment
        raise ValidationError(
            f"Comment {identifier} was not found on D{revision_number(revision)}"
        )

    def _conduit(self) -> ConduitClient:
        if self.conduit is None:
            raise RuntimeError("Conduit client not configured")
        return self.conduit

    def _web(self) -> WebClient:
        if self.web is None:
            raise RuntimeError("Web client not configured")
        return self.web


def _timestamp(value: Any) -> str | None:
    if value is None:
        return None
    return datetime.fromtimestamp(int(value), timezone.utc).isoformat()
