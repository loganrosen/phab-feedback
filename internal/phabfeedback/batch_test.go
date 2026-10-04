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
	"strconv"
	"strings"
	"testing"
)

func sameBoolPointerValue(got, want *bool) bool {
	return (got == nil && want == nil) ||
		(got != nil && want != nil && *got == *want)
}

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
	for index, mutation := range result.Mutations {
		if !mutation.Planned || mutation.Draft != nil || mutation.Published != nil || mutation.FinalDone != nil {
			t.Fatalf("dry-run reported observed state: %#v", mutation)
		}
		if mutation.ActionIndex != 1 || mutation.MutationIndex != index+1 {
			t.Fatalf("unexpected mutation indexes: %#v", mutation)
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
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
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
		result.Failure.ActionIndex != 2 || result.Failure.MutationIndex != 2 ||
		result.Mutations[1].Draft != nil || result.Mutations[1].Published != nil ||
		result.Mutations[1].ObservedChecked == nil || *result.Mutations[1].ObservedChecked ||
		result.Mutations[1].ObservedDraftState == nil || !*result.Mutations[1].ObservedDraftState ||
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
		conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(2, "inline", 20, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "published" || result.Submission == nil || !result.Submission.Attempted ||
		len(result.Mutations) != 2 || result.Mutations[0].Action != "reply" ||
		result.Mutations[1].Action != "done" ||
		result.Submission.DoneVerification == nil ||
		result.Submission.DoneVerification.Status != "observed" ||
		!result.Submission.DoneVerification.DoneStateAmbiguous ||
		result.Mutations[1].Published != nil ||
		!boolPointerValue(result.Mutations[1].FinalDone) {
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

func TestBatchAcceptedSubmissionPublishesRepliesBeforeDoneVerificationFailure(t *testing.T) {
	reply := "reply"
	done := true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	tests := []struct {
		name               string
		verificationResult any
		wantFinalDone      *bool
	}{
		{
			name: "target unresolved",
			verificationResult: conduitResult(map[string]any{"data": []any{
				transaction(2, "inline", 20, map[string]any{"isDone": false}),
			}, "cursor": map[string]any{"after": nil}}),
			wantFinalDone: new(false),
		},
		{
			name:               "verification read failed",
			verificationResult: errors.New("Conduit unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := serviceWith(
				conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
				response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
				test.verificationResult,
			)
			result, err := service.batch(batchManifest{
				Revision: "D1",
				Actions: []batchManifestAction{
					{CommentID: 20, Reply: &reply, Done: &done},
				},
			}, true, false)
			if err == nil || result.State != "partial" ||
				len(result.Mutations) != 2 ||
				!strings.Contains(result.Mutations[0].Action, "reply") ||
				!boolPointerValue(result.Mutations[0].Published) ||
				boolPointerValue(result.Mutations[0].Draft) {
				t.Fatalf("accepted reply was not reported as published: %#v %v", result, err)
			}
			doneMutation := result.Mutations[1]
			if doneMutation.Action != "done" || doneMutation.Published != nil ||
				doneMutation.Draft != nil || !sameBoolPointerValue(doneMutation.FinalDone, test.wantFinalDone) {
				t.Fatalf("unexpected Done result after verification failure: %#v", doneMutation)
			}
		})
	}
}

func TestBatchMixedActionsReportsUnresolvedDoneAfterAcceptedSubmission(t *testing.T) {
	reply20, reply21, done := "reply 20", "reply 21", true
	initial := []any{
		transaction(1, "inline", 20, map[string]any{"isDone": false}),
		transaction(2, "inline", 21, map[string]any{"isDone": false}),
		transaction(3, "inline", 22, map[string]any{"isDone": false}),
		publishedReply(90, 20),
		publishedReply(91, 21),
	}
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": initial, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 56}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 56}}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(4, "inline", 20, map[string]any{"isDone": false}),
			transaction(5, "inline", 21, map[string]any{"isDone": true}),
			transaction(6, "inline", 22, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions: []batchManifestAction{
			{CommentID: 20, Reply: &reply20, Done: &done},
			{CommentID: 21, Reply: &reply21, Done: &done},
			{CommentID: 22, Done: &done},
		},
	}, true, false)
	if err == nil || !strings.Contains(err.Error(), "#20") ||
		result.State != "partial" || result.Failure == nil || result.Failure.Action != "submit" ||
		result.Submission == nil || !result.Submission.Submitted ||
		result.Submission.DoneVerification == nil ||
		result.Submission.DoneVerification.Status != "failed" ||
		!strings.Contains(result.Submission.Recovery, "Done is a toggle") ||
		len(result.Mutations) != 5 {
		t.Fatalf("unresolved Done state was not reported as a partial failure: %#v %v", result, err)
	}
	if !boolPointerValue(result.Mutations[0].Published) ||
		!sameBoolPointerValue(result.Mutations[1].FinalDone, new(false)) ||
		!boolPointerValue(result.Mutations[2].Published) ||
		!sameBoolPointerValue(result.Mutations[3].FinalDone, new(true)) ||
		!sameBoolPointerValue(result.Mutations[4].FinalDone, new(true)) {
		t.Fatalf("unexpected per-mutation states after submission: %#v", result.Mutations)
	}
	submits := 0
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			submits++
		}
	}
	if submits != 1 {
		t.Fatalf("submission count = %d, want one revision-wide submission", submits)
	}
}

