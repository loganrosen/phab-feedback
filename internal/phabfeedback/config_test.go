package phabfeedback

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeHostAndCredentialPrecedence(t *testing.T) {
	if _, err := normalizeHost("phabricator.example"); err == nil {
		t.Fatal("expected invalid host error")
	}
	if _, err := normalizeHost("https://token@phabricator.example"); err == nil {
		t.Fatal("expected credential host error")
	}
	t.Setenv("PHAB_FEEDBACK_HOST", "https://env.example")
	t.Setenv("PHAB_FEEDBACK_TOKEN", "secret-token")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "base64==")
	got, err := resolveCredentials(t.Context(), credentialOptions{requireToken: true, requireCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.host != "https://env.example" || got.token != "secret-token" || got.cookie != "phsid=base64==" {
		t.Fatalf("unexpected credentials: %+v", got)
	}
}

func TestFirefoxCookieDiscoveryReadsWAL(t *testing.T) {
	profile := t.TempDir()
	database := filepath.Join(profile, "cookies.sqlite")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)",
		"PRAGMA wal_checkpoint(TRUNCATE)",
		"INSERT INTO moz_cookies VALUES ('phsid', 'right', '.phab.example'), ('phusr', 'logan', '.phab.example')",
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	got, err := discoverFirefoxCookie(t.Context(), "phab.example", "phsid", profile, profile)
	if err != nil {
		t.Fatal(err)
	}
	if got != "phsid=right; phusr=logan" {
		t.Fatalf("unexpected cookie: %s", got)
	}
}

func TestFirefoxInstallDefaultPrecedesProfileDefault(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Library", "Application Support", "Firefox")
	legacy := filepath.Join(root, "Profiles", "legacy.default")
	current := filepath.Join(root, "Profiles", "current.default-release")
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0o750); err != nil {
		t.Fatal(err)
	}
	profilesINI := `[Profile0]
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
`
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(profilesINI), 0o600); err != nil {
		t.Fatal(err)
	}
	candidates := firefoxProfileCandidates(home)
	if len(candidates) < 2 || candidates[0] != current || candidates[1] != legacy {
		t.Fatalf("unexpected profile order: %#v", candidates)
	}
}

func TestWebSessionAutomaticallyFallsBackToFirefox(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Library", "Application Support", "Firefox")
	profile := filepath.Join(root, "Profiles", "current.default-release")
	if err := os.MkdirAll(profile, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(`[InstallABC]
Default=Profiles/current.default-release
Locked=1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(profile, "cookies.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO moz_cookies VALUES ('phsid', 'discovered', '.phab.example')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PHAB_FEEDBACK_HOST", "https://phab.example")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(home, "missing-arcrc"))

	got, err := resolveCredentials(t.Context(), credentialOptions{requireCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.cookie != "phsid=discovered" {
		t.Fatalf("cookie = %q, want discovered Firefox cookie", got.cookie)
	}
}

func TestSessionCookieEnvironmentPrecedesFirefox(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, "profile")
	writeFirefoxCookie(t, profile, "firefox")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PHAB_FEEDBACK_HOST", "https://phab.example")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "environment")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(home, "missing-arcrc"))

	got, err := resolveCredentials(t.Context(), credentialOptions{
		requireCookie: true, firefoxProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.cookie != "phsid=environment" {
		t.Fatalf("cookie = %q, want environment cookie", got.cookie)
	}
}

func TestExplicitFirefoxProfilePrecedesAutomaticDiscovery(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Library", "Application Support", "Firefox")
	automatic := filepath.Join(root, "Profiles", "automatic.default-release")
	explicit := filepath.Join(home, "explicit-profile")
	writeFirefoxCookie(t, automatic, "automatic")
	writeFirefoxCookie(t, explicit, "explicit")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(`[InstallABC]
Default=Profiles/automatic.default-release
Locked=1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PHAB_FEEDBACK_HOST", "https://phab.example")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(home, "missing-arcrc"))

	got, err := resolveCredentials(t.Context(), credentialOptions{
		requireCookie: true, firefoxProfile: explicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.cookie != "phsid=explicit" {
		t.Fatalf("cookie = %q, want explicit profile cookie", got.cookie)
	}
}

func writeFirefoxCookie(t *testing.T, profile, value string) {
	t.Helper()
	if err := os.MkdirAll(profile, 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(profile, "cookies.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO moz_cookies VALUES ('phsid', ?, '.phab.example')", value); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
