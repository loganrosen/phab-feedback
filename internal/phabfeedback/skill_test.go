package phabfeedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loganrosen/phab-feedback/skills"
)

func runSkill(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := Run(args, strings.NewReader(""), &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func TestSkillShowOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PHAB_FEEDBACK_HOST", "not a URL")
	t.Setenv("PHAB_FEEDBACK_TOKEN", "")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	args := []string{"--config", filepath.Join(t.TempDir(), "missing.json"), "skill", "show"}
	status, stdout, stderr := runSkill(t, args...)
	if status != 0 || stderr != "" || stdout != workflowGuide {
		t.Fatalf("show status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	status, stdout, stderr = runSkill(t, append(args, "--format=json")...)
	var result struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if status != 0 || stderr != "" || result.Markdown != workflowGuide {
		t.Fatalf("JSON show status=%d guide=%q stderr=%q", status, result.Markdown, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("show must not install a skill: %v", err)
	}
}

func TestEmbeddedSkillSourcesAndSafeguards(t *testing.T) {
	for _, source := range []struct {
		path, embedded string
	}{
		{"resources/workflow.md", workflowGuide},
		{"../../skills/phab-feedback/SKILL.md", skills.Discovery},
	} {
		content, err := os.ReadFile(source.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != source.embedded {
			t.Fatalf("embedded source does not match %s", source.path)
		}
	}
	for _, required := range []string{
		"only from their `id` fields",
		"separate mutations requiring separate",
		"including unrelated",
		"Empty Comment", "Action(s) With No Effect",
		"save or close",
		"interrupted", "pending undo-Done",
		"validates\nthe complete manifest",
		`outcome: "blocked"`, `outcome: "not-attempted"`, "`recovery`",
		"does not\nindependently prove reply publication",
		"done-or-pending-undo",
	} {
		if !strings.Contains(workflowGuide, required) {
			t.Errorf("workflow guide missing safeguard %q", required)
		}
	}
	if !strings.HasPrefix(skills.Discovery, "---\nname: phab-feedback\n") ||
		!strings.Contains(skills.Discovery, "\ndescription: ") ||
		!strings.Contains(skills.Discovery, "Before doing any phab-feedback work, run `phab-feedback skill show`") ||
		!strings.Contains(skills.Discovery, "@latest skill show") ||
		len(skills.Discovery) > 2500 {
		t.Fatalf("expected a small discoverable stub directing agents to read the CLI guide")
	}
}

func TestSkillInstallOfflineDefaultAndIdempotence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PHAB_FEEDBACK_HOST", "invalid")
	t.Setenv("PHAB_FEEDBACK_TOKEN", "")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	destination := filepath.Join(home, ".agents", "skills", "phab-feedback", "SKILL.md")
	for _, changed := range []bool{true, false} {
		status, stdout, stderr := runSkill(t, "skill", "install", "--format=json")
		var result skillInstallResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatal(err)
		}
		if status != 0 || stderr != "" || result.Path != destination || result.Changed != changed {
			t.Fatalf("install status=%d result=%+v stderr=%q", status, result, stderr)
		}
	}
	content, err := readSkillTestFile(destination)
	if err != nil || string(content) != skills.Discovery {
		t.Fatalf("installed content=%q error=%v", content, err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runSkill(t, "skill", "install")
	after, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Already installed") ||
		!os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatalf("matching file was not left unchanged: status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestSkillInstallOverrideAndConflicts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(t.TempDir(), "custom", "phab-feedback")
	status, stdout, stderr := runSkill(t, "skill", "install", "--dir", directory)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Installed") {
		t.Fatalf("install status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	path := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(path, []byte("local customization\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = runSkill(t, "skill", "install", "--dir", directory)
	if status != 1 || stdout != "" || !strings.Contains(stderr, "--force") {
		t.Fatalf("conflict status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	content, err := readSkillTestFile(path)
	if err != nil || string(content) != "local customization\n" {
		t.Fatalf("custom content changed: %q %v", content, err)
	}
	status, stdout, stderr = runSkill(t, "skill", "install", "--dir", directory, "--force")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Installed") {
		t.Fatalf("force status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	content, err = readSkillTestFile(path)
	if err != nil || string(content) != skills.Discovery {
		t.Fatalf("forced content=%q error=%v", content, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("unexpected remaining files: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination override touched default directory: %v", err)
	}
}

func TestSkillInstallPathErrors(t *testing.T) {
	for _, kind := range []string{"directory-is-file", "skill-is-directory", "skill-is-symlink", "skill-is-dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "SKILL.md")
			switch kind {
			case "directory-is-file":
				directory = target
				if err := os.WriteFile(directory, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "skill-is-directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "skill-is-symlink", "skill-is-dangling-symlink":
				other := filepath.Join(t.TempDir(), "original")
				if kind == "skill-is-symlink" {
					if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
			}
			for _, force := range []bool{false, true} {
				if _, err := installSkill(directory, force); err == nil {
					t.Fatalf("expected path error with force=%v", force)
				}
			}
			if kind == "skill-is-symlink" {
				content, err := readSkillTestFile(target)
				if err != nil || string(content) != "keep" {
					t.Fatalf("symlink target changed: %q %v", content, err)
				}
			}
		})
	}
}

func TestSkillInstallPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "unwritable-directory", true: "unreadable-skill"}[existing], func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "SKILL.md")
			if existing {
				if err := os.WriteFile(path, []byte("custom"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(directory, 0o500); err != nil { //nolint:gosec // Read-only directory, not file permissions.
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(directory, 0o700); err != nil { //nolint:gosec // Restore private directory traversal for test cleanup.
						t.Error(err)
					}
				})
			}
			if _, err := installSkill(directory, true); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected explicit permission error: %v", err)
			}
		})
	}
}

func TestSkillCommandHelpAndValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"skill", "--help"}, {"skill", "show", "--help"}, {"skill", "install", "--help"},
	} {
		status, stdout, stderr := runSkill(t, args...)
		if status != 0 || stdout == "" || stderr != "" {
			t.Fatalf("help %v status=%d stdout=%q stderr=%q", args, status, stdout, stderr)
		}
	}
	for _, args := range [][]string{
		{"skill", "show", "extra"}, {"skill", "install", "extra"}, {"skill", "unknown"},
		{"skill", "show", "--format=invalid"},
	} {
		status, _, stderr := runSkill(t, args...)
		if status != 1 || stderr == "" {
			t.Fatalf("invalid args %v status=%d stderr=%q", args, status, stderr)
		}
	}
}

func TestMutationHelpIncludesWorkflowSemantics(t *testing.T) {
	for _, test := range []struct {
		args     []string
		required []string
	}{
		{[]string{"D123", "reply", "--help"}, []string{"exact comment id", "separate approval", "unrelated browser drafts", "dialog blocks"}},
		{[]string{"D123", "done", "--help"}, []string{"separate approval", "unrelated drafts", "rerun done", "undo-Done"}},
		{[]string{"D123", "submit", "--help"}, []string{"unrelated drafts", "publication approval", "Every server dialog", "approve a retry"}},
		{[]string{"D123", "verify", "--help"}, []string{"not independent publication proof", "pending undo-Done"}},
		{[]string{"batch", "--help"}, []string{"separate approval", "outside the manifest", "partial failures", "recovery", "No submission follows"}},
	} {
		status, stdout, stderr := runSkill(t, test.args...)
		if status != 0 || stderr != "" {
			t.Fatalf("help %v status=%d stderr=%q", test.args, status, stderr)
		}
		for _, required := range test.required {
			if !strings.Contains(stdout, required) {
				t.Errorf("help %v missing %q", test.args, required)
			}
		}
	}
}

func TestStandaloneBinarySkillCommands(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "phab-feedback")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/phab-feedback") //nolint:gosec // Fixed build arguments; output is inside t.TempDir.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build standalone binary: %v\n%s", err, output)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	t.Setenv("PHAB_FEEDBACK_HOST", "invalid")
	t.Setenv("PHAB_FEEDBACK_TOKEN", "")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	show := exec.CommandContext(t.Context(), binary, "skill", "show") //nolint:gosec // Execute only the binary built by this test.
	show.Dir = directory
	if output, err := show.CombinedOutput(); err != nil || string(output) != workflowGuide {
		t.Fatalf("standalone show: %v\n%s", err, output)
	}
	destination := filepath.Join(directory, "skill")
	install := exec.CommandContext(t.Context(), binary, "skill", "install", "--dir", destination) //nolint:gosec // Execute the test-built binary with a t.TempDir destination.
	install.Dir = directory
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("standalone install: %v\n%s", err, output)
	}
	content, err := readSkillTestFile(filepath.Join(destination, "SKILL.md"))
	if err != nil || string(content) != skills.Discovery {
		t.Fatalf("standalone installed content=%q error=%v", content, err)
	}
}

func TestSkillOutputErrors(t *testing.T) {
	root := newRootCommand(nil, strings.NewReader(""), failingSkillWriter{}, io.Discard)
	root.SetArgs([]string{"skill", "show"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected output failure")
	}
}

type failingSkillWriter struct{}

func (failingSkillWriter) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

func readSkillTestFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // Callers only pass files inside t.TempDir.
}
