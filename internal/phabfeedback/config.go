package phabfeedback

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/ini.v1"
	_ "modernc.org/sqlite"
)

type credentials struct {
	host   string
	token  string
	cookie string
}

type credentialOptions struct {
	host, configPath, firefoxProfile string
	requireToken, requireCookie      bool
	firefoxCookies                   bool
}

type configFile struct {
	Host           string `json:"host"`
	CookieName     string `json:"cookie_name"`
	FirefoxCookies bool   `json:"firefox_cookies"`
	FirefoxProfile string `json:"firefox_profile"`
}

type arcConfig struct {
	Hosts map[string]arcHost `json:"hosts"`
}

type arcHost struct {
	Token string `json:"token"`
}

func resolveCredentials(options credentialOptions) (credentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return credentials{}, fmt.Errorf("Could not determine home directory")
	}
	config, err := readConfig(options.configPath, home)
	if err != nil {
		return credentials{}, err
	}
	arcrc, err := readJSONFile[arcConfig](expandHome(envOr("PHAB_FEEDBACK_ARCRC", filepath.Join(home, ".arcrc")), home), true, ".arcrc")
	if err != nil {
		return credentials{}, err
	}
	host, err := resolveHost(options.host, config, arcrc)
	if err != nil {
		return credentials{}, err
	}
	result := credentials{host: host}
	if options.requireToken {
		result.token, err = resolveToken(host, arcrc)
		if err != nil {
			return credentials{}, err
		}
	}
	if options.requireCookie {
		result.cookie, err = resolveCookie(host, config, options, home)
		if err != nil {
			return credentials{}, err
		}
	}
	return result, nil
}

func readConfig(path, home string) (configFile, error) {
	explicit := path != ""
	if path == "" {
		root := envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		path = filepath.Join(root, "phab-feedback", "config.json")
	}
	return readJSONFile[configFile](expandHome(path, home), !explicit, "config file")
}

func readJSONFile[T any](path string, missingOK bool, label string) (T, error) {
	var zero T
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && missingOK {
		return zero, nil
	}
	if err != nil {
		if os.IsNotExist(err) {
			return zero, fmt.Errorf("Config file not found: %s", path)
		}
		return zero, fmt.Errorf("Could not read %s: %s", label, path)
	}
	var payload T
	if err := json.Unmarshal(data, &payload); err != nil {
		return zero, fmt.Errorf("Could not read %s: %s", label, path)
	}
	return payload, nil
}

func resolveHost(cliHost string, config configFile, arcrc arcConfig) (string, error) {
	configured := cliHost
	if configured == "" {
		configured = os.Getenv("PHAB_FEEDBACK_HOST")
	}
	if configured == "" {
		configured = config.Host
	}
	if configured != "" {
		return normalizeHost(configured)
	}
	var available []string
	for candidate := range arcrc.Hosts {
		available = append(available, candidate)
	}
	sort.Strings(available)
	if len(available) == 1 {
		return normalizeHost(available[0])
	}
	if len(available) == 0 {
		return "", fmt.Errorf("No Phabricator host configured; use --host, PHAB_FEEDBACK_HOST, or the config file")
	}
	return "", fmt.Errorf("Multiple .arcrc hosts found; select one with --host or PHAB_FEEDBACK_HOST")
}

func normalizeHost(host string) (string, error) {
	value := strings.TrimSuffix(strings.TrimSpace(host), "/")
	value = strings.TrimSuffix(value, "/api")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("Phabricator host must be an absolute http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Phabricator host must not contain credentials, a query, or a fragment")
	}
	return parsed.Scheme + "://" + parsed.Host + strings.TrimSuffix(parsed.Path, "/"), nil
}

func resolveToken(host string, arcrc arcConfig) (string, error) {
	if token := os.Getenv("PHAB_FEEDBACK_TOKEN"); token != "" {
		return token, nil
	}
	for candidate, settings := range arcrc.Hosts {
		normalized, err := normalizeHost(candidate)
		if err != nil || normalized != host {
			continue
		}
		if settings.Token != "" {
			return settings.Token, nil
		}
	}
	return "", fmt.Errorf("No Conduit token found for %s; configure .arcrc or PHAB_FEEDBACK_TOKEN", host)
}