func TestBatchSubmissionDialogPreservesDraftMutations(t *testing.T) {
	reply := "reply"
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"dialog": "An inline comment is still being edited."}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply}},
	}, true, false)
	if err == nil || result.State != "partial" || result.Submission == nil ||
		result.Submission.Submitted || result.Submission.Outcome != "rejected" ||
		len(result.Mutations) != 1 || !boolPointerValue(result.Mutations[0].Draft) ||
		boolPointerValue(result.Mutations[0].Published) {
		t.Fatalf("unexpected batch result: %#v %v", result, err)
	}
}

func TestBatchSkipsSubmissionWhenNoDraftWasCreated(t *testing.T) {
	done := true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": true})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": false}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Done: &done}},
	}, true, false)
	if err != nil || result.State != "unchanged" || result.Submission == nil ||
		result.Submission.Attempted || result.Submission.Outcome != "not-attempted" ||
		result.Submission.Submitted {
		t.Fatalf("unexpected batch result: %#v %v", result, err)
	}
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			t.Fatal("submitted even though the batch created no drafts")
		}
	}
}

func TestReplyDoneDoesNotMarkDoneWhenReplyFails(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		errors.New("reply failed"),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err == nil {
		t.Fatal("expected reply failure")
	}
	if result.Submission == nil ||
		result.Submission.Outcome != submissionOutcomeNotAttempted ||
		!strings.Contains(result.Submission.Recovery, "reply draft was not created") {
		t.Fatalf("missing submission result: %#v", result)
	}
	for _, request := range transport.requests {
		if request.form(t).Get("op") == "done" {
			t.Fatal("Done mutation occurred after failed reply creation")
		}
	}
}

func TestReplySubmitFailureEmbedsUpdatedStructuredResult(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		errors.New("reply failed"),
	)
	result, err := service.reply("D1", "20", "reply", false, true)
	if err == nil || result.Submission == nil {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	var resultError interface{ commandResult() any }
	if !errors.As(err, &resultError) {
		t.Fatalf("error does not contain a command result: %v", err)
	}
	reported, ok := resultError.commandResult().(inlineReplyResult)
	if !ok || reported.Submission == nil ||
		reported.Submission.Outcome != submissionOutcomeNotAttempted {
		t.Fatalf("stale command result: %#v", resultError.commandResult())
	}
}

