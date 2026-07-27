import json
import tempfile
import unittest
from pathlib import Path

from phab_feedback.config import (
    ConfigResolver,
    discover_firefox_cookie,
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
            import sqlite3

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
