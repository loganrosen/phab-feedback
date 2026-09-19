package phabfeedback

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

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
	if len(result.Threads[0].Replies) != 2 {
		t.Fatalf("expected nested replies, got %#v", result.Threads[0].Replies)
	}
	if len(result.OrphanReplies) != 1 {
		t.Fatalf("expected orphan reply, got %#v", result.OrphanReplies)
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
	grouped := groupThreads(timeline.InlineComments)
	if grouped.Threads[0].Root.ID != 10 || grouped.Threads[1].Root.ID != 11 {
		t.Fatalf("unstable order: %#v", grouped.Threads)
	}
}

func TestFeedbackEventJSONPreservesInlineShape(t *testing.T) {
	body, err := json.Marshal(feedbackEvent{
		Kind: "inline", ID: 10, PHID: "PHID-CMT-10",
		TransactionID: 1, TransactionPHID: "PHID-XACT-1",
		Created: "2026-09-19T00:00:00+00:00", Content: "comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"diff_id", "diff_phid", "on_current_diff", "path", "line", "is_done",
		"reply_to_comment_id", "reply_to_comment_phid",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("inline JSON omitted %q: %s", key, body)
		}
	}
}

func TestTypedResultsPreserveEmptyJSONArrays(t *testing.T) {
	timeline := buildTimeline(1, map[string]any{
		"id": 2, "phid": "PHID-DIFF-current", "fields": map[string]any{},
	}, nil)
	body, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"events":[]`) ||
		!strings.Contains(string(body), `"general_comments":[]`) ||
		!strings.Contains(string(body), `"inline_comments":[]`) {
		t.Fatalf("timeline contains null collections: %s", body)
	}

	grouped := groupThreads([]feedbackEvent{{
		Kind: "inline", ID: 10, PHID: "PHID-CMT-10", OnCurrentDiff: true,
	}})
	result := threadsResult{Threads: grouped.Threads, OrphanReplies: grouped.OrphanReplies}
	body, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"replies":[]`) || !strings.Contains(string(body), `"orphan_replies":[]`) {
		t.Fatalf("threads contain null collections: %s", body)
	}
}

func TestReviewerJSONPreservesMissingHandleShape(t *testing.T) {
	body, err := json.Marshal(reviewer{ReviewerPHID: nil, Decision: "accepted", IsBlocking: false})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"phid", "name", "full_name", "type", "type_name", "uri"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("reviewer JSON unexpectedly added %q: %s", key, body)
		}
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
