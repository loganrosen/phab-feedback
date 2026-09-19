package phabfeedback

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

func TestParseArgsAndMessageInputs(t *testing.T) {
	message, err := readMessage(messageOptions{messageFile: "-", messageFileSet: true}, strings.NewReader("from stdin"))
	if err != nil || message != "from stdin" {
		t.Fatalf("read message: %q %v", message, err)
	}
	for _, value := range []string{
		"2025-01-02",
		"2025-01-02 03:04:05",
		"2025-01-02T03:04:05.123",
		"2025-01-02T03:04:05.123Z",
		"2025-01-02T03:04:05+01:30",
		"2025-01",
		"2025-002",
	} {
		if _, err := parseTime(value); err != nil {
			t.Fatalf("parse time %q: %v", value, err)
		}
	}
	for _, value := range []string{"01/02/2025", "not-a-time"} {
		if _, err := parseTime(value); err == nil {
			t.Fatalf("expected time %q to be rejected", value)
		}
	}
	var stdout, stderr bytes.Buffer
	if status := Run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); status != 0 || !strings.Contains(stdout.String(), "pass a revision first") {
		t.Fatalf("help status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if status := Run([]string{"list", "--role=invalid"}, strings.NewReader(""), &stdout, &stderr); status != 1 || !strings.Contains(stderr.String(), `invalid --role value "invalid"`) {
		t.Fatalf("invalid role status=%d stderr=%q", status, stderr.String())
	}
}

func TestJSONOutputUsesStableIndentation(t *testing.T) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"posted": true}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\n  \"posted\": true\n}\n" {
		t.Fatalf("unexpected JSON: %q", output.String())
	}
}

func TestOutputFormatDefaultsToText(t *testing.T) {
	root := newRootCommand(nil, strings.NewReader(""), io.Discard, io.Discard)
	format, err := root.PersistentFlags().GetString("format")
	if err != nil {
		t.Fatal(err)
	}
	if format != "text" {
		t.Fatalf("default format = %q, want text", format)
	}
}

func TestRevisionArgument(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "direct", args: []string{"D123", "--timeline"}, want: "D123"},
		{name: "global flag", args: []string{"--format", "json", "D123"}, want: "D123"},
		{name: "global flag assignment", args: []string{"--format=json", "123"}, want: "123"},
		{name: "help", args: []string{"help", "D123"}, want: "D123"},
		{name: "completion", args: []string{cobra.ShellCompRequestCmd, "D123"}, want: "D123"},
		{name: "empty argument", args: []string{"", "D123"}, want: "D123"},
		{name: "list", args: []string{"list", "--role", "reviewing"}},
		{name: "doctor", args: []string{"doctor"}},
		{name: "invalid zero revision", args: []string{"D0"}, want: "D0"},
		{name: "invalid text revision", args: []string{"Dxyz"}, want: "Dxyz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newRootCommand(nil, strings.NewReader(""), io.Discard, io.Discard)
			if got := revisionArgument(root, test.args); got != test.want {
				t.Fatalf("revision argument = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInvalidRevisionKeepsSpecificValidationError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := Run([]string{"D0"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("status = %d, want 1", status)
	}
	if !strings.Contains(stderr.String(), "invalid revision identifier: D0") {
		t.Fatalf("unexpected error: %q", stderr.String())
	}
}

func TestRevisionHelpShowsErgonomicSurface(t *testing.T) {
	var output bytes.Buffer
	root := newRootCommand([]string{"D123", "--help"}, strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"D123", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"--threads", "--timeline", "Respond:", "reply", "done", "Mozilla Review Helper:", "rate", "ai-review"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("revision help missing %q:\n%s", expected, text)
		}
	}
	for _, removed := range []string{"reply-inline", "mark-done", "request-ai-review"} {
		if strings.Contains(text, removed) {
			t.Fatalf("revision help contains removed command %q:\n%s", removed, text)
		}
	}
}

