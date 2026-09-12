import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

from phab_feedback.config import (
    ConfigResolver,
    discover_firefox_cookie,
    find_firefox_profile,
    normalize_host,
)
from phab_feedback.errors import ConfigurationError


class ConfigTests(unittest.TestCase):
    def test_precedence_and_matching_arcrc_token(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / ".arcrc").write_text(
                json.dumps(
                    {
                        "hosts": {
                            "https://arcrc.example/api/": {"token": "arcrc-token"},
                            "https://cli.example/api/": {"token": "cli-token"},
                        }
                    }
                ),
                encoding="utf-8",
            )
            config = home / "config.json"
            config.write_text(
                json.dumps({"host": "https://config.example"}),
                encoding="utf-8",
            )
            resolver = ConfigResolver(
                env={"PHAB_FEEDBACK_HOST": "https://env.example"},
                home=home,
            )
            credentials = resolver.resolve(
                cli_host="https://cli.example/",
                config_path=config,
                require_token=True,
                require_cookie=False,
                firefox_cookies=False,
                firefox_profile=None,
            )
            self.assertEqual("https://cli.example", credentials.host)
            self.assertEqual("cli-token", credentials.conduit_token)

    def test_environment_credentials_are_not_required_in_config(self) -> None:
        resolver = ConfigResolver(
            env={
                "PHAB_FEEDBACK_HOST": "https://phab.example",
                "PHAB_FEEDBACK_TOKEN": "secret-token",
                "PHAB_FEEDBACK_SESSION_COOKIE": "secret-cookie",
            },
            home=Path("/not-used"),
        )
        credentials = resolver.resolve(
            cli_host=None,
            config_path=None,
            require_token=True,
            require_cookie=True,
            firefox_cookies=False,
            firefox_profile=None,
        )
        self.assertEqual("secret-token", credentials.conduit_token)
        self.assertEqual("phsid=secret-cookie", credentials.cookie_header)

    def test_cookie_value_with_padding_is_not_mistaken_for_header(self) -> None:
        resolver = ConfigResolver(
            env={
                "PHAB_FEEDBACK_HOST": "https://phab.example",
                "PHAB_FEEDBACK_SESSION_COOKIE": "base64==",
            },
            home=Path("/not-used"),
        )
        credentials = resolver.resolve(
            cli_host=None,
            config_path=None,
            require_token=False,
            require_cookie=True,
            firefox_cookies=False,
            firefox_profile=None,
        )
        self.assertEqual("phsid=base64==", credentials.cookie_header)

    def test_complete_cookie_header_is_preserved(self) -> None:
        resolver = ConfigResolver(
            env={
                "PHAB_FEEDBACK_HOST": "https://phab.example",
                "PHAB_FEEDBACK_SESSION_COOKIE": "phsid=value; phusr=logan",
            },
            home=Path("/not-used"),
        )
        credentials = resolver.resolve(
            cli_host=None,
            config_path=None,
            require_token=False,
            require_cookie=True,
            firefox_cookies=False,
            firefox_profile=None,
        )
        self.assertEqual(
            "phsid=value; phusr=logan", credentials.cookie_header
        )

    def test_firefox_cookie_discovery_uses_selected_host(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory)

            with sqlite3.connect(profile / "cookies.sqlite") as connection:
                connection.execute(
                    "CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)"
                )
                connection.executemany(
                    "INSERT INTO moz_cookies VALUES (?, ?, ?)",
                    [
                        ("phsid", "wrong", ".other.example"),
                        ("phsid", "right", ".phab.example"),
                        ("phusr", "logan", ".phab.example"),
                    ],
                )
            self.assertEqual(
                "phsid=right; phusr=logan",
                discover_firefox_cookie(
                    hostname="phab.example",
                    cookie_name="phsid",
                    profile=profile,
                    home=profile,
                ),
            )

    def test_firefox_cookie_discovery_reads_uncheckpointed_wal(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory)
            with sqlite3.connect(profile / "cookies.sqlite") as connection:
                connection.execute("PRAGMA journal_mode=WAL")
                connection.execute("PRAGMA wal_autocheckpoint=0")
                connection.execute(
                    "CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)"
                )
                connection.commit()
                connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
                connection.executemany(
                    "INSERT INTO moz_cookies VALUES (?, ?, ?)",
                    [
                        ("phsid", "right", ".phab.example"),
                        ("phusr", "logan", ".phab.example"),
                    ],
                )
                connection.commit()

                self.assertEqual(
                    "phsid=right; phusr=logan",
                    discover_firefox_cookie(
                        hostname="phab.example",
                        cookie_name="phsid",
                        profile=profile,
                        home=profile,
                    ),
                )

    def test_firefox_install_default_precedes_legacy_profile_default(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / "Library" / "Application Support" / "Firefox"
            legacy = root / "Profiles" / "legacy.default"
            current = root / "Profiles" / "current.default-release"
            legacy.mkdir(parents=True)
            current.mkdir()
            (root / "profiles.ini").write_text(
                """
[Profile0]
Name=default-release
IsRelative=1
Path=Profiles/current.default-release

[Profile1]
Name=default
IsRelative=1
Path=Profiles/legacy.default
Default=1

[InstallABC]
Default=Profiles/current.default-release
Locked=1
""".strip(),
                encoding="utf-8",
            )

            self.assertEqual(current, find_firefox_profile(home))

    def test_firefox_cookie_discovery_checks_multiple_install_defaults(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / ".mozilla" / "firefox"
            first = root / "first.default-release"
            second = root / "second.default-release"
            first.mkdir(parents=True)
            second.mkdir()
            (root / "profiles.ini").write_text(
                """
[InstallAAA]
Default=first.default-release

[InstallBBB]
Default=second.default-release
""".strip(),
                encoding="utf-8",
            )
            self._write_cookie_database(first, [("other", "value", ".phab.example")])
            self._write_cookie_database(
                second,
                [
                    ("phsid", "right", ".phab.example"),
                    ("phusr", "logan", ".phab.example"),
                ],
            )

            self.assertEqual(
                "phsid=right; phusr=logan",
                discover_firefox_cookie(
                    hostname="phab.example",
                    cookie_name="phsid",
                    profile=None,
                    home=home,
                ),
            )

    def test_explicit_firefox_profile_does_not_fall_back(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / "AppData" / "Roaming" / "Mozilla" / "Firefox"
            explicit = root / "Profiles" / "explicit.default"
            other = root / "Profiles" / "other.default-release"
            explicit.mkdir(parents=True)
            other.mkdir()
            (root / "profiles.ini").write_text(
                """
[InstallABC]
Default=Profiles/other.default-release
""".strip(),
                encoding="utf-8",
            )
            self._write_cookie_database(
                explicit, [("other", "value", ".phab.example")]
            )
            self._write_cookie_database(
                other, [("phsid", "not-selected", ".phab.example")]
            )

            with self.assertRaisesRegex(ConfigurationError, "No phsid"):
                discover_firefox_cookie(
                    hostname="phab.example",
                    cookie_name="phsid",
                    profile=explicit,
                    home=home,
                )

    @staticmethod
    def _write_cookie_database(
        profile: Path, cookies: list[tuple[str, str, str]]
    ) -> None:
        with sqlite3.connect(profile / "cookies.sqlite") as connection:
            connection.execute(
                "CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)"
            )
            connection.executemany("INSERT INTO moz_cookies VALUES (?, ?, ?)", cookies)

    def test_single_arcrc_host_is_inferred(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / ".arcrc").write_text(
                json.dumps(
                    {"hosts": {"https://one.example/api/": {"token": "token"}}}
                ),
                encoding="utf-8",
            )
            credentials = ConfigResolver(env={}, home=home).resolve(
                cli_host=None,
                config_path=None,
                require_token=True,
                require_cookie=False,
                firefox_cookies=False,
                firefox_profile=None,
            )
            self.assertEqual("https://one.example", credentials.host)

    def test_multiple_hosts_require_selection(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / ".arcrc").write_text(
                json.dumps({"hosts": {"https://a": {}, "https://b": {}}}),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ConfigurationError, "Multiple"):
                ConfigResolver(env={}, home=home).resolve(
                    cli_host=None,
                    config_path=None,
                    require_token=False,
                    require_cookie=False,
                    firefox_cookies=False,
                    firefox_profile=None,
                )

    def test_normalize_host_rejects_invalid_url(self) -> None:
        with self.assertRaises(ConfigurationError):
            normalize_host("phabricator.example")
        with self.assertRaises(ConfigurationError):
            normalize_host("https://token@phabricator.example")


if __name__ == "__main__":
    unittest.main()