func resolveCookie(host string, config configFile, options credentialOptions, home string) (string, error) {
	cookieName := "phsid"
	if config.CookieName != "" {
		cookieName = config.CookieName
	}
	if raw := strings.TrimSpace(os.Getenv("PHAB_FEEDBACK_SESSION_COOKIE")); raw != "" {
		if strings.HasPrefix(raw, cookieName+"=") || strings.Contains(raw, ";") {
			return raw, nil
		}
		return cookieName + "=" + raw, nil
	}
	profile := options.firefoxProfile
	if profile == "" {
		profile = config.FirefoxProfile
	}
	useFirefox := options.firefoxCookies || config.FirefoxCookies
	if useFirefox || profile != "" {
		parsed, _ := url.Parse(host)
		return discoverFirefoxCookie(parsed.Hostname(), cookieName, expandHome(profile, home), home)
	}
	return "", fmt.Errorf("This command needs a web session; set PHAB_FEEDBACK_SESSION_COOKIE or pass --firefox-cookies")
}

func discoverFirefoxCookie(hostname, cookieName, profile, home string) (string, error) {
	if profile != "" {
		database := filepath.Join(profile, "cookies.sqlite")
		if _, err := os.Stat(database); err != nil {
			return "", fmt.Errorf("Firefox cookie database not found: %s", database)
		}
		cookie, err := readFirefoxCookie(database, hostname, cookieName)
		if err != nil {
			return "", err
		}
		if cookie == "" {
			return "", fmt.Errorf("No %s Firefox cookie found for %s", cookieName, hostname)
		}
		return cookie, nil
	}
	profiles := firefoxProfileCandidates(home)
	if len(profiles) == 0 {
		return "", fmt.Errorf("No Firefox profile found")
	}
	readable, readError := false, false
	for _, candidate := range profiles {
		database := filepath.Join(candidate, "cookies.sqlite")
		if _, err := os.Stat(database); err != nil {
			continue
		}
		cookie, err := readFirefoxCookie(database, hostname, cookieName)
		if err != nil {
			readError = true
			continue
		}
		readable = true
		if cookie != "" {
			return cookie, nil
		}
	}
	if readable {
		return "", fmt.Errorf("No %s Firefox cookie found for %s in discovered profiles", cookieName, hostname)
	}
	if readError {
		return "", fmt.Errorf("Could not read any Firefox cookie database")
	}
	return "", fmt.Errorf("No Firefox cookie database found in discovered profiles")
}

func readFirefoxCookie(database, hostname, cookieName string) (string, error) {
	directory, err := os.MkdirTemp("", "phab-feedback-cookies-*")
	if err != nil {
		return "", fmt.Errorf("Could not read Firefox cookie database")
	}
	defer os.RemoveAll(directory)
	copyPath, err := snapshotFirefoxDatabase(database, directory)
	if err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", copyPath)
	if err != nil {
		return "", fmt.Errorf("Could not read Firefox cookie database")
	}
	defer db.Close()
	rows, err := db.Query(
		"SELECT name, value FROM moz_cookies WHERE name IN (?, 'phusr') AND (host = ? OR host = ?)",
		cookieName, hostname, "."+hostname,
	)
	if err != nil {
		return "", fmt.Errorf("Could not read Firefox cookie database")
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return "", fmt.Errorf("Could not read Firefox cookie database")
		}
		values[name] = value
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("Could not read Firefox cookie database")
	}
	if values[cookieName] == "" {
		return "", nil
	}
	result := cookieName + "=" + values[cookieName]
	if values["phusr"] != "" {
		result += "; phusr=" + values["phusr"]
	}
	return result, nil
}