func TestRevisionListTextLayout(t *testing.T) {
	got := ansi.Strip(revisionList(revisionListResult{
		Count: 1, Role: "responsible", Status: "open",
		Revisions: []revisionRecord{{
			ID: 123, Title: "Improve command output",
			Status:    revisionStatus{Value: "needs-review", Name: "Needs Review"},
			Author:    &handleInfo{FullName: "Logan Rosen"},
			Reviewers: []reviewer{{Handle: handleInfo{FullName: "Reviewer"}, Decision: "accepted"}},
			Modified:  "2026-09-19T02:35:58+00:00", URI: "https://phab.example/D123",
			MergeConflictStatus: &mergeConflictStatus{
				Status: "conflict", Reason: "The landing conflicts with the target branch.",
			},
		}},
	}))
	want := strings.Join([]string{
		"1 revisions (responsible, open)",
		"D123  Needs Review  Improve command output",
		"  Author     Logan Rosen",
		"  Reviewers  Reviewer accepted",
		"  Merge      Merge conflict - The landing conflicts with the target branch.",
		"  Updated    2026-09-19T02:35:58+00:00",
		"  URL        https://phab.example/D123",
	}, "\n")
	if got != want {
		t.Fatalf("text output:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestReviewerRejectedStatusUsesUILabel(t *testing.T) {
	if got := decisionText("rejected"); got != "requested changes" {
		t.Fatalf("decision text = %q, want requested changes", got)
	}
}

func TestNormalizeRevisionMergeConflictStatus(t *testing.T) {
	raw := revision(123)
	fields, _ := mapValue(raw["fields"])
	fields["merge.conflict.status"] = map[string]any{
		"status":                         "conflict",
		"reason":                         "Landing may fail.",
		"isStale":                        false,
		"epoch":                          1757000000,
		"checkedAgainstCommit":           "target",
		"checkedAgainstBaseCommit":       "base",
		"checkedAgainstBaseRevisionPHID": nil,
		"checkedAgainstDiffID":           456,
		"checkedAgainstDiffPHID":         "PHID-DIFF-current",
	}
	normalized, err := normalizeRevision(raw, map[string]map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	status := normalized.MergeConflictStatus
	if status.Status != "conflict" || status.Reason != "Landing may fail." {
		t.Fatalf("unexpected merge conflict status: %#v", status)
	}
	if status.CheckedAt != "2025-09-04T15:33:20+00:00" {
		t.Fatalf("checked_at = %#v", status.CheckedAt)
	}
}

func TestTextStylesAreStrippedForNonTerminalOutput(t *testing.T) {
	var output bytes.Buffer
	if _, err := lipgloss.Fprintln(&output, idStyle.Render("D123")); err != nil {
		t.Fatal(err)
	}
	if output.String() != "D123\n" {
		t.Fatalf("non-terminal output = %q, want plain text", output.String())
	}
}

func TestMutationTextOutput(t *testing.T) {
	tests := []struct {
		command string
		result  any
		want    string
	}{
		{
			command: "comment",
			result:  commentResult{RevisionID: 12},
			want:    "Posted a comment on D12.",
		},
		{
			command: "reply",
			result: inlineReplyResult{
				RevisionID: 12, ParentCommentID: 34, DraftCommentID: 56, CreatedReplyID: 56,
				Saved:      true,
				Submission: &submissionResult{RevisionID: 12, Submitted: true},
			},
			want: "Drafted inline reply #56 to comment #34 on D12.\nSubmitted pending drafts on D12.",
		},
		{
			command: "done",
			result: commentActionResult{
				RevisionID: 12,
				Comments: []commentAction{
					{CommentID: 34},
					{CommentID: 35},
				},
			},
			want: "Confirmed #34, #35 Done on D12.",
		},
		{
			command: "ai-review",
			result:  aiReviewResult{RevisionID: 12, Status: "already-in-progress"},
			want:    "A Review Helper AI review is already in progress on D12.",
		},
	}

	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			got, err := renderText(test.command, test.result)
			if err != nil {
				t.Fatal(err)
			}
			if ansi.Strip(got) != test.want {
				t.Fatalf("text output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDoneFailureTextPreservesEarlierConfirmedComments(t *testing.T) {
	got := ansi.Strip(renderDone(commentActionResult{
		RevisionID: 12,
		Comments: []commentAction{
			{CommentID: 34},
			{CommentID: 35, Recovery: "Rerun done before submitting."},
		},
	}))
	want := "Confirmed #34 Done on D12.\n" +
		"Done action for comment #35 on D12 requires recovery: Rerun done before submitting."
	if got != want {
		t.Fatalf("text output = %q, want %q", got, want)
	}
}

func TestBatchAndMutationHelpExposeExplicitPublicationFlags(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"batch", "--help"}, want: []string{"MANIFEST", "--dry-run", "--submit"}},
		{args: []string{"D123", "reply", "--help"}, want: []string{"--done", "--submit"}},
		{args: []string{"D123", "done", "--help"}, want: []string{"--submit"}},
		{args: []string{"D123", "verify", "--help"}, want: []string{"REPLY_ID:PARENT_ID", "--done"}},
	}
	for _, test := range tests {
		var output bytes.Buffer
		root := newRootCommand(test.args, strings.NewReader(""), &output, &output)
		root.SetArgs(test.args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}
		for _, expected := range test.want {
			if !strings.Contains(output.String(), expected) {
				t.Fatalf("%v help missing %q:\n%s", test.args, expected, output.String())
			}
		}
	}
}

func TestBatchFailureTextHandlesMissingDetails(t *testing.T) {
	got := ansi.Strip(renderBatch(batchResult{RevisionID: 12, State: "partial"}))
	if got != "Batch stopped after an unreported failure on D12." {
		t.Fatalf("text output = %q", got)
	}
}

func TestFirefoxHelpExplainsAutomaticDiscoveryAndProfileRestriction(t *testing.T) {
	var output bytes.Buffer
	root := newRootCommand([]string{"--help"}, strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Report Firefox discovery failures directly (discovery is automatic)",
		"Restrict Firefox cookie discovery to this profile",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("help missing %q:\n%s", expected, output.String())
		}
	}
}
