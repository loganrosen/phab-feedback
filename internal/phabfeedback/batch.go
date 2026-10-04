package phabfeedback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func readBatchManifest(path string) (batchManifest, error) {
	//nolint:gosec // The manifest path is intentionally user-selectable.
	data, err := os.ReadFile(path)
	if err != nil {
		return batchManifest{}, fmt.Errorf("could not read batch manifest: %s", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest batchManifest
	if err := decoder.Decode(&manifest); err != nil {
		return batchManifest{}, fmt.Errorf("invalid batch manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return batchManifest{}, fmt.Errorf("invalid batch manifest: expected one JSON object")
	}
	if err := validateBatchManifest(manifest); err != nil {
		return batchManifest{}, err
	}
	return manifest, nil
}

func validateBatchManifest(manifest batchManifest) error {
	revisionID, err := revisionNumber(manifest.Revision)
	if err != nil {
		return fmt.Errorf("invalid batch manifest revision: %w", err)
	}
	if len(manifest.Actions) == 0 {
		return fmt.Errorf("invalid batch manifest for D%d: actions must not be empty", revisionID)
	}
	seen := map[int]bool{}
	for index, action := range manifest.Actions {
		position := index + 1
		if action.CommentID < 1 {
			return fmt.Errorf("invalid batch manifest action %d: comment_id must be positive", position)
		}
		if seen[action.CommentID] {
			return fmt.Errorf("invalid batch manifest action %d: comment_id %d was specified more than once", position, action.CommentID)
		}
		seen[action.CommentID] = true
		if action.Reply != nil && strings.TrimSpace(*action.Reply) == "" {
			return fmt.Errorf("invalid batch manifest action %d: reply must not be empty", position)
		}
		if action.Done != nil && !*action.Done {
			return fmt.Errorf("invalid batch manifest action %d: done must be true when present", position)
		}
		if action.Reply == nil && action.Done == nil {
			return fmt.Errorf("invalid batch manifest action %d: provide reply, done, or both", position)
		}
	}
	return nil
}

func (s *feedbackService) batch(manifest batchManifest, submit, dryRun bool) (batchResult, error) {
	if err := validateBatchManifest(manifest); err != nil {
		return batchResult{}, err
	}
	revisionID, _ := revisionNumber(manifest.Revision)
	targets := make([]string, 0, len(manifest.Actions))
	for _, action := range manifest.Actions {
		targets = append(targets, strconv.Itoa(action.CommentID))
	}
	comments, err := s.validateInlineComments(manifest.Revision, targets)
	if err != nil {
		return batchResult{}, err
	}
	byID := make(map[int]validatedComment, len(comments))
	for _, comment := range comments {
		byID[comment.ID] = comment
	}
	firstReplyDone := batchFirstReplyDoneTargets(manifest, byID)
	if len(firstReplyDone) > 0 && !submit {
		return batchResult{}, firstReplyDoneError(firstReplyDone)
	}
	twoPhase := len(firstReplyDone) > 0
	planned := plannedBatchMutations(manifest)
	result := batchResult{
		RevisionID: revisionID, Action: "batch", DryRun: dryRun, Submit: submit,
		TwoPhase: twoPhase, State: "planned", Mutations: planned,
	}
	if dryRun {
		return result, nil
	}
	result.Mutations = make([]batchMutation, 0, len(planned))
	completedMutations := 0
	draftPlanned := func(include func(batchMutation) bool) error {
		for _, mutation := range planned {
			if !include(mutation) {
				continue
			}
			action := manifest.Actions[mutation.ActionIndex-1]
			if err := s.draftBatchMutation(revisionID, byID[mutation.CommentID], action, mutation, &result); err != nil {
				return err
			}
			completedMutations++
		}
		return nil
	}
	var deferredDone []int
	if twoPhase {
		for _, mutation := range planned {
			if mutation.Action == "done" {
				deferredDone = append(deferredDone, mutation.CommentID)
			}
		}
	}
	if err := draftPlanned(func(mutation batchMutation) bool {
		return !twoPhase || mutation.Action == "reply"
	}); err != nil {
		last := result.Mutations[len(result.Mutations)-1]
		return failedBatch(result, last.ActionIndex, last.MutationIndex, last.Action, completedMutations, deferredDone, err)
	}
	result.State = "draft"
	if !submit {
		return result, nil
	}
	if twoPhase {
		replySubmission, err := s.submit(manifest.Revision)
		result.ReplySubmission = &replySubmission
		if replySubmission.Submitted {
			markBatchRepliesPublished(result.Mutations)
		}
		if err == nil && !replySubmission.Submitted {
			err = fmt.Errorf("reply drafts were created but Phabricator reported no publishable effect; inspect the revision")
		}
		if err != nil {
			return failedBatch(result, 0, 0, "submit", completedMutations, deferredDone, err)
		}
		if err := draftPlanned(func(mutation batchMutation) bool { return mutation.Action == "done" }); err != nil {
			last := result.Mutations[len(result.Mutations)-1]
			return failedBatch(result, last.ActionIndex, last.MutationIndex, last.Action, completedMutations, nil, err)
		}
		sortBatchMutations(result.Mutations)
	}
	if !batchMutationsHaveDraft(result.Mutations) {
		result.State = "unchanged"
		if twoPhase {
			result.State = "published"
			return result, nil
		}
		result.Submission = unattemptedSubmission(
			revisionID,
			"The batch created no new drafts; existing revision drafts remain unpublished.",
		)
		return result, nil
	}
	doneIDs := batchDoneCommentIDs(result.Mutations)
	var submission submissionResult
	if len(doneIDs) > 0 {
		submission, err = s.submitAndVerifyDone(manifest.Revision, doneIDs)
	} else {
		submission, err = s.submit(manifest.Revision)
	}
	result.Submission = &submission
	if submission.Submitted {
		reconcileBatchDoneMutations(result.Mutations, submission.DoneVerification)
		markBatchRepliesPublished(result.Mutations)
	}
	if err != nil {
		return failedBatch(result, 0, 0, "submit", completedMutations, nil, err)
	}
	if !submission.Submitted {
		return failedBatch(
			result, 0, 0, "submit", completedMutations, nil,
			fmt.Errorf("batch drafts were created but Phabricator reported no publishable effect; inspect the revision"),
		)
	}
	result.State = "published"
	return result, nil
}

func (s *feedbackService) draftBatchMutation(
	revisionID int,
	comment validatedComment,
	action batchManifestAction,
	planned batchMutation,
	result *batchResult,
) error {
	if planned.Action == "reply" {
		reply, err := s.draftInlineReplyValidated(revisionID, comment, *action.Reply)
		mutation := batchMutation{
			ActionIndex: planned.ActionIndex, MutationIndex: planned.MutationIndex,
			Action: "reply", CommentID: action.CommentID,
			ParentCommentID: action.CommentID, CreatedReplyID: reply.CreatedReplyID,
			Saved: reply.Saved,
		}
		if reply.Saved {
			draft, published := reply.Draft, reply.Published
			mutation.Draft = &draft
			mutation.Published = &published
		}
		result.Mutations = append(result.Mutations, mutation)
		return err
	}
	done, err := s.markDoneValidated(revisionID, []validatedComment{comment})
	mutation := batchMutation{
		ActionIndex: planned.ActionIndex, MutationIndex: planned.MutationIndex,
		Action: "done", CommentID: action.CommentID,
	}
	if len(done.Comments) > 0 {
		item := done.Comments[0]
		mutation.Draft, mutation.Published = item.Draft, item.Published
		mutation.FinalDone = item.FinalDone
		mutation.ObservedChecked, mutation.ObservedDraftState = item.ObservedChecked, item.ObservedDraftState
		mutation.Recovery = item.Recovery
	}
	result.Mutations = append(result.Mutations, mutation)
	return err
}

// batchFirstReplyDoneTargets returns parents that would receive their first
// reply and a Done state in the same submission.
// Phabricator drops Done when the same submission publishes the parent's
// first reply, so these targets need replies published first: https://we.phorge.it/T16847
func batchFirstReplyDoneTargets(manifest batchManifest, comments map[int]validatedComment) []int {
	ids := make([]int, 0)
	for _, action := range manifest.Actions {
		if action.Reply != nil && action.Done != nil && !comments[action.CommentID].HasReplies {
			ids = append(ids, action.CommentID)
		}
	}
	return ids
}

func firstReplyDoneError(ids []int) error {
	return fmt.Errorf(
		"refusing to draft a first reply and Done together for %s: Phabricator drops the Done state when both are published in one submission. "+
			"Add --submit to publish the replies and then the Done states in two submissions, or publish the replies first and mark Done afterward",
		commentIDList(ids),
	)
}

func sortBatchMutations(mutations []batchMutation) {
	sort.SliceStable(mutations, func(i, j int) bool {
		return mutations[i].MutationIndex < mutations[j].MutationIndex
	})
}

func markBatchRepliesPublished(mutations []batchMutation) {
	for index := range mutations {
		if mutations[index].Action != "reply" || !boolPointerValue(mutations[index].Draft) {
			continue
		}
		draft, published := false, true
		mutations[index].Draft = &draft
		mutations[index].Published = &published
	}
}

func batchDoneCommentIDs(mutations []batchMutation) []int {
	ids := make([]int, 0)
	for _, mutation := range mutations {
		if mutation.Action == "done" {
			ids = append(ids, mutation.CommentID)
		}
	}
	return ids
}

func reconcileBatchDoneMutations(mutations []batchMutation, verification *verificationResult) {
	byID := make(map[int]doneVerification)
	if verification != nil {
		for _, state := range verification.Done {
			byID[state.CommentID] = state
		}
	}
	for index := range mutations {
		if mutations[index].Action != "done" {
			continue
		}
		mutations[index].Draft = nil
		mutations[index].Published = nil
		mutations[index].FinalDone = nil
		if state, ok := byID[mutations[index].CommentID]; ok {
			isDone := state.Found && state.ConduitIsDone
			mutations[index].FinalDone = &isDone
		}
	}
}

func batchMutationsHaveDraft(mutations []batchMutation) bool {
	for _, mutation := range mutations {
		if boolPointerValue(mutation.Draft) {
			return true
		}
	}
	return false
}

func plannedBatchMutations(manifest batchManifest) []batchMutation {
	mutations := make([]batchMutation, 0, len(manifest.Actions)*2)
	mutationIndex := 0
	for index, action := range manifest.Actions {
		position := index + 1
		if action.Reply != nil {
			mutationIndex++
			mutations = append(mutations, batchMutation{
				ActionIndex: position, MutationIndex: mutationIndex,
				Action: "reply", CommentID: action.CommentID,
				ParentCommentID: action.CommentID, Planned: true,
			})
		}
		if action.Done != nil {
			mutationIndex++
			mutations = append(mutations, batchMutation{
				ActionIndex: position, MutationIndex: mutationIndex,
				Action: "done", CommentID: action.CommentID,
				Planned: true,
			})
		}
	}
	return mutations
}

func failedBatch(
	result batchResult,
	actionIndex, mutationIndex int,
	action string,
	completedMutations int,
	notAttemptedDone []int,
	cause error,
) (batchResult, error) {
	sortBatchMutations(result.Mutations)
	result.State = "partial"
	result.Failure = &batchFailure{
		ActionIndex: actionIndex, MutationIndex: mutationIndex,
		Action: action, CompletedMutations: completedMutations,
		NotAttemptedDone: notAttemptedDone, Error: cause.Error(),
	}
	location := "during submission"
	if actionIndex > 0 {
		location = fmt.Sprintf("during manifest action %d, mutation %d (%s)", actionIndex, mutationIndex, action)
	}
	return result, &mutationResultError{
		result: result,
		err: fmt.Errorf(
			"remote partial failure %s; %d prior mutations completed and attempted mutations may remain on the server: %w",
			location, completedMutations, cause,
		),
	}
}

func boolPointerValue(value *bool) bool {
	return value != nil && *value
}

func parseReplyExpectations(values []string) ([]replyExpectation, error) {
	result := make([]replyExpectation, 0, len(values))
	for _, value := range values {
		reply, parent, ok := strings.Cut(value, ":")
		if !ok {
			return nil, fmt.Errorf("invalid --reply value %q; use REPLY_ID:PARENT_ID", value)
		}
		replyID, err := commentID(reply)
		if err != nil {
			return nil, fmt.Errorf("invalid reply ID in --reply %q", value)
		}
		parentID, err := commentID(parent)
		if err != nil {
			return nil, fmt.Errorf("invalid parent comment ID in --reply %q", value)
		}
		result = append(result, replyExpectation{ReplyID: replyID, ParentID: parentID})
	}
	return result, nil
}

func (s *feedbackService) verify(revision string, replies []replyExpectation, doneTargets []string) (verificationResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return verificationResult{}, err
	}
	if len(replies) == 0 && len(doneTargets) == 0 {
		return verificationResult{}, fmt.Errorf("provide at least one --reply or --done expectation")
	}
	doneIDs := make([]int, 0, len(doneTargets))
	for _, value := range doneTargets {
		identifier, err := commentID(value)
		if err != nil {
			return verificationResult{}, err
		}
		doneIDs = append(doneIDs, identifier)
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return verificationResult{}, err
	}
	byID := map[int]map[string]any{}
	phidToID := map[string]int{}
	for _, transaction := range transactions {
		comment := activeComment(transaction)
		if comment == nil {
			continue
		}
		identifier, ok := intValue(comment["id"])
		if !ok {
			continue
		}
		byID[identifier] = transaction
		if phid := stringValue(comment["phid"]); phid != "" {
			phidToID[phid] = identifier
		}
	}
	result := verificationResult{
		RevisionID: revisionID, Action: "verify", Status: "verified", ChecksPassed: true,
		Replies: make([]replyVerification, 0, len(replies)),
		Done:    make([]doneVerification, 0, len(doneIDs)),
	}
	for _, expectation := range replies {
		transaction := byID[expectation.ReplyID]
		found := transaction != nil && stringValue(transaction["type"]) == "inline"
		fields, _ := mapValue(transaction["fields"])
		parentID := phidToID[stringValue(fields["replyToCommentPHID"])]
		linked := found && parentID == expectation.ParentID
		result.Replies = append(result.Replies, replyVerification{
			ReplyID: expectation.ReplyID, ParentCommentID: expectation.ParentID,
			Found: found, Linked: linked,
		})
		result.ChecksPassed = result.ChecksPassed && found && linked
	}
	for _, identifier := range doneIDs {
		transaction := byID[identifier]
		found := transaction != nil && stringValue(transaction["type"]) == "inline"
		fields, _ := mapValue(transaction["fields"])
		conduitIsDone := found && boolValue(fields["isDone"])
		state := "missing"
		if found {
			state = "not-done"
		}
		if conduitIsDone {
			state = "done-or-pending-undo"
		}
		result.Done = append(result.Done, doneVerification{
			CommentID: identifier, Found: found, ConduitIsDone: conduitIsDone, State: state,
		})
		result.ChecksPassed = result.ChecksPassed && found && conduitIsDone
	}
	if len(doneIDs) > 0 {
		result.DoneStateAmbiguous = true
		result.Status = "observed"
		result.Limitations = append(result.Limitations,
			"Conduit isDone cannot distinguish published Done from a pending undo-Done draft.",
		)
	}
	if !result.ChecksPassed {
		result.Status = "failed"
	}
	return result, nil
}
