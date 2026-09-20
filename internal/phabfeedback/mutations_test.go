package phabfeedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplyDoneAndSubmissionPayloads(t *testing.T) {
	inline := transaction(1, "inline", 20, nil)
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"redirect": "/D1"}}),
	)
	reply, err := service.draftInlineReply("D1", "20", "A reply")
	if err != nil {
		t.Fatal(err)
	}
	if reply.DraftCommentID != 55 {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	if transport.requests[2].form(t).Get("replyToCommentPHID") != "PHID-CMT-20" {
		t.Fatal("reply did not preserve parent PHID")
	}
	if transport.requests[3].form(t).Get("op") != "save" || transport.requests[3].form(t).Get("id") != "55" {
		t.Fatalf("unexpected reply save form: %v", transport.requests[3].form(t))
	}
	if transport.requests[2].headers.Get("X-Phabricator-Csrf") != "B@csrf123" {
		t.Fatalf("missing CSRF header: %v", transport.requests[2].headers)
	}
	submitted, err := service.submit("D1")
	if err != nil || !submitted.Attempted || !submitted.Submitted || submitted.Outcome != "submitted" {
		t.Fatalf("submit: %#v %v", submitted, err)
	}
	if transport.requests[4].form(t).Get("editengine.actions") != "[]" {
		t.Fatal("missing editengine actions")
	}
}

func TestSubmitRequiresRedirectConfirmation(t *testing.T) {
	tests := []struct {
		name        string
		payload     map[string]any
		wantOutcome string
		wantUnknown bool
		wantError   string
	}{
		{
			name: "dialog",
			payload: map[string]any{
				"dialog": `<div class="aphront-dialog-head">Unsaved Inline</div>
					<div>An inline comment is still being edited.</div>`,
			},
			wantOutcome: "rejected",
			wantError:   "unsaved inline comment",
		},
		{
			name:        "missing redirect",
			payload:     map[string]any{},
			wantOutcome: "unknown",
			wantUnknown: true,
			wantError:   "contained no redirect",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := serviceWith(
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": test.payload}),
			)
			result, err := service.submit("D1")
			if err == nil || !result.Attempted || result.Submitted || result.Outcome != test.wantOutcome ||
				result.OutcomeUnknown != test.wantUnknown ||
				!strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("unexpected submission result: %#v %v", result, err)
			}
			if strings.ContainsAny(result.Dialog, "<>") {
				t.Fatalf("dialog markup was not removed: %q", result.Dialog)
			}
		})
	}
}

func TestSubmitTreatsEmptyCommentDialogAsNoEffect(t *testing.T) {
	service, _ := serviceWith(
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{
			"dialog": `<div class="aphront-dialog-head">Empty Comment</div>
				<div>You can not post an empty comment.</div>`,
		}}),
	)
	result, err := service.submit("D1")
	if err != nil || !result.Attempted || result.Submitted || result.Outcome != "no-effect" ||
		!strings.Contains(result.Dialog, "Empty Comment") {
		t.Fatalf("unexpected submission result: %#v %v", result, err)
	}
}

func TestSubmitClassifiesOnlyEmptyCommentTitleAsNoEffect(t *testing.T) {
	service, _ := serviceWith(
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{
			"dialog": `<div class="aphront-dialog-head">1 Action(s) With No Effect</div>
				<div>You can not post an empty comment.</div>
				<button>Apply Remaining Actions</button>`,
		}}),
	)
	result, err := service.submit("D1")
	if err == nil || result.Outcome != submissionOutcomeRejected ||
		!strings.Contains(result.Dialog, "Apply Remaining Actions") {
		t.Fatalf("unexpected submission result: %#v %v", result, err)
	}
}

func TestSubmitRejectsActionsWithNoEffectConfirmation(t *testing.T) {
	service, _ := serviceWith(
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{
			"dialog": `<div class="aphront-dialog-head">1 Action(s) With No Effect</div>
				<div>Some of your actions have no effect.</div>
				<button>Apply Remaining Actions</button>`,
		}}),
	)
	result, err := service.submit("D1")
	if err == nil || !result.Attempted || result.Submitted || result.Outcome != "rejected" ||
		!strings.Contains(result.Dialog, "Apply Remaining Actions") {
		t.Fatalf("unexpected submission result: %#v %v", result, err)
	}
}

