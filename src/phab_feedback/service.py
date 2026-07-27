"""Feedback timeline and mutation workflows."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Iterable

from .api import ConduitClient, WebClient
from .errors import APIError, NetworkError, ValidationError


_FALLBACK_STATUS_CONSTRAINTS = {
    "open": [
        "needs-review",
        "needs-revision",
        "changes-planned",
        "accepted",
        "draft",
    ],
    "closed": ["published", "abandoned"],
}


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
        result = conduit.paginate(
            "transaction.search",
            {"objectIdentifier": identifier},
        )
        return result["data"]

    def timeline(self, revision: str) -> dict[str, Any]:
        revision_id, _, current_diff = self._revision_context(revision)
        current_diff_phid = current_diff["phid"]
        transactions = self.revision_transactions(revision)
        return self._build_timeline(
            revision_id,
            current_diff,
            transactions,
        )

    def list_revisions(
        self,
        *,
        role: str = "responsible",
        status: str = "open",
        modified_after: int | None = None,
        limit: int = 25,
        after: str | None = None,
    ) -> dict[str, Any]:
        role_constraints = {
            "responsible": "responsiblePHIDs",
            "authored": "authorPHIDs",
            "reviewing": "reviewerPHIDs",
        }
        constraint = role_constraints.get(role)
        if constraint is None:
            raise ValidationError(f"Unsupported revision role: {role}")
        if status not in {"open", "closed", "all"}:
            raise ValidationError(f"Unsupported revision status: {status}")
        if limit < 1:
            raise ValidationError("Revision limit must be positive")

        conduit = self._conduit()
        viewer = conduit.call("user.whoami", {})
        if not isinstance(viewer, dict) or not viewer.get("phid"):
            raise APIError("user.whoami returned invalid user data")

        constraints: dict[str, Any] = {constraint: [viewer["phid"]]}
        if status != "all":
            constraints["statuses"] = [f"{status}()"]
        if modified_after is not None:
            constraints["modifiedStart"] = modified_after

        try:
            search = conduit.search(
                "differential.revision.search",
                constraints,
                attachments={"reviewers": True},
                order="updated",
                after=after,
                limit=limit,
            )
        except NetworkError as error:
            if status == "all" or error.status != 406:
                raise
            constraints["statuses"] = _FALLBACK_STATUS_CONSTRAINTS[status]
            search = conduit.search(
                "differential.revision.search",
                constraints,
                attachments={"reviewers": True},
                order="updated",
                after=after,
                limit=limit,
            )
        handles = self._hydrate_revision_handles(search["data"])
        revisions = [
            self._normalize_revision(item, handles) for item in search["data"]
        ]
        return {
            "viewer": {
                "phid": viewer.get("phid"),
                "username": viewer.get("userName"),
                "name": viewer.get("realName"),
            },
            "role": role,
            "status": status,
            "modified_after": _timestamp(modified_after),
            "count": len(revisions),
            "cursor": search["cursor"],
            "revisions": revisions,
        }

    def show(self, revision: str) -> dict[str, Any]:
        revision_id, raw_revision, current_diff = self._revision_context(
            revision,
            include_reviewers=True,
        )
        transactions = self.revision_transactions(revision)
        timeline = self._build_timeline(
            revision_id,
            current_diff,
            transactions,
        )
        grouped = _group_threads(timeline["inline_comments"])
        handles = self._hydrate_revision_handles([raw_revision])
        inline = timeline["inline_comments"]
        replies = [
            item for item in inline if item["reply_to_comment_phid"] is not None
        ]
        return {
            "revision": self._normalize_revision(raw_revision, handles),
            "current_diff": timeline["current_diff"],
            "feedback": {
                "general_comments": len(timeline["general_comments"]),
                "inline_comments": len(inline),
                "root_threads": len(grouped["threads"]),
                "unresolved_threads": sum(
                    not item["resolved"] for item in grouped["threads"]
                ),
                "resolved_threads": sum(
                    item["resolved"] for item in grouped["threads"]
                ),
                "replies": len(replies),
                "older_diff_comments": sum(
                    not item["on_current_diff"] for item in inline
                ),
                "orphan_replies": len(grouped["orphan_replies"]),
            },
        }

    def threads(
        self,
        revision: str,
        *,
        state: str = "unresolved",
        current_diff_only: bool = False,
    ) -> dict[str, Any]:
        if state not in {"unresolved", "resolved", "all"}:
            raise ValidationError(f"Unsupported thread state: {state}")

        timeline = self.timeline(revision)
        grouped = _group_threads(timeline["inline_comments"])
        threads = grouped["threads"]
        if state == "unresolved":
            threads = [item for item in threads if not item["resolved"]]
        elif state == "resolved":
            threads = [item for item in threads if item["resolved"]]
        if current_diff_only:
            threads = [
                item for item in threads if item["root"]["on_current_diff"]
            ]
            orphan_replies = [
                item
                for item in grouped["orphan_replies"]
                if item["on_current_diff"]
            ]
        else:
            orphan_replies = grouped["orphan_replies"]

        return {
            "revision_id": timeline["revision_id"],
            "current_diff": timeline["current_diff"],
            "state": state,
            "current_diff_only": current_diff_only,
            "count": len(threads),
            "threads": threads,
            "orphan_replies": orphan_replies,
        }

    def _build_timeline(
        self,
        revision_id: int,
        current_diff: dict[str, Any],
        transactions: list[dict[str, Any]],
    ) -> dict[str, Any]:
        current_diff_phid = current_diff["phid"]
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

    def _revision_context(
        self,
        revision: str,
        *,
        include_reviewers: bool = False,
    ) -> tuple[int, dict[str, Any], dict[str, Any]]:
        conduit = self._conduit()
        revision_id = revision_number(revision)
        params: dict[str, Any] = {"constraints": {"ids": [revision_id]}}
        if include_reviewers:
            params["attachments"] = {"reviewers": True}
        result = conduit.call("differential.revision.search", params)
        revisions = _result_data(result, "differential.revision.search")
        if not revisions:
            raise ValidationError(f"D{revision_id} was not found")
        revision_data = revisions[0]
        fields = revision_data.get("fields")
        if not isinstance(fields, dict):
            raise APIError("differential.revision.search returned invalid fields")
        current_diff_phid = fields.get("diffPHID")
        if not current_diff_phid:
            raise APIError(f"D{revision_id} returned no current diff")

        diff_result = conduit.call(
            "differential.diff.search",
            {"constraints": {"phids": [current_diff_phid]}},
        )
        diffs = _result_data(diff_result, "differential.diff.search")
        if not diffs:
            raise APIError(f"Current diff for D{revision_id} was not found")
        current_diff = diffs[0]
        if current_diff.get("phid") is None:
            current_diff = {**current_diff, "phid": current_diff_phid}
        return revision_id, revision_data, current_diff

    def _hydrate_revision_handles(
        self,
        revisions: list[dict[str, Any]],
    ) -> dict[str, dict[str, Any]]:
        phids: set[str] = set()
        for revision in revisions:
            fields = revision.get("fields")
            if not isinstance(fields, dict):
                raise APIError(
                    "differential.revision.search returned invalid fields"
                )
            for key in ("authorPHID", "repositoryPHID"):
                value = fields.get(key)
                if value:
                    phids.add(value)
            for reviewer in _revision_reviewers(revision):
                for key in ("reviewerPHID", "actorPHID"):
                    value = reviewer.get(key)
                    if value:
                        phids.add(value)

        handles: dict[str, dict[str, Any]] = {}
        ordered = sorted(phids)
        for offset in range(0, len(ordered), 100):
            result = self._conduit().call(
                "phid.query",
                {"phids": ordered[offset : offset + 100]},
            )
            if not isinstance(result, dict):
                raise APIError("phid.query returned invalid handle data")
            for phid, handle in result.items():
                if not isinstance(handle, dict):
                    raise APIError("phid.query returned an invalid handle")
                handles[phid] = handle
        return handles

    def _normalize_revision(
        self,
        revision: dict[str, Any],
        handles: dict[str, dict[str, Any]],
    ) -> dict[str, Any]:
        fields = revision["fields"]
        reviewers = _revision_reviewers(revision)
        return {
            "id": revision.get("id"),
            "phid": revision.get("phid"),
            "title": fields.get("title"),
            "uri": fields.get("uri"),
            "status": _revision_status(fields.get("status")),
            "is_draft": fields.get("isDraft"),
            "author": _handle(fields.get("authorPHID"), handles),
            "repository": _handle(fields.get("repositoryPHID"), handles),
            "reviewers": [
                {
                    **(_handle(item.get("reviewerPHID"), handles) or {}),
                    "reviewer_phid": item.get("reviewerPHID"),
                    "status": item.get("status"),
                    "is_blocking": item.get("isBlocking"),
                    "actor": _handle(item.get("actorPHID"), handles),
                }
                for item in reviewers
            ],
            "created": _timestamp(fields.get("dateCreated")),
            "modified": _timestamp(fields.get("dateModified")),
            "current_diff_phid": fields.get("diffPHID"),
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


def _result_data(result: Any, method: str) -> list[dict[str, Any]]:
    if not isinstance(result, dict):
        raise APIError(f"{method} returned invalid data")
    data = result.get("data")
    if not isinstance(data, list):
        raise APIError(f"{method} returned invalid result data")
    if not all(isinstance(item, dict) for item in data):
        raise APIError(f"{method} returned an invalid result item")
    return data


def _handle(
    phid: str | None,
    handles: dict[str, dict[str, Any]],
) -> dict[str, Any] | None:
    if not phid:
        return None
    handle = handles.get(phid, {})
    return {
        "phid": phid,
        "name": handle.get("name"),
        "full_name": handle.get("fullName"),
        "type": handle.get("type"),
        "type_name": handle.get("typeName"),
        "status": handle.get("status"),
        "uri": handle.get("uri"),
    }


def _revision_reviewers(revision: dict[str, Any]) -> list[dict[str, Any]]:
    attachments = revision.get("attachments")
    if attachments is None:
        return []
    if not isinstance(attachments, dict):
        raise APIError("differential.revision.search returned invalid attachments")
    attachment = attachments.get("reviewers")
    if attachment is None:
        return []
    if not isinstance(attachment, dict):
        raise APIError(
            "differential.revision.search returned invalid reviewer attachments"
        )
    reviewers = attachment.get("reviewers")
    if reviewers is None:
        return []
    if not isinstance(reviewers, list) or not all(
        isinstance(item, dict) for item in reviewers
    ):
        raise APIError("differential.revision.search returned invalid reviewers")
    return reviewers


def _revision_status(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        return {
            "value": value.get("value"),
            "name": value.get("name"),
            "color": value.get("color"),
        }
    return {
        "value": value,
        "name": value,
        "color": None,
    }


def _group_threads(inline: list[dict[str, Any]]) -> dict[str, Any]:
    by_phid = {
        item["phid"]: item for item in inline if item.get("phid") is not None
    }
    roots = {
        item["phid"]: item
        for item in inline
        if item.get("phid") is not None
        and item.get("reply_to_comment_phid") is None
    }
    replies_by_root: dict[str, list[dict[str, Any]]] = {
        phid: [] for phid in roots
    }
    orphan_replies: list[dict[str, Any]] = []

    for item in inline:
        parent_phid = item.get("reply_to_comment_phid")
        if parent_phid is None:
            continue
        seen = {item.get("phid")}
        ancestor = by_phid.get(parent_phid)
        while ancestor is not None and ancestor.get("reply_to_comment_phid"):
            ancestor_phid = ancestor.get("phid")
            if ancestor_phid in seen:
                ancestor = None
                break
            seen.add(ancestor_phid)
            ancestor = by_phid.get(ancestor["reply_to_comment_phid"])
        if ancestor is None or ancestor.get("phid") not in roots:
            orphan_replies.append(
                {
                    **item,
                    "orphan_reason": "missing-or-cyclic-parent",
                }
            )
            continue
        replies_by_root[ancestor["phid"]].append(item)

    threads: list[dict[str, Any]] = []
    for root in roots.values():
        replies = sorted(
            replies_by_root[root["phid"]],
            key=lambda item: item.get("created") or "",
        )
        threads.append(
            {
                "root": root,
                "replies": replies,
                "resolved": bool(root.get("is_done")),
                "on_current_diff": bool(root.get("on_current_diff")),
            }
        )
    threads.sort(key=lambda item: item["root"].get("created") or "")
    orphan_replies.sort(key=lambda item: item.get("created") or "")
    return {
        "threads": threads,
        "orphan_replies": orphan_replies,
    }


def _timestamp(value: Any) -> str | None:
    if value is None:
        return None
    return datetime.fromtimestamp(int(value), timezone.utc).isoformat()
