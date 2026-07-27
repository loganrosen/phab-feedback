"""Configuration and credential discovery."""

from __future__ import annotations

import configparser
import json
import os
import shutil
import sqlite3
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping
from urllib.parse import urlsplit

from .errors import ConfigurationError


@dataclass(frozen=True)
class Credentials:
    host: str
    conduit_token: str | None = None
    cookie_header: str | None = None


class ConfigResolver:
    def __init__(
        self,
        *,
        env: Mapping[str, str] | None = None,
        home: Path | None = None,
    ) -> None:
        self.env = dict(os.environ if env is None else env)
        self.home = Path.home() if home is None else home

    def resolve(
        self,
        *,
        cli_host: str | None,
        config_path: Path | None,
        require_token: bool,
        require_cookie: bool,
        firefox_cookies: bool,
        firefox_profile: Path | None,
    ) -> Credentials:
        config = self._read_config(config_path)
        arcrc = self._read_arcrc()
        host = self._resolve_host(cli_host, config, arcrc)
        token = self._resolve_token(host, arcrc) if require_token else None
        cookie = (
            self._resolve_cookie(
                host,
                config,
                firefox_cookies=firefox_cookies,
                firefox_profile=firefox_profile,
            )
            if require_cookie
            else None
        )
        return Credentials(host=host, conduit_token=token, cookie_header=cookie)

    def _default_config_path(self) -> Path:
        root = Path(
            self.env.get("XDG_CONFIG_HOME", self.home / ".config")
        ).expanduser()
        return root / "phab-feedback" / "config.json"

    def _read_config(self, path: Path | None) -> dict[str, object]:
        selected = self._default_config_path() if path is None else path.expanduser()
        if not selected.exists():
            if path is not None:
                raise ConfigurationError(f"Config file not found: {selected}")
            return {}
        try:
            payload = json.loads(selected.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise ConfigurationError(f"Could not read config file: {selected}") from error
        if not isinstance(payload, dict):
            raise ConfigurationError("Config file must contain a JSON object")
        return payload

    def _arcrc_path(self) -> Path:
        return Path(
            self.env.get("PHAB_FEEDBACK_ARCRC", self.home / ".arcrc")
        ).expanduser()

    def _read_arcrc(self) -> dict[str, object]:
        path = self._arcrc_path()
        if not path.exists():
            return {}
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise ConfigurationError(f"Could not read .arcrc: {path}") from error
        if not isinstance(payload, dict):
            raise ConfigurationError(".arcrc must contain a JSON object")
        return payload

    def _resolve_host(
        self,
        cli_host: str | None,
        config: Mapping[str, object],
        arcrc: Mapping[str, object],
    ) -> str:
        configured = cli_host or self.env.get("PHAB_FEEDBACK_HOST")
        if configured is None:
            value = config.get("host")
            configured = value if isinstance(value, str) else None
        if configured:
            return normalize_host(configured)

        hosts = arcrc.get("hosts")
        available = list(hosts) if isinstance(hosts, dict) else []
        if len(available) == 1:
            return normalize_host(available[0])
        if not available:
            raise ConfigurationError(
                "No Phabricator host configured; use --host, "
                "PHAB_FEEDBACK_HOST, or the config file"
            )
        raise ConfigurationError(
            "Multiple .arcrc hosts found; select one with --host or "
            "PHAB_FEEDBACK_HOST"
        )

    def _resolve_token(
        self, host: str, arcrc: Mapping[str, object]
    ) -> str:
        token = self.env.get("PHAB_FEEDBACK_TOKEN")
        if token:
            return token
        hosts = arcrc.get("hosts")
        if isinstance(hosts, dict):
            for candidate, raw_settings in hosts.items():
                if normalize_host(candidate) != host or not isinstance(
                    raw_settings, dict
                ):
                    continue
                value = raw_settings.get("token")
                if isinstance(value, str) and value:
                    return value
        raise ConfigurationError(
            f"No Conduit token found for {host}; configure .arcrc or "
            "PHAB_FEEDBACK_TOKEN"
        )

    def _resolve_cookie(
        self,
        host: str,
        config: Mapping[str, object],
        *,
        firefox_cookies: bool,
        firefox_profile: Path | None,
    ) -> str:
        cookie_name = config.get("cookie_name", "phsid")
        if not isinstance(cookie_name, str) or not cookie_name:
            raise ConfigurationError("cookie_name must be a non-empty string")
        raw = self.env.get("PHAB_FEEDBACK_SESSION_COOKIE")
        if raw:
            stripped = raw.strip()
            if stripped.startswith(f"{cookie_name}=") or ";" in stripped:
                return stripped
            return f"{cookie_name}={stripped}"

        use_firefox = firefox_cookies or config.get("firefox_cookies") is True
        configured_profile = config.get("firefox_profile")
        profile = firefox_profile
        if profile is None and isinstance(configured_profile, str):
            profile = Path(configured_profile).expanduser()
        if use_firefox:
            return discover_firefox_cookie(
                hostname=urlsplit(host).hostname or "",
                cookie_name=cookie_name,
                profile=profile,
                home=self.home,
            )
        raise ConfigurationError(
            "This command needs a web session; set "
            "PHAB_FEEDBACK_SESSION_COOKIE or pass --firefox-cookies"
        )


def normalize_host(host: str) -> str:
    value = host.strip().rstrip("/")
    if value.endswith("/api"):
        value = value[:-4]
    parsed = urlsplit(value)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ConfigurationError(
            "Phabricator host must be an absolute http(s) URL"
        )
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ConfigurationError(
            "Phabricator host must not contain credentials, a query, or a fragment"
        )
    return f"{parsed.scheme}://{parsed.netloc}{parsed.path.rstrip('/')}"


def discover_firefox_cookie(
    *,
    hostname: str,
    cookie_name: str,
    profile: Path | None,
    home: Path,
) -> str:
    selected = profile or find_firefox_profile(home)
    if selected is None:
        raise ConfigurationError("No Firefox profile found")
    database = selected / "cookies.sqlite"
    if not database.exists():
        raise ConfigurationError(f"Firefox cookie database not found: {database}")

    temporary = tempfile.NamedTemporaryFile(suffix=".sqlite", delete=False)
    temporary.close()
    copy = Path(temporary.name)
    try:
        shutil.copy2(database, copy)
        with sqlite3.connect(copy) as connection:
            rows = connection.execute(
                """
                SELECT name, value FROM moz_cookies
                WHERE name IN (?, 'phusr') AND (host = ? OR host = ?)
                """,
                (cookie_name, hostname, f".{hostname}"),
            ).fetchall()
    except (OSError, sqlite3.Error) as error:
        raise ConfigurationError("Could not read Firefox cookie database") from error
    finally:
        copy.unlink(missing_ok=True)

    values = {str(name): str(value) for name, value in rows}
    session = values.get(cookie_name)
    if not session:
        raise ConfigurationError(
            f"No {cookie_name} Firefox cookie found for {hostname}"
        )
    pairs = [f"{cookie_name}={session}"]
    if values.get("phusr"):
        pairs.append(f"phusr={values['phusr']}")
    return "; ".join(pairs)


def find_firefox_profile(home: Path) -> Path | None:
    roots = [
        home / "Library" / "Application Support" / "Firefox",
        home / ".mozilla" / "firefox",
    ]
    for root in roots:
        profiles_ini = root / "profiles.ini"
        if profiles_ini.exists():
            parser = configparser.ConfigParser()
            parser.read(profiles_ini, encoding="utf-8")
            candidates: list[tuple[bool, Path]] = []
            for section in parser.sections():
                if not section.startswith("Profile"):
                    continue
                raw_path = parser.get(section, "Path", fallback="")
                if not raw_path:
                    continue
                candidate = Path(raw_path)
                if parser.getboolean(section, "IsRelative", fallback=True):
                    candidate = root / candidate
                candidates.append(
                    (parser.getboolean(section, "Default", fallback=False), candidate)
                )
            for _, candidate in sorted(candidates, reverse=True):
                if candidate.exists():
                    return candidate
        profiles = root / "Profiles"
        if profiles.exists():
            matches = sorted(profiles.glob("*.default-release"))
            matches.extend(sorted(profiles.glob("*.default")))
            matches.extend(sorted(path for path in profiles.iterdir() if path.is_dir()))
            if matches:
                return matches[0]
    return None