func TestSubmitReportsCSRFFailureAsNotAttempted(t *testing.T) {
	service, _ := serviceWith(errors.New("csrf failed"))
	result, err := service.submit("D1")
	if err == nil || result.Attempted || result.Submitted || result.Outcome != submissionOutcomeBlocked ||
		!strings.Contains(result.Recovery, "CSRF") {
		t.Fatalf("unexpected submission result: %#v %v", result, err)
	}
}

func TestDialogSummaryProducesBoundedPlainText(t *testing.T) {
	dialog := `<div class="aphront-dialog-head">Warning &amp; Details</div><p>` +
		`Escaped &lt;script&gt; and literal 5 < 3 still has trailing guidance. ` +
		strings.Repeat("x", 600) + `</p>`
	result := parseDialog(dialog)
	if result.Title != "Warning & Details" ||
		strings.ContainsAny(result.Text, "<>") ||
		!strings.Contains(result.Text, `Escaped \x3cscript\x3e`) ||
		!strings.Contains(result.Text, `literal 5 \x3c 3 still has trailing guidance`) ||
		len([]rune(result.Text)) > 500 || !strings.HasSuffix(result.Text, "...") {
		t.Fatalf("unexpected dialog summary: %#v", result)
	}
}

func TestReplySaveRequiresInlineConfirmation(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "dialog",
			payload: map[string]any{"dialog": "This inline is still being edited."},
			want:    "Phabricator returned a dialog",
		},
		{
			name:    "missing inline",
			payload: map[string]any{},
			want:    "did not include a rendered inline",
		},
		{
			name:    "empty inline",
			payload: map[string]any{"inline": map[string]any{}},
			want:    "did not include a rendered inline",
		},
		{
			name:    "wrong inline",
			payload: map[string]any{"inline": map[string]any{"id": 56}},
			want:    "identified inline 56 instead of 55",
		},
		{
			name:    "string inline ID",
			payload: map[string]any{"inline": map[string]any{"id": "55"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inline := transaction(1, "inline", 20, nil)
			service, _ := serviceWith(
				conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
				response(map[string]any{"payload": test.payload}),
			)
			result, err := service.draftInlineReply("D1", "20", "reply")
			if test.want == "" {
				if err != nil || !result.Saved || !result.Draft {
					t.Fatalf("unexpected reply result: %#v %v", result, err)
				}
			} else if err == nil || result.CreatedReplyID != 55 || result.Saved || result.Draft ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected reply failure: %#v %v", result, err)
			}
		})
	}
}

func TestReplySubmissionDialogPreservesDraftState(t *testing.T) {
	inline := transaction(1, "inline", 20, nil)
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"dialog": "An inline comment is still being edited."}}),
	)
	result, err := service.reply("D1", "20", "reply", false, true)
	if err == nil || !result.Draft || result.Published || result.Submission == nil ||
		result.Submission.Submitted || result.Submission.Outcome != "rejected" {
		t.Fatalf("unexpected reply result: %#v %v", result, err)
	}
}

func TestReplySubmissionWarningIsPartialFailure(t *testing.T) {
	inline := transaction(1, "inline", 20, nil)
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{"inline": map[string]any{"id": 55}}}),
		response(map[string]any{"payload": map[string]any{
			"dialog": `<div>1 Action(s) With No Effect</div><button>Apply Remaining Actions</button>`,
		}}),
	)
	result, err := service.reply("D1", "20", "reply", false, true)
	if err == nil || !result.Draft || result.Published || result.Submission == nil ||
		result.Submission.Outcome != "rejected" || !strings.Contains(err.Error(), "submission failed") {
		t.Fatalf("unexpected reply result: %#v %v", result, err)
	}
}

func TestPostCommentContract(t *testing.T) {
	service, transport := serviceWith(conduitResult(map[string]any{"object": map[string]any{"id": 1}}))
	result, err := service.postComment("D1", "Looks good")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Posted || result.RevisionID != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(transport.requests) != 1 || transport.requests[0].target != "https://phab.example/api/differential.revision.edit" {
		t.Fatalf("unexpected requests: %#v", transport.requests)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(transport.requests[0].form(t).Get("params")), &params); err != nil {
		t.Fatal(err)
	}
	transactions, _ := sliceValue(params["transactions"])
	transaction, _ := mapValue(transactions[0])
	if transaction["type"] != "comment" || transaction["value"] != "Looks good" {
		t.Fatalf("unexpected transaction: %#v", transaction)
	}
}

