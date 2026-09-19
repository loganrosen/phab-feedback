package phabfeedback

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

type recordedRequest struct {
	method, target string
	headers        http.Header
	data           []byte
}

func (r recordedRequest) form(t *testing.T) url.Values {
	t.Helper()
	values, err := url.ParseQuery(string(r.data))
	if err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return values
}

type fakeTransport struct {
	responses []any
	requests  []recordedRequest
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f *fakeTransport) Request(method, target string, headers http.Header, data io.Reader) ([]byte, error) {
	var body []byte
	if data != nil {
		body, _ = io.ReadAll(data)
	}
	f.requests = append(f.requests, recordedRequest{method, target, headers.Clone(), body})
	if len(f.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	if err, ok := response.(error); ok {
		return nil, err
	}
	responseBody, ok := response.([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T", response)
	}
	return responseBody, nil
}

func response(payload any) []byte {
	var body []byte
	if raw, ok := payload.([]byte); ok {
		body = raw
	} else {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			panic(fmt.Sprintf("marshal test response: %v", err))
		}
	}
	return body
}

func conduitResult(result any) []byte {
	return response(map[string]any{"result": result, "error_code": nil, "error_info": nil})
}

func transaction(transactionID int, kind string, comment int, fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	return map[string]any{
		"id": transactionID, "phid": "PHID-XACT-" + strconv.Itoa(transactionID), "type": kind, "fields": fields,
		"comments": []any{map[string]any{
			"id": comment, "phid": "PHID-CMT-" + strconv.Itoa(comment), "version": 1,
			"removed": false, "dateCreated": 100 + comment, "content": map[string]any{"raw": "comment " + strconv.Itoa(comment)},
		}},
	}
}

func revision(revisionID int) map[string]any {
	return map[string]any{
		"id": revisionID, "phid": "PHID-DREV-" + strconv.Itoa(revisionID),
		"fields": map[string]any{
			"title": "Revision " + strconv.Itoa(revisionID), "uri": "https://phab.example/D" + strconv.Itoa(revisionID),
			"authorPHID": "PHID-USER-author", "repositoryPHID": "PHID-REPO-main", "diffPHID": "PHID-DIFF-current",
			"status": map[string]any{"value": "needs-review", "name": "Needs Review"}, "isDraft": false,
			"dateCreated": 100, "dateModified": 200,
		},
		"attachments": map[string]any{"reviewers": map[string]any{"reviewers": []any{map[string]any{
			"reviewerPHID": "PHID-USER-reviewer", "actorPHID": "PHID-USER-reviewer", "status": "accepted", "isBlocking": true,
		}}}},
	}
}

func serviceWith(responses ...any) (*feedbackService, *fakeTransport) {
	transport := &fakeTransport{responses: responses}
	return &feedbackService{
		conduit: &conduitClient{host: "https://phab.example", token: "token", transport: transport},
		web:     &webClient{host: "https://phab.example", cookie: "phsid=cookie", transport: transport},
	}, transport
}

func TestHTTPTransportPropagatesContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "expected")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if value := request.Context().Value(contextKey{}); value != "expected" {
			t.Fatalf("unexpected request context value: %v", value)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})}
	body, err := (httpTransport{client: client}).Request(ctx, http.MethodGet, "https://phab.example", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected response: %q", body)
	}
}

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

