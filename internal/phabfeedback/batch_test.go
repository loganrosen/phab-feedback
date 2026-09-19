package phabfeedback

import (
	"encoding/json"
	"errors"
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
	if len(transport.requests) != 1 {
		t.Fatalf("dry-run made mutation requests: %d", len(transport.requests))
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
	if !result.Verified || !result.Replies[0].Linked || !result.Done[0].FinalDone {
		t.Fatalf("unexpected verification: %#v", result)
	}
}