func TestRemoveCommentContractAndConfirmation(t *testing.T) {
	original := transaction(1, "comment", 20, nil)
	removed := transaction(1, "comment", 20, nil)
	removedComment := activeComment(removed)
	removedComment["version"] = 2
	removedComment["removed"] = true
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{original}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{}}),
		conduitResult(map[string]any{"data": []any{removed}, "cursor": map[string]any{"after": nil}}),
	)
	result, err := service.removeComment("D1", "20")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed || result.CommentID != 20 {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := transport.requests[2]
	if request.target != "https://phab.example/transactions/edit/PHID-XACT-1/" {
		t.Fatalf("unexpected removal target: %s", request.target)
	}
	if request.form(t).Get("text") != "" || request.form(t).Get("__form__") != "1" {
		t.Fatalf("unexpected removal form: %v", request.form(t))
	}
}

func TestRemoveCommentRequiresServerConfirmation(t *testing.T) {
	comment := transaction(1, "comment", 20, nil)
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{comment}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{}}),
		conduitResult(map[string]any{"data": []any{comment}, "cursor": map[string]any{"after": nil}}),
	)
	if _, err := service.removeComment("D1", "20"); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkDoneRetriesUncheckedResponse(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": true})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": false}}),
	)
	result, err := service.markDone("D1", []string{"20"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Comments) != 1 || result.Comments[0].IsDone == nil || !*result.Comments[0].IsDone ||
		result.Comments[0].Draft == nil || *result.Comments[0].Draft ||
		result.Comments[0].Published == nil || !*result.Comments[0].Published {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("Done state required %d requests, want 4", len(transport.requests))
	}
	for _, request := range transport.requests[2:] {
		if request.form(t).Get("op") != "done" || request.form(t).Get("id") != "20" {
			t.Fatalf("unexpected Done form: %v", request.form(t))
		}
	}
}

func TestMarkDoneRestoresClearedPendingDraft(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": false}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
	)
	result, err := service.markDone("D1", []string{"20"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Comments) != 1 || !boolPointerValue(result.Comments[0].IsDone) ||
		!boolPointerValue(result.Comments[0].Draft) || boolPointerValue(result.Comments[0].Published) {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestMarkDoneRetryFailureReportsActionableState(t *testing.T) {
	tests := []struct {
		name       string
		draftState bool
		want       string
	}{
		{name: "pending undo", draftState: true, want: "pending undo-Done draft"},
		{name: "cleared pending done", draftState: false, want: "cleared the pending Done draft"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inline := transaction(1, "inline", 20, map[string]any{"isDone": test.draftState})
			service, _ := serviceWith(
				conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": map[string]any{
					"isChecked": false, "draftState": test.draftState,
				}}),
				io.ErrUnexpectedEOF,
			)
			result, err := service.markDone("D1", []string{"20"}, false)
			if err == nil || len(result.Comments) != 1 {
				t.Fatalf("unexpected result: %#v %v", result, err)
			}
			comment := result.Comments[0]
			if comment.IsDone != nil || comment.Draft != nil || comment.Published != nil ||
				comment.ObservedChecked == nil || *comment.ObservedChecked ||
				comment.ObservedDraftState == nil || *comment.ObservedDraftState != test.draftState ||
				comment.Recovery == "" ||
				!strings.Contains(result.Comments[0].Recovery, test.want) ||
				!strings.Contains(err.Error(), "Rerun done") {
				t.Fatalf("missing recovery guidance: %#v %v", result, err)
			}
			body, marshalErr := json.Marshal(comment)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			text := string(body)
			for _, absent := range []string{`"is_done"`, `"draft"`, `"published"`} {
				if strings.Contains(text, absent) {
					t.Fatalf("unknown outcome included %s: %s", absent, text)
				}
			}
			for _, present := range []string{`"observed_checked":false`, `"observed_draft_state":`} {
				if !strings.Contains(text, present) {
					t.Fatalf("observed state missing %s: %s", present, text)
				}
			}
		})
	}
}

