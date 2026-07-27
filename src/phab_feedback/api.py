"""Conduit and internal web API clients."""

from __future__ import annotations

import html
import json
import re
from typing import Any, Mapping
from urllib.parse import urlencode

from .errors import APIError
from .transport import Transport


class ConduitClient:
    def __init__(self, host: str, token: str, transport: Transport) -> None:
        self.host = host
        self._token = token
        self._transport = transport

    def call(self, method: str, params: Mapping[str, Any]) -> Any:
        conduit_params = dict(params)
        conduit_params["__conduit__"] = {"token": self._token}
        data = urlencode(
            {
                "params": json.dumps(conduit_params, separators=(",", ":")),
                "output": "json",
                "__conduit__": "1",
            }
        ).encode()
        response = self._transport.request(
            "POST",
            f"{self.host}/api/{method}",
            headers={"Content-Type": "application/x-www-form-urlencoded"},
            data=data,
        )
        payload = _json_object(response.body, f"Conduit method {method}")
        if payload.get("error_code"):
            code = payload["error_code"]
            info = str(payload.get("error_info") or "request rejected")
            info = info.replace(self._token, "[redacted]")
            raise APIError(f"Conduit {method} failed: {code}: {info}")
        if "result" not in payload:
            raise APIError(f"Conduit {method} returned no result")
        return payload["result"]


class WebClient:
    def __init__(self, host: str, cookie_header: str, transport: Transport) -> None:
        self.host = host
        self._cookie_header = cookie_header
        self._transport = transport
        self._csrf: str | None = None

    @property
    def csrf(self) -> str:
        if self._csrf is None:
            response = self._transport.request(
                "GET",
                self.host,
                headers={"Cookie": self._cookie_header},
            )
            decoded = html.unescape(
                response.body.decode("utf-8", errors="replace")
            )
            patterns = (
                r'name="__csrf__"\s+value="(B@[A-Za-z0-9]+)"',
                r'"current":"(B@[A-Za-z0-9]+)"',
                r'"token":"(B@[A-Za-z0-9]+)"',
            )
            for pattern in patterns:
                match = re.search(pattern, decoded)
                if match:
                    self._csrf = match.group(1)
                    break
            if self._csrf is None:
                raise APIError("Could not extract a CSRF token from the host")
        return self._csrf

    def post(self, path: str, data: Mapping[str, Any]) -> dict[str, Any]:
        csrf = self.csrf
        response = self._transport.request(
            "POST",
            f"{self.host}{path}",
            headers={
                "Cookie": self._cookie_header,
                "X-Phabricator-Csrf": csrf,
                "Content-Type": "application/x-www-form-urlencoded",
            },
            data=urlencode(data).encode(),
        )
        body = re.sub(rb"^for \(;;\);", b"", response.body)
        payload = _json_object(body, f"Web endpoint {path}")
        error = payload.get("error")
        if error:
            raise APIError(f"Web endpoint {path} failed: {error}")
        return payload


def _json_object(body: bytes, operation: str) -> dict[str, Any]:
    try:
        payload = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise APIError(f"{operation} returned an invalid JSON response") from error
    if not isinstance(payload, dict):
        raise APIError(f"{operation} returned an unexpected response")
    return payload