func snapshotFirefoxDatabase(database, directory string) (string, error) {
	copyPath := filepath.Join(directory, filepath.Base(database))
	wal := database + "-wal"
	walCopy := copyPath + "-wal"
	for attempt := 0; attempt < 3; attempt++ {
		_ = os.Remove(copyPath)
		_ = os.Remove(walCopy)
		if copyFile(database, copyPath) == nil {
			_, walErr := os.Stat(wal)
			hasWAL := walErr == nil
			if !hasWAL || copyFile(wal, walCopy) == nil {
				if filesMatch(database, copyPath) {
					_, currentWALErr := os.Stat(wal)
					if (currentWALErr == nil) == hasWAL && (!hasWAL || filesMatch(wal, walCopy)) && filesMatch(database, copyPath) {
						return copyPath, nil
					}
				}
			}
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return "", fmt.Errorf("Could not take a consistent snapshot of Firefox cookie database")
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func filesMatch(first, second string) bool {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(second)
	if err != nil || firstInfo.Size() != secondInfo.Size() {
		return false
	}
	firstFile, err := os.Open(first)
	if err != nil {
		return false
	}
	defer firstFile.Close()
	secondFile, err := os.Open(second)
	if err != nil {
		return false
	}
	defer secondFile.Close()
	firstHash, secondHash := sha256.New(), sha256.New()
	if _, err := io.Copy(firstHash, firstFile); err != nil {
		return false
	}
	if _, err := io.Copy(secondHash, secondFile); err != nil {
		return false
	}
	return bytes.Equal(firstHash.Sum(nil), secondHash.Sum(nil))
}

func firefoxProfileCandidates(home string) []string {
	roots := []string{
		filepath.Join(home, "Library", "Application Support", "Firefox"),
		filepath.Join(home, ".mozilla", "firefox"),
		filepath.Join(home, "AppData", "Roaming", "Mozilla", "Firefox"),
	}
	var result []string
	seen := map[string]bool{}
	add := func(candidate string) {
		if candidate == "" {
			return
		}
		absolute, _ := filepath.Abs(candidate)
		key := strings.ToLower(filepath.Clean(absolute))
		if seen[key] {
			return
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			seen[key] = true
			result = append(result, candidate)
		}
	}
	for _, root := range roots {
		config, err := ini.Load(filepath.Join(root, "profiles.ini"))
		if err != nil {
			config = ini.Empty()
		}
		for _, section := range sortedINISections(config, "Install") {
			add(resolveProfilePath(root, section.Key("Default").String(), true))
		}
		type profileEntry struct {
			defaultProfile bool
			section, path  string
		}
		var entries []profileEntry
		for _, section := range sortedINISections(config, "Profile") {
			path := resolveProfilePath(root, section.Key("Path").String(), section.Key("IsRelative").String() != "0")
			entries = append(entries, profileEntry{
				defaultProfile: iniBoolean(section.Key("Default").String()),
				section:        section.Name(),
				path:           path,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].defaultProfile != entries[j].defaultProfile {
				return entries[i].defaultProfile
			}
			return entries[i].section < entries[j].section
		})
		for _, entry := range entries {
			add(entry.path)
		}
		profilesRoot := filepath.Join(root, "Profiles")
		matches, _ := filepath.Glob(filepath.Join(profilesRoot, "*.default-release"))
		legacy, _ := filepath.Glob(filepath.Join(profilesRoot, "*.default"))
		sort.Strings(matches)
		sort.Strings(legacy)
		for _, candidate := range append(matches, legacy...) {
			add(candidate)
		}
		entriesOnDisk, _ := os.ReadDir(profilesRoot)
		for _, entry := range entriesOnDisk {
			if entry.IsDir() {
				add(filepath.Join(profilesRoot, entry.Name()))
			}
		}
	}
	return result
}

func sortedINISections(config *ini.File, prefix string) []*ini.Section {
	var result []*ini.Section
	for _, section := range config.Sections() {
		if strings.HasPrefix(section.Name(), prefix) {
			result = append(result, section)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

func iniBoolean(value string) bool {
	return value == "1" || strings.EqualFold(value, "true")
}

func resolveProfilePath(root, path string, relative bool) string {
	if path == "" {
		return ""
	}
	if relative && !filepath.IsAbs(path) {
		return filepath.Join(root, filepath.FromSlash(path))
	}
	return filepath.FromSlash(path)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