func TestMarkDoneDoesNotRetryMalformedResponse(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{name: "missing checked", payload: map[string]any{"draftState": true}},
		{name: "missing draft state", payload: map[string]any{"isChecked": false}},
		{name: "wrong checked type", payload: map[string]any{"isChecked": "false", "draftState": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
			service, transport := serviceWith(
				conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
				response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
				response(map[string]any{"payload": test.payload}),
			)
			result, err := service.markDone("D1", []string{"20"}, false)
			if err == nil || !strings.Contains(err.Error(), "invalid Done response") ||
				len(result.Comments) != 1 || result.Comments[0].Recovery == "" {
				t.Fatalf("unexpected result: %#v %v", result, err)
			}
			if len(transport.requests) != 3 {
				t.Fatalf("malformed response triggered another mutation: %d requests", len(transport.requests))
			}
		})
	}
}

func TestMarkDoneMalformedRetryPreservesOnlyObservedState(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": true})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"draftState": false}}),
	)
	result, err := service.markDone("D1", []string{"20"}, false)
	if err == nil || !strings.Contains(err.Error(), "invalid Done retry response") ||
		len(result.Comments) != 1 {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	comment := result.Comments[0]
	if comment.IsDone != nil || comment.Draft != nil || comment.Published != nil ||
		comment.ObservedChecked == nil || *comment.ObservedChecked ||
		comment.ObservedDraftState == nil || !*comment.ObservedDraftState {
		t.Fatalf("unexpected observed state: %#v", comment)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(transport.requests))
	}
}

func TestMarkDoneReportsUnattemptedTargets(t *testing.T) {
	first := transaction(1, "inline", 20, map[string]any{"isDone": false})
	second := transaction(2, "inline", 21, map[string]any{"isDone": false})
	third := transaction(3, "inline", 22, map[string]any{"isDone": false})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{first, second, third}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": true}}),
		errors.New("second target failed"),
	)
	result, err := service.markDone("D1", []string{"20", "21", "22"}, false)
	if err == nil || len(result.Comments) != 2 || len(result.NotAttempted) != 1 ||
		result.NotAttempted[0] != 22 {
		t.Fatalf("unexpected partial result: %#v %v", result, err)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(transport.requests))
	}
}

func TestDoneFailureReportsSubmissionWasNotAttempted(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	service, _ := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		errors.New("Done failed"),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err == nil || result.Submission == nil ||
		result.Submission.Outcome != submissionOutcomeNotAttempted ||
		!strings.Contains(result.Submission.Recovery, "Done action failed") {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}

func TestDoneSubmitSkipsWhenTargetsAreAlreadyPublished(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": true})
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"isChecked": false, "draftState": true}}),
		response(map[string]any{"payload": map[string]any{"isChecked": true, "draftState": false}}),
	)
	result, err := service.markDone("D1", []string{"20"}, true)
	if err != nil || result.Submission == nil || result.Submission.Attempted ||
		result.Submission.Outcome != "not-attempted" || result.Submission.Submitted ||
		!boolPointerValue(result.Comments[0].Published) ||
		!strings.Contains(result.Submission.Recovery, "unrelated drafts remain unpublished") {
		t.Fatalf("unexpected Done result: %#v %v", result, err)
	}
	for _, request := range transport.requests {
		if strings.Contains(request.target, "/differential/revision/edit/1/comment/") {
			t.Fatal("submitted even though no new Done draft was created")
		}
	}
}

func TestInlineValidationRejectsMissingPHIDBeforeMutation(t *testing.T) {
	inline := transaction(1, "inline", 20, map[string]any{"isDone": false})
	activeComment(inline)["phid"] = ""
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
	)
	if _, err := service.draftInlineReply("D1", "20", "reply"); err == nil ||
		!strings.Contains(err.Error(), "has no PHID") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("mutation occurred after malformed validation data: %d requests", len(transport.requests))
	}
}

func TestCommentActionJSONPreservesFalseDraftState(t *testing.T) {
	value := false
	body, err := json.Marshal(commentAction{CommentID: 20, Draft: &value})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if draft, ok := payload["draft"]; !ok || draft != false {
		t.Fatalf("draft state was not preserved: %s", body)
	}
}

