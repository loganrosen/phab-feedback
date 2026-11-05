package phabfeedback

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func mentionTransaction(id int, operation string) map[string]any {
	return map[string]any{
		"id": id, "phid": "PHID-XACT-mention", "type": "mentioned-in",
		"authorPHID": "PHID-USER-author", "dateCreated": 115,
		"comments": []any{}, "fields": map[string]any{
			"operations": []any{map[string]any{"operation": operation, "phid": "PHID-TASK-source"}},
		},
	}
}

func TestTimelineMentionsAreNotComments(t *testing.T) {
	mention := mentionTransaction(3, "add")
	outgoing := mentionTransaction(4, "add")
	outgoing["type"] = "mention"
	unknown := mentionTransaction(5, "add")
	unknown["type"] = nil
	result := buildTimeline(1, map[string]any{"phid": "PHID-DIFF-current"}, []map[string]any{
		transaction(1, "comment", 10, nil), mention,
		transaction(2, "inline", 20, nil), outgoing, unknown,
	})
	if len(result.Events) != 3 || len(result.GeneralComments) != 1 || len(result.InlineComments) != 1 {
		t.Fatalf("mentions changed comment counts: %#v", result)
	}
	event := result.Events[1]
	if event.Kind != "mention" || event.TransactionID != 3 || event.ID != nil ||
		event.Author.PHID != "PHID-USER-author" || event.Object.PHID != "PHID-TASK-source" ||
		event.Created != timestamp(115) || event.Operation != "add" {
		t.Fatalf("incorrect mention: %#v", event)
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "phid", "is_done", "reply_to_comment_id"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("mention has comment field %q: %s", key, body)
		}
	}
	for _, key := range []string{"author", "object", "operation", "transaction_id", "transaction_phid", "created"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("mention missing %q: %s", key, body)
		}
	}
	grouped := groupThreads(result.InlineComments)
	if len(grouped.Threads) != 1 || len(grouped.OrphanReplies) != 0 {
		t.Fatalf("mention changed threads: %#v", grouped)
	}
}

func TestTimelineHydratesMentionHandles(t *testing.T) {
	for _, operation := range []string{"add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			mention := mentionTransaction(3, operation)
			mention["comments"] = transaction(3, "comment", 40, nil)["comments"]
			service, transport := serviceWith(
				conduitResult(map[string]any{"data": []any{revision(1)}}),
				conduitResult(map[string]any{"data": []any{map[string]any{"id": 4, "phid": "PHID-DIFF-current"}}}),
				conduitResult(map[string]any{"data": []any{mention}}),
				conduitResult(map[string]any{
					"PHID-USER-author": map[string]any{"name": "reviewer"},
					"PHID-TASK-source": map[string]any{
						"name": "T16909", "fullName": "T16909: Update selected columns",
						"uri": "https://phab.example/T16909",
					},
				}),
			)
			result, err := service.timeline("D1")
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]any{}
			if err := json.Unmarshal([]byte(transport.requests[3].form(t).Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			phids, _ := sliceValue(params["phids"])
			if len(phids) != 2 || phids[0] != "PHID-TASK-source" || phids[1] != "PHID-USER-author" {
				t.Fatalf("unexpected handle query: %#v", params)
			}
			text := ansi.Strip(renderTimeline(result))
			action := "mentioned this in"
			if operation == "remove" {
				action = "removed mention from"
			}
			for _, expected := range []string{
				"transaction #3", "reviewer " + action, "T16909: Update selected columns",
				"https://phab.example/T16909", stringValue(timestamp(115)), "comment 40",
			} {
				if !strings.Contains(text, expected) {
					t.Fatalf("missing %q: %s", expected, text)
				}
			}
			if len(result.GeneralComments) != 0 || len(result.InlineComments) != 0 {
				t.Fatalf("mention counted as comment: %#v", result)
			}
		})
	}
}

func TestMentionHandleFailuresAndMissingHandles(t *testing.T) {
	service, _ := serviceWith(errors.New("lookup failed"))
	result := buildTimeline(1, nil, []map[string]any{mentionTransaction(3, "add")})
	if err := service.hydrateMentionHandles(&result); err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("lost lookup error: %v", err)
	}
	service, _ = serviceWith(conduitResult(map[string]any{}))
	if err := service.hydrateMentionHandles(&result); err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(renderTimeline(result))
	if !strings.Contains(text, "PHID-USER-author") || !strings.Contains(text, "PHID-TASK-source") {
		t.Fatalf("missing inaccessible handle identifiers: %s", text)
	}
	result.Events[0].Author.Name = "unsafe\x1b]52;c;clipboard\x07"
	result.Events[0].Object.FullName = "title\nnext"
	text = ansi.Strip(renderTimeline(result))
	if strings.ContainsRune(text, '\x07') || !strings.Contains(text, `title\nnext`) {
		t.Fatalf("unsafe mention output: %q", text)
	}
}

func TestMentionTransactionsDoNotRequireHandlesForThreads(t *testing.T) {
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{revision(1)}}),
		conduitResult(map[string]any{"data": []any{map[string]any{"id": 4, "phid": "PHID-DIFF-current"}}}),
		conduitResult(map[string]any{"data": []any{mentionTransaction(3, "add")}}),
	)
	result, err := service.threads("D1", "all", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 0 || len(result.OrphanReplies) != 0 || len(transport.requests) != 3 {
		t.Fatalf("mention affected threads: %#v (%d requests)", result, len(transport.requests))
	}
}

func TestMentionTransactionPreservesMultipleOperations(t *testing.T) {
	item := mentionTransaction(3, "add")
	fields, _ := mapValue(item["fields"])
	fields["operations"] = []any{
		map[string]any{"operation": "add", "phid": "PHID-TASK-first"},
		map[string]any{"operation": "remove", "phid": "PHID-TASK-second"},
	}
	result := buildTimeline(1, nil, []map[string]any{item})
	if len(result.Events) != 2 || result.Events[0].Object.PHID != "PHID-TASK-first" ||
		result.Events[1].Object.PHID != "PHID-TASK-second" || result.Events[1].Operation != "remove" {
		t.Fatalf("lost mention operations: %#v", result.Events)
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
