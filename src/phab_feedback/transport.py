"""Minimal HTTP transport with a mockable boundary."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen

from .errors import NetworkError


@dataclass(frozen=True)
class HttpResponse:
    status: int
    body: bytes
    headers: Mapping[str, str]


class Transport(Protocol):
    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        data: bytes | None = None,
    ) -> HttpResponse: ...


class UrllibTransport:
    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        data: bytes | None = None,
    ) -> HttpResponse:
        request = Request(url, data=data, method=method, headers=dict(headers or {}))
        try:
            with urlopen(request) as response:
                return HttpResponse(
                    status=response.status,
                    body=response.read(),
                    headers=dict(response.headers.items()),
                )
        except HTTPError as error:
            raise NetworkError(
                f"HTTP {error.code} from {_safe_location(url)}"
            ) from error
        except URLError as error:
            reason = getattr(error, "reason", "connection failed")
            raise NetworkError(
                f"Request to {_safe_location(url)} failed: {reason}"
            ) from error


def _safe_location(url: str) -> str:
    parsed = urlsplit(url)
    return f"{parsed.scheme}://{parsed.netloc}{parsed.path}"
