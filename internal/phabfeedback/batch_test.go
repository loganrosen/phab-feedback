package phabfeedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchManifestValidationIsStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch.json")
	if err := os.WriteFile(path, []byte(`{
  "revision": "D1",
  "actions": [{"comment_id": 20, "reply": "reply", "unexpected": true}]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBatchManifest(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}

	empty := ""
	for _, manifest := range []batchManifest{
		{Revision: "D1"},
		{Revision: "D1", Actions: []batchManifestAction{{CommentID: 20}}},
		{Revision: "D1", Actions: []batchManifestAction{{CommentID: 20, Reply: &empty}}},
		{Revision: "D1", Actions: []batchManifestAction{{CommentID: 20, Done: new(false)}}},
	} {
		if err := validateBatchManifest(manifest); err == nil {
			t.Fatalf("manifest passed validation: %#v", manifest)
		}
	}
}

func TestBatchValidatesEveryTargetBeforeMutation(t *testing.T) {
	reply := "reply"
	done := true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
	)
	_, err := service.batch(batchManifest{
		Revision: "D1",
		Actions: []batchManifestAction{
			{CommentID: 20, Reply: &reply},
			{CommentID: 21, Done: &done},
		},
	}, false, false)
	if err == nil || !strings.Contains(err.Error(), "comment 21 was not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("batch mutated before validation completed: %d requests", len(transport.requests))
	}
}

func TestBatchDryRunReportsPlanWithoutWebMutation(t *testing.T) {
	reply := "reply"
	done := true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "planned" || !result.DryRun || !result.Submit || len(result.Mutations) != 2 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	for _, mutation := range result.Mutations {
		if !mutation.Planned || mutation.Draft != nil || mutation.Published != nil || mutation.FinalDone != nil {
			t.Fatalf("dry-run reported observed state: %#v", mutation)
		}
	}
	if len(transport.requests) != 1 {
		t.Fatalf("dry-run made mutation requests: %d", len(transport.requests))
	}
}

func TestBatchDoneFailureReportsRecoveryAndSkipsSubmit(t *testing.T) {
	reply := "reply"
	done := true
	first := transaction(1, "inline", 20, map[string]any{"isDone": false})
	second := transaction(2, "inline", 21, map[string]any{"isDone": true})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{first, second}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{}}),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": true}}),
		errors.New("retry failed"),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions: []batchManifestAction{
			{CommentID: 20, Reply: &reply},
			{CommentID: 21, Done: &done},
		},
	}, true, false)
	if err == nil || result.State != "partial" || result.Failure == nil {
		t.Fatalf("unexpected batch failure: %#v %v", result, err)
	}
	if result.Failure.CompletedMutations != 1 || len(result.Mutations) != 2 ||
		!strings.Contains(result.Mutations[1].Recovery, "pending undo-Done draft") {
		t.Fatalf("unexpected partial details: %#v", result)
	}
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			t.Fatal("submitted after a mid-batch Done failure")
		}
	}
}

func TestBatchReplyFailureOmitsUnobservedState(t *testing.T) {
	reply := "reply"
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		errors.New("reply outcome unknown"),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply}},
	}, false, false)
	if err == nil || len(result.Mutations) != 1 {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	mutation := result.Mutations[0]
	if mutation.Draft != nil || mutation.Published != nil {
		t.Fatalf("unknown reply outcome reported definite state: %#v", mutation)
	}
}

func TestBatchDraftsInOrderAndSubmitsOnce(t *testing.T) {
	reply := "reply"
	done := true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "published" || len(result.Mutations) != 2 || result.Mutations[0].Action != "reply" || result.Mutations[1].Action != "done" {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	if transport.requests[2].form(t).Get("op") != "reply" ||
		transport.requests[3].form(t).Get("op") != "save" ||
		transport.requests[4].form(t).Get("op") != "done" {
		t.Fatalf("batch mutation order was not reply, save, Done")
	}
	submits := 0
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			submits++
		}
	}
	if submits != 1 {
		t.Fatalf("submission count = %d, want 1", submits)
	}
}

func TestReplyDoneDoesNotMarkDoneWhenReplyFails(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		errors.New("reply failed"),
	)
	_, err := service.reply("D1", "20", "reply", true, true)
	if err == nil {
		t.Fatal("expected reply failure")
	}
	for _, request := range transport.requests {
		if request.form(t).Get("op") == "done" {
			t.Fatal("Done mutation occurred after failed reply creation")
		}
	}
}

func TestReplyDoneFailureReportsDraftedReplyAndSkipsSubmit(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{}}),
		errors.New("Done failed"),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err == nil || result.CreatedReplyID != 55 || !result.Draft {
		t.Fatalf("unexpected partial result: %#v %v", result, err)
	}
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			t.Fatal("submitted after Done failure")
		}
	}
}

func TestReplyDoneSubmitsAfterBothDrafts(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "reply+done" || !result.Published || result.Draft || result.Done == nil ||
		!boolPointerValue(result.Done.Published) || result.Submission == nil {
		t.Fatalf("unexpected reply+Done result: %#v", result)
	}
	if transport.requests[2].form(t).Get("op") != "reply" ||
		transport.requests[3].form(t).Get("op") != "save" ||
		transport.requests[4].form(t).Get("op") != "done" ||
		!strings.Contains(transport.requests[5].target, "/differential/revision/edit/1/comment/") {
		t.Fatal("reply, Done, and submission ordering was not preserved")
	}
}

func TestDoneSubmitPublishesOnce(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Submission == nil || !result.Submission.Submitted || boolPointerValue(result.Comments[0].Draft) || !boolPointerValue(result.Comments[0].Published) {
		t.Fatalf("unexpected Done result: %#v", result)
	}
	submits := 0
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			submits++
		}
	}
	if submits != 1 {
		t.Fatalf("submission count = %d, want 1", submits)
	}
}

func TestStructuredMutationJSONContainsWorkflowFields(t *testing.T) {
	finalDone := true
	payload, err := json.Marshal(inlineReplyResult{
		RevisionID: 1, Action: "reply+done", ParentCommentID: 20,
		CreatedReplyID: 55, Draft: false, Published: true, FinalDone: &finalDone,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, field := range []string{
		`"revision_id":1`, `"action":"reply+done"`, `"created_reply_id":55`,
		`"parent_comment_id":20`, `"draft":false`, `"published":true`, `"final_done":true`,
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("JSON missing %s: %s", field, text)
		}
	}
}

func TestVerifyChecksReplyParentAndDoneState(t *testing.T) {
	root := transaction(1, "inline", 20, map[string]any{"isDone": true})
	reply := transaction(2, "inline", 55, map[string]any{
		"isDone": false, "replyToCommentPHID": "PHID-CMT-20",
	})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{root, reply}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.verify("D1", []replyExpectation{{ReplyID: 55, ParentID: 20}}, []string{"20"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !result.Replies[0].Linked || !result.Done[0].ConduitIsDone ||
		!result.Done[0].Ambiguous || len(result.Limitations) != 1 {
		t.Fatalf("unexpected verification: %#v", result)
	}
}

func TestVerifyReportsMissingReplyWrongParentAndUncheckedDone(t *testing.T) {
	root := transaction(1, "inline", 20, map[string]any{"isDone": false})
	otherParent := transaction(2, "inline", 21, map[string]any{"isDone": false})
	reply := transaction(3, "inline", 55, map[string]any{
		"isDone": false, "replyToCommentPHID": "PHID-CMT-21",
	})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{root, otherParent, reply}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.verify(
		"D1",
		[]replyExpectation{{ReplyID: 55, ParentID: 20}, {ReplyID: 99, ParentID: 20}},
		[]string{"20"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified || result.Replies[0].Linked || result.Replies[1].Found || result.Done[0].ConduitIsDone {
		t.Fatalf("unexpected verification: %#v", result)
	}
}

func TestVerifyCLIExitsNonzeroAndPrintsStructuredFailure(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/transaction.search" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(conduitResult(map[string]any{
			"data": []any{inline}, "cursor": map[string]any{"after": nil},
		}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("PHAB_FEEDBACK_HOST", server.URL)
	t.Setenv("PHAB_FEEDBACK_TOKEN", "token")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(t.TempDir(), "missing-arcrc"))

	var stdout, stderr bytes.Buffer
	status := Run(
		[]string{"D1", "verify", "--done", "20", "--format", "json"},
		strings.NewReader(""), &stdout, &stderr,
	)
	if status != 1 {
		t.Fatalf("status = %d, want 1", status)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	body, err := io.ReadAll(&stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"verified": false`) ||
		!strings.Contains(string(body), `"conduit_is_done": false`) {
		t.Fatalf("unexpected output: %s", body)
	}
}