func TestReplyDoneFailureReportsDraftedReplyAndSkipsSubmit(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		errors.New("Done failed"),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err == nil || result.CreatedReplyID != 55 || !result.Draft {
		t.Fatalf("unexpected partial result: %#v %v", result, err)
	}
	if result.Submission == nil ||
		result.Submission.Outcome != submissionOutcomeNotAttempted ||
		!strings.Contains(result.Submission.Recovery, "parent Done action failed") {
		t.Fatalf("missing submission status: %#v %v", result, err)
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
		conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(2, "inline", 20, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "reply+done" || !result.Published || result.Draft || result.Done == nil ||
		result.Done.Published != nil || !boolPointerValue(result.Done.FinalDone) ||
		result.Submission == nil || result.Submission.DoneVerification == nil ||
		result.Submission.DoneVerification.Status != "observed" ||
		!result.Submission.DoneVerification.DoneStateAmbiguous {
		t.Fatalf("unexpected reply+Done result: %#v", result)
	}
	if transport.requests[2].form(t).Get("op") != "reply" ||
		transport.requests[3].form(t).Get("op") != "save" ||
		transport.requests[4].form(t).Get("op") != "done" ||
		!strings.Contains(transport.requests[5].target, "/differential/revision/edit/1/comment/") {
		t.Fatal("reply, Done, and submission ordering was not preserved")
	}
}

func TestReplyDoneSubmitReconcilesTopLevelFinalDone(t *testing.T) {
	tests := []struct {
		name               string
		verificationResult any
		wantFinalDone      *bool
	}{
		{
			name: "target unresolved",
			verificationResult: conduitResult(map[string]any{"data": []any{
				transaction(2, "inline", 20, map[string]any{"isDone": false}),
			}, "cursor": map[string]any{"after": nil}}),
			wantFinalDone: new(false),
		},
		{
			name:               "verification read failed",
			verificationResult: errors.New("Conduit unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
			service, _ := serviceWith(
				conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
				response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
				test.verificationResult,
			)
			result, err := service.reply("D1", "20", "reply", true, true)
			if err == nil || result.Submission == nil || !result.Submission.Submitted ||
				result.Done == nil || !sameBoolPointerValue(result.FinalDone, test.wantFinalDone) ||
				!sameBoolPointerValue(result.Done.FinalDone, test.wantFinalDone) {
				t.Fatalf("top-level and nested Done results disagree: %#v %v", result, err)
			}
		})
	}
}

func TestReplyDoneAcceptedSubmissionPublishesReplyDespiteDoneVerificationFailure(t *testing.T) {
	tests := []struct {
		name               string
		verificationResult any
		wantFinalDone      *bool
	}{
		{
			name: "target unresolved",
			verificationResult: conduitResult(map[string]any{"data": []any{
				transaction(2, "inline", 20, map[string]any{"isDone": false}),
			}, "cursor": map[string]any{"after": nil}}),
			wantFinalDone: new(false),
		},
		{
			name:               "verification read failed",
			verificationResult: errors.New("Conduit unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
			service, _ := serviceWith(
				conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
				response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
				test.verificationResult,
			)
			result, err := service.reply("D1", "20", "reply", true, true)
			if err == nil || result.Submission == nil || !result.Submission.Submitted ||
				!result.Published || result.Draft ||
				!sameBoolPointerValue(result.FinalDone, test.wantFinalDone) {
				t.Fatalf("accepted reply was not reported as published: %#v %v", result, err)
			}
		})
	}
}

func TestDoneSubmitPublishesOnce(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(2, "inline", 20, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Submission == nil || !result.Submission.Attempted || !result.Submission.Submitted ||
		result.Submission.DoneVerification == nil ||
		result.Submission.DoneVerification.Status != "observed" ||
		!result.Submission.DoneVerification.DoneStateAmbiguous ||
		result.Comments[0].Draft != nil || result.Comments[0].Published != nil ||
		!boolPointerValue(result.Comments[0].FinalDone) {
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

func TestDoneSubmitRejectsRedirectWhenTargetRemainsUnresolved(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(2, "inline", 20, map[string]any{"isDone": false}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err == nil || !strings.Contains(err.Error(), "Done state is not visible for #20") ||
		result.Submission == nil || !result.Submission.Submitted ||
		result.Submission.DoneVerification == nil ||
		result.Submission.DoneVerification.Status != "failed" ||
		boolPointerValue(result.Comments[0].FinalDone) ||
		result.Comments[0].Published != nil {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}

func TestDoneSubmitReportsVerificationFailureAfterAcceptedSubmission(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		errors.New("Conduit unavailable"),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err == nil || !strings.Contains(err.Error(), "Done outcome verification failed") ||
		result.Submission == nil || !result.Submission.Submitted ||
		result.Submission.DoneVerification != nil ||
		result.Submission.DoneVerificationNote == "" ||
		result.Comments[0].FinalDone != nil ||
		result.Comments[0].Published != nil {
		t.Fatalf("unexpected result: %#v %v", result, err)
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
	if !result.ChecksPassed || result.Status != "observed" ||
		!result.Replies[0].Linked || !result.Done[0].ConduitIsDone ||
		result.Done[0].State != "done-or-pending-undo" ||
		!result.DoneStateAmbiguous || len(result.Limitations) != 1 {
		t.Fatalf("unexpected verification: %#v", result)
	}
}

func TestVerifyReplyOnlyIsDefinitive(t *testing.T) {
	root := transaction(1, "inline", 20, map[string]any{"isDone": false})
	reply := transaction(2, "inline", 55, map[string]any{
		"isDone": false, "replyToCommentPHID": "PHID-CMT-20",
	})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{root, reply}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.verify("D1", []replyExpectation{{ReplyID: 55, ParentID: 20}}, nil)
	if err != nil || !result.ChecksPassed || result.Status != "verified" || result.DoneStateAmbiguous {
		t.Fatalf("unexpected verification: %#v %v", result, err)
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
	if result.ChecksPassed || result.Status != "failed" ||
		result.Replies[0].Linked || result.Replies[1].Found || result.Done[0].ConduitIsDone ||
		result.Done[0].State != "not-done" {
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
	if !strings.Contains(string(body), `"status": "failed"`) ||
		!strings.Contains(string(body), `"checks_passed": false`) ||
		!strings.Contains(string(body), `"conduit_is_done": false`) {
		t.Fatalf("unexpected output: %s", body)
	}
}

func publishedReply(comment, parent int) map[string]any {
	return transaction(comment, "inline", comment, map[string]any{
		"isDone": false, "replyToCommentPHID": "PHID-CMT-" + strconv.Itoa(parent),
	})
}

func submissionRequestIndexes(t *testing.T, transport *fakeTransport) []int {
	t.Helper()
	indexes := make([]int, 0)
	for index, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func TestBatchPublishesFirstRepliesBeforeDone(t *testing.T) {
	reply20, reply21, done := "reply 20", "reply 21", true
	initial := []any{
		transaction(1, "inline", 20, map[string]any{"isDone": false}),
		transaction(2, "inline", 21, map[string]any{"isDone": false}),
		transaction(3, "inline", 22, map[string]any{"isDone": false}),
	}
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": initial, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 56}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 56}}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(4, "inline", 20, map[string]any{"isDone": true}),
			transaction(5, "inline", 21, map[string]any{"isDone": true}),
			transaction(6, "inline", 22, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions: []batchManifestAction{
			{CommentID: 20, Reply: &reply20, Done: &done},
			{CommentID: 21, Reply: &reply21, Done: &done},
			{CommentID: 22, Done: &done},
		},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TwoPhase || result.State != "published" ||
		result.ReplySubmission == nil || !result.ReplySubmission.Submitted ||
		result.Submission == nil || result.Submission.DoneVerification == nil ||
		len(result.Submission.DoneVerification.Done) != 3 {
		t.Fatalf("unexpected two-phase result: %#v", result)
	}
	wantActions := []string{"reply", "done", "reply", "done", "done"}
	for index, mutation := range result.Mutations {
		if mutation.MutationIndex != index+1 || mutation.Action != wantActions[index] {
			t.Fatalf("mutations are not in manifest order: %#v", result.Mutations)
		}
	}
	if !boolPointerValue(result.Mutations[0].Published) || !boolPointerValue(result.Mutations[2].Published) ||
		!boolPointerValue(result.Mutations[1].FinalDone) || !boolPointerValue(result.Mutations[4].FinalDone) {
		t.Fatalf("unexpected per-mutation states: %#v", result.Mutations)
	}
	submits := submissionRequestIndexes(t, transport)
	if len(submits) != 2 {
		t.Fatalf("submission count = %d, want 2", len(submits))
	}
	for index, request := range transport.requests {
		if index < submits[0] && request.method == http.MethodPost && request.form(t).Get("op") == "done" {
			t.Fatal("Done was drafted before the first replies were published")
		}
	}
}

func TestBatchRejectsFirstReplyAndDoneWithoutSubmit(t *testing.T) {
	reply, done := "reply", true
	for _, dryRun := range []bool{false, true} {
		inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
		service, transport := serviceWith(
			conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		)
		_, err := service.batch(batchManifest{
			Revision: "D1",
			Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
		}, false, dryRun)
		if err == nil || !strings.Contains(err.Error(), "#20") || !strings.Contains(err.Error(), "--submit") {
			t.Fatalf("dryRun=%v: expected first-reply rejection, got %v", dryRun, err)
		}
		if len(transport.requests) != 1 {
			t.Fatalf("dryRun=%v: made %d requests, want only the validation read", dryRun, len(transport.requests))
		}
	}
}

func TestBatchAllowsReplyAndDoneWithoutSubmitWhenParentHasReplies(t *testing.T) {
	reply, done := "reply", true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline, publishedReply(90, 20)}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, false, true)
	if err != nil || result.TwoPhase || len(result.Mutations) != 2 {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}

func TestBatchDryRunReportsTwoPhasePlan(t *testing.T) {
	reply, done := "reply", true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, true, true)
	if err != nil || !result.TwoPhase || result.State != "planned" {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	if got := renderBatch(result); !strings.Contains(got, "two") {
		t.Fatalf("text plan does not mention two submissions: %q", got)
	}
}

func TestBatchFirstSubmissionFailureLeavesDoneUnattempted(t *testing.T) {
	reply, done := "reply", true
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"dialog": "An inline comment is still being edited."}}),
	)
	result, err := service.batch(batchManifest{
		Revision: "D1",
		Actions:  []batchManifestAction{{CommentID: 20, Reply: &reply, Done: &done}},
	}, true, false)
	if err == nil || result.State != "partial" || result.Failure == nil ||
		result.Failure.Action != "submit" || len(result.Failure.NotAttemptedDone) != 1 ||
		result.Failure.NotAttemptedDone[0] != 20 || result.ReplySubmission == nil ||
		result.ReplySubmission.Submitted || len(result.Mutations) != 1 {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	for _, request := range transport.requests {
		if request.method == http.MethodPost && request.form(t).Get("op") == "done" {
			t.Fatal("Done was drafted after the reply submission failed")
		}
	}
}

func TestReplyDonePublishesFirstReplyBeforeDone(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
		conduitResult(map[string]any{"data": []any{
			transaction(2, "inline", 20, map[string]any{"isDone": true}),
		}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.reply("D1", "20", "reply", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "reply+done" || !result.Published || result.ReplySubmission == nil ||
		!result.ReplySubmission.Submitted || result.Submission == nil ||
		!boolPointerValue(result.FinalDone) {
		t.Fatalf("unexpected result: %#v", result)
	}
	submits := submissionRequestIndexes(t, transport)
	if len(submits) != 2 || transport.requests[submits[0]+1].form(t).Get("op") != "done" {
		t.Fatalf("expected reply submission, Done draft, then Done submission; submits at %v", submits)
	}
}

func TestReplyDoneRejectsFirstReplyWithoutSubmit(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
	)
	_, err := service.reply("D1", "20", "reply", true, false)
	if err == nil || !strings.Contains(err.Error(), "--submit") || len(transport.requests) != 1 {
		t.Fatalf("expected rejection before mutation, got %v after %d requests", err, len(transport.requests))
	}
}