func TestTimelineGroupsNestedRepliesAndOrphans(t *testing.T) {
	root := transaction(1, "inline", 10, map[string]any{"diff": map[string]any{"id": 4, "phid": "PHID-DIFF-current"}, "path": "a.go", "line": 3, "isDone": false})
	reply := transaction(2, "inline", 11, map[string]any{"diff": map[string]any{"id": 4, "phid": "PHID-DIFF-current"}, "replyToCommentPHID": "PHID-CMT-10"})
	nested := transaction(3, "inline", 12, map[string]any{"diff": map[string]any{"id": 4, "phid": "PHID-DIFF-current"}, "replyToCommentPHID": "PHID-CMT-11"})
	orphan := transaction(4, "inline", 13, map[string]any{"diff": map[string]any{"id": 3, "phid": "PHID-DIFF-old"}, "replyToCommentPHID": "PHID-CMT-missing"})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{map[string]any{"fields": map[string]any{"diffPHID": "PHID-DIFF-current"}}}}),
		conduitResult(map[string]any{"data": []any{map[string]any{"id": 4, "phid": "PHID-DIFF-current", "fields": map[string]any{"dateCreated": 100}}}}),
		conduitResult(map[string]any{"data": []any{nested, orphan, root, reply}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.threads("D9", "all", false)
	if err != nil {
		t.Fatal(err)
	}
	threads, _ := sliceValue(result["threads"])
	thread, _ := mapValue(threads[0])
	replies, _ := sliceValue(thread["replies"])
	if len(replies) != 2 {
		t.Fatalf("expected nested replies, got %#v", replies)
	}
	orphans, _ := sliceValue(result["orphan_replies"])
	if len(orphans) != 1 {
		t.Fatalf("expected orphan reply, got %#v", orphans)
	}
}

func TestThreadOrderingIsStableForEqualTimestamps(t *testing.T) {
	first := transaction(1, "inline", 10, map[string]any{"diff": map[string]any{"phid": "PHID-DIFF-current"}})
	second := transaction(2, "inline", 11, map[string]any{"diff": map[string]any{"phid": "PHID-DIFF-current"}})
	firstComment := activeComment(first)
	secondComment := activeComment(second)
	firstComment["dateCreated"] = 100
	secondComment["dateCreated"] = 100
	timeline := buildTimeline(1, map[string]any{"phid": "PHID-DIFF-current", "fields": map[string]any{}}, []map[string]any{first, second})
	inline, _ := objectSlice(timeline["inline_comments"], "timeline")
	grouped := groupThreads(inline)
	threads, _ := sliceValue(grouped["threads"])
	firstThread, _ := mapValue(threads[0])
	secondThread, _ := mapValue(threads[1])
	firstRoot, _ := mapValue(firstThread["root"])
	secondRoot, _ := mapValue(secondThread["root"])
	if firstRoot["id"] != 10 || secondRoot["id"] != 11 {
		t.Fatalf("unstable order: %#v", threads)
	}
}

func TestListFallbackHydrationAndTextEscaping(t *testing.T) {
	item := revision(17)
	fields, _ := mapValue(item["fields"])
	fields["title"] = "unsafe\x1b]52;c;clipboard\x07\nnext"
	service, transport := serviceWith(
		conduitResult(map[string]any{"phid": "PHID-USER-viewer", "userName": "viewer", "realName": "Viewer"}),
		newNetworkError(406, "HTTP 406"),
		conduitResult(map[string]any{"data": []any{item}, "cursor": map[string]any{"after": "next-page"}}),
		conduitResult(map[string]any{
			"PHID-USER-author":   map[string]any{"fullName": "Author"},
			"PHID-USER-reviewer": map[string]any{"fullName": "Reviewer"},
			"PHID-REPO-main":     map[string]any{"fullName": "Repository"},
		}),
	)
	result, err := service.listRevisions("reviewing", "open", nil, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{}
	if err := json.Unmarshal([]byte(transport.requests[2].form(t).Get("params")), &params); err != nil {
		t.Fatal(err)
	}
	constraints, _ := mapValue(params["constraints"])
	statuses, _ := sliceValue(constraints["statuses"])
	if len(statuses) != 5 {
		t.Fatalf("expected fallback statuses, got %#v", statuses)
	}
	text := ansi.Strip(revisionList(result))
	if strings.ContainsRune(text, '\x07') || !strings.Contains(text, `unsafe\x1b]52;c;clipboard\x07\nnext`) {
		t.Fatalf("unsafe text output: %q", text)
	}
}

func TestReplyDoneAndSubmissionPayloads(t *testing.T) {
	inline := transaction(1, "inline", 20, nil)
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
	)
	reply, err := service.draftInlineReply("D1", "20", "A reply")
	if err != nil {
		t.Fatal(err)
	}
	if reply["draft_comment_id"] != 55 {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	if transport.requests[2].form(t).Get("replyToCommentPHID") != "PHID-CMT-20" {
		t.Fatal("reply did not preserve parent PHID")
	}
	submitted, err := service.submit("D1")
	if err != nil || !boolValue(submitted["submitted"]) {
		t.Fatalf("submit: %#v %v", submitted, err)
	}
	if transport.requests[4].form(t).Get("editengine.actions") != "[]" {
		t.Fatal("missing editengine actions")
	}
}

func TestConduitErrorsRedactToken(t *testing.T) {
	transport := &fakeTransport{responses: []any{response(map[string]any{
		"result": nil, "error_code": "ERR-FAIL", "error_info": "very-secret-token was rejected",
	})}}
	client := conduitClient{host: "https://phab.example", token: "very-secret-token", transport: transport}
	var result any
	err := client.call("transaction.search", map[string]any{}, &result)
	if err == nil || strings.Contains(err.Error(), "very-secret-token") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestConduitPreservesLargeNumbers(t *testing.T) {
	transport := &fakeTransport{responses: []any{response(map[string]any{
		"result": map[string]any{
			"data":   []map[string]any{{"id": 7654321}},
			"cursor": map[string]any{"after": nil},
		},
		"error_code": nil,
		"error_info": nil,
	})}}
	client := conduitClient{host: "https://phab.example", token: "api-token", transport: transport}
	result, err := client.search("transaction.search", map[string]any{}, nil, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := stringValue(result.Data[0]["id"]); got != "7654321" {
		t.Fatalf("large Conduit number = %q, want 7654321", got)
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
	got := ansi.Strip(revisionList(map[string]any{
		"count":  1,
		"role":   "responsible",
		"status": "open",
		"revisions": []any{map[string]any{
			"id":     123,
			"title":  "Improve command output",
			"status": map[string]any{"value": "needs-review", "name": "Needs Review"},
			"author": map[string]any{"full_name": "Logan Rosen"},
			"reviewers": []any{map[string]any{
				"full_name": "Reviewer", "status": "accepted",
			}},
			"modified": "2026-09-19T02:35:58+00:00",
			"uri":      "https://phab.example/D123",
			"merge_conflict_status": map[string]any{
				"status": "conflict",
				"reason": "The landing conflicts with the target branch.",
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
	status, _ := mapValue(normalized["merge_conflict_status"])
	if status["status"] != "conflict" || status["reason"] != "Landing may fail." {
		t.Fatalf("unexpected merge conflict status: %#v", status)
	}
	if status["checked_at"] != "2025-09-04T15:33:20+00:00" {
		t.Fatalf("checked_at = %#v", status["checked_at"])
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
		result  map[string]any
		want    string
	}{
		{
			command: "comment",
			result:  map[string]any{"revision_id": 12},
			want:    "Posted a comment on D12.",
		},
		{
			command: "reply",
			result: map[string]any{
				"revision_id": 12, "parent_comment_id": 34, "draft_comment_id": 56,
				"submission": map[string]any{"revision_id": 12},
			},
			want: "Drafted inline reply #56 to comment #34 on D12.\nSubmitted pending drafts on D12.",
		},
		{
			command: "done",
			result: map[string]any{
				"revision_id": 12,
				"comments": []any{
					map[string]any{"comment_id": 34},
					map[string]any{"comment_id": 35},
				},
			},
			want: "Marked #34, #35 Done as drafts on D12.",
		},
		{
			command: "ai-review",
			result:  map[string]any{"revision_id": 12, "status": "already-in-progress"},
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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
