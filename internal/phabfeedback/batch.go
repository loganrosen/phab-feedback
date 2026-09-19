package phabfeedback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	result := batchResult{
		RevisionID: revisionID, Action: "batch", DryRun: dryRun, Submit: submit,
		State: "planned", Mutations: plannedBatchMutations(manifest),
	}
	if dryRun {
		return result, nil
	}
	result.Mutations = make([]batchMutation, 0, len(result.Mutations))
	completedMutations := 0
	for index, action := range manifest.Actions {
		position := index + 1
		comment := byID[action.CommentID]
		if action.Reply != nil {
			reply, replyErr := s.draftInlineReplyValidated(revisionID, comment, *action.Reply)
			mutation := batchMutation{
				Index: position, Action: "reply", CommentID: action.CommentID,
				ParentCommentID: action.CommentID, CreatedReplyID: reply.CreatedReplyID,
				Saved: reply.Saved,
			}
			if reply.Saved {
				draft, published := reply.Draft, reply.Published
				mutation.Draft = &draft
				mutation.Published = &published
			}
			result.Mutations = append(result.Mutations, mutation)
			if replyErr != nil {
				return failedBatch(result, position, "reply", completedMutations, replyErr)
			}
			completedMutations++
		}
		if action.Done != nil {
			done, doneErr := s.markDoneValidated(revisionID, []validatedComment{comment})
			var mutation batchMutation
			if len(done.Comments) > 0 {
				item := done.Comments[0]
				mutation = batchMutation{
					Index: position, Action: "done", CommentID: action.CommentID,
					Draft: item.Draft, Published: item.Published,
					FinalDone: item.FinalDone, Recovery: item.Recovery,
				}
				result.Mutations = append(result.Mutations, mutation)
			}
			if doneErr != nil {
				return failedBatch(result, position, "done", completedMutations, doneErr)
			}
			completedMutations++
		}
	}
	result.State = "draft"
	if !submit {
		return result, nil
	}
	submission, err := s.submit(manifest.Revision)
	result.Submission = &submission
	if err != nil {
		return failedBatch(result, len(manifest.Actions), "submit", completedMutations, err)
	}
	for index := range result.Mutations {
		if boolPointerValue(result.Mutations[index].Draft) {
			draft, published := false, true
			result.Mutations[index].Draft = &draft
			result.Mutations[index].Published = &published
		}
	}
	result.State = "published"
	return result, nil
}

func plannedBatchMutations(manifest batchManifest) []batchMutation {
	mutations := make([]batchMutation, 0, len(manifest.Actions)*2)
	for index, action := range manifest.Actions {
		position := index + 1
		if action.Reply != nil {
			mutations = append(mutations, batchMutation{
				Index: position, Action: "reply", CommentID: action.CommentID,
				ParentCommentID: action.CommentID, Planned: true,
			})
		}
		if action.Done != nil {
			mutations = append(mutations, batchMutation{
				Index: position, Action: "done", CommentID: action.CommentID,
				Planned: true,
			})
		}
	}
	return mutations
}

func failedBatch(result batchResult, index int, action string, completedMutations int, cause error) (batchResult, error) {
	result.State = "partial"
	result.Failure = &batchFailure{
		Index: index, Action: action, CompletedMutations: completedMutations, Error: cause.Error(),
	}
	return result, &mutationResultError{
		result: result,
		err: fmt.Errorf(
			"remote partial failure during batch action %d (%s); %d prior mutations completed and attempted mutations may remain on the server: %w",
			index, action, completedMutations, cause,
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
		RevisionID: revisionID, Action: "verify", Verified: true,
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
		result.Verified = result.Verified && found && linked
	}
	for _, identifier := range doneIDs {
		transaction := byID[identifier]
		found := transaction != nil && stringValue(transaction["type"]) == "inline"
		fields, _ := mapValue(transaction["fields"])
		conduitIsDone := found && boolValue(fields["isDone"])
		result.Done = append(result.Done, doneVerification{
			CommentID: identifier, Found: found, ConduitIsDone: conduitIsDone,
			Ambiguous: conduitIsDone,
		})
		result.Verified = result.Verified && found && conduitIsDone
	}
	if len(doneIDs) > 0 {
		result.Limitations = append(result.Limitations,
			"Conduit isDone cannot distinguish published Done from a pending undo-Done draft.",
		)
	}
	return result, nil
}