func TestRateContract(t *testing.T) {
	inline := transaction(1, "inline", 20, nil)
	service, transport := serviceWith(
		conduitResult(map[string]any{"data": []any{inline}, "cursor": map[string]any{"after": nil}}),
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"message": "recorded"}}),
	)
	result, err := service.rate("D1", []string{"20"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MozillaReviewHelper || result.Comments[0].Helpful == nil || *result.Comments[0].Helpful {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := transport.requests[2]
	if request.target != "https://phab.example/reviewhelper/feedback/" || request.form(t).Get("feedbackType") != "down" {
		t.Fatalf("unexpected rating request: %#v %v", request, request.form(t))
	}
}

func TestAIReviewContract(t *testing.T) {
	service, transport := serviceWith(
		response([]byte(`<input name="__csrf__" value="B@csrf123">`)),
		response(map[string]any{"payload": map[string]any{"dialog": "Request successfully queued."}}),
	)
	result, err := service.requestAIReview("D1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "requested" {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := transport.requests[1]
	if request.target != "https://phab.example/reviewhelper/request/1/" || request.form(t).Get("__metablock__") != "6" {
		t.Fatalf("unexpected AI review request: %#v %v", request, request.form(t))
	}
}

func TestDoctorChecksReadOnlyCredentialsAndCapabilities(t *testing.T) {
	isolateCredentialFiles(t)
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/api/user.whoami":
			_, _ = writer.Write(conduitResult(map[string]any{
				"phid": "PHID-USER-test", "userName": "logan", "realName": "Logan Rosen",
			}))
		case "/":
			_, _ = io.WriteString(writer, `<input name="__csrf__" value="B@csrf123"><a href="/logout/">Log out</a><a href="/reviewhelper/">Review Helper</a>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("PHAB_FEEDBACK_HOST", server.URL)
	t.Setenv("PHAB_FEEDBACK_TOKEN", "api-token")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "session-cookie")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(t.TempDir(), "missing-arcrc"))

	result := (&appOptions{}).doctor(t.Context())
	if !result.OK || len(result.Checks) != 4 {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, method := range methods {
		if !strings.HasPrefix(method, "GET ") && method != "POST /api/user.whoami" {
			t.Fatalf("doctor made unexpected request: %s", method)
		}
	}
}

func TestDoctorRejectsLoggedOutSession(t *testing.T) {
	isolateCredentialFiles(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/user.whoami":
			_, _ = writer.Write(conduitResult(map[string]any{
				"phid": "PHID-USER-test", "userName": "logan", "realName": "Logan Rosen",
			}))
		case "/":
			_, _ = io.WriteString(writer, `<input name="__csrf__" value="B@csrf123"><form action="/auth/start/">Log in</form>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("PHAB_FEEDBACK_HOST", server.URL)
	t.Setenv("PHAB_FEEDBACK_TOKEN", "api-token")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "expired-cookie")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(t.TempDir(), "missing-arcrc"))

	result := (&appOptions{}).doctor(t.Context())
	if result.OK {
		t.Fatalf("logged-out session passed diagnostics: %#v", result)
	}
	if result.Checks[2].Status != "error" || !strings.Contains(result.Checks[2].Message, "did not produce a logged-in") {
		t.Fatalf("unexpected web-session check: %#v", result.Checks[2])
	}
}

func TestDoctorAllowsMissingOptionalWebSession(t *testing.T) {
	isolateCredentialFiles(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/user.whoami" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(conduitResult(map[string]any{
			"phid": "PHID-USER-test", "userName": "logan", "realName": "Logan Rosen",
		}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("PHAB_FEEDBACK_HOST", server.URL)
	t.Setenv("PHAB_FEEDBACK_TOKEN", "api-token")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(t.TempDir(), "missing-arcrc"))

	result := (&appOptions{}).doctor(t.Context())
	if !result.OK || result.Checks[2].Status != "warning" {
		t.Fatalf("optional web session failed diagnostics: %#v", result)
	}
}

func TestDoctorReturnsFailureWithoutPrintingAnExtraError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PHAB_FEEDBACK_HOST", "")
	t.Setenv("PHAB_FEEDBACK_TOKEN", "")
	t.Setenv("PHAB_FEEDBACK_SESSION_COOKIE", "")
	t.Setenv("PHAB_FEEDBACK_ARCRC", filepath.Join(home, "missing-arcrc"))
	var stdout, stderr bytes.Buffer
	if status := Run([]string{"doctor", "--format", "json"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("doctor status = %d, want 1", status)
	}
	if stderr.Len() != 0 {
		t.Fatalf("doctor printed an extra error: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"ok": false`) {
		t.Fatalf("doctor did not emit diagnostics: %q", stdout.String())
	}
}
