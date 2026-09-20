package phabfeedback

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"
)

var fallbackStatusConstraints = map[string][]string{
	"open":   {"needs-review", "needs-revision", "changes-planned", "accepted", "draft"},
	"closed": {"published", "abandoned"},
}

type feedbackService struct {
	conduit *conduitClient
	web     *webClient
}

func revisionNumber(revision string) (int, error) {
	value := strings.TrimSpace(revision)
	if len(value) > 0 && (value[0] == 'd' || value[0] == 'D') {
		value = value[1:]
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid revision identifier: %s", revision)
	}
	return number, nil
}

func commentID(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid comment ID: %s", value)
	}
	return number, nil
}

func activeComment(transaction map[string]any) map[string]any {
	versions, ok := sliceValue(transaction["comments"])
	if !ok || len(versions) == 0 {
		return nil
	}
	var latest map[string]any
	latestVersion := -1
	for _, raw := range versions {
		version, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		number, _ := intValue(version["version"])
		if latest == nil || number > latestVersion {
			latest, latestVersion = version, number
		}
	}
	if latest == nil || boolValue(latest["removed"]) {
		return nil
	}
	return latest
}

func (s *feedbackService) revisionTransactions(revision string) ([]map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	result, err := s.conduit.paginate("transaction.search", map[string]any{"objectIdentifier": fmt.Sprintf("D%d", revisionID)}, "", 0)
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

func (s *feedbackService) timeline(revision string) (timelineResult, error) {
	revisionID, _, currentDiff, err := s.revisionContext(revision, false)
	if err != nil {
		return timelineResult{}, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return timelineResult{}, err
	}
	return buildTimeline(revisionID, currentDiff, transactions), nil
}

func (s *feedbackService) listRevisions(role, status string, modifiedAfter *int64, limit int, after string) (revisionListResult, error) {
	roleConstraints := map[string]string{
		"responsible": "responsiblePHIDs",
		"authored":    "authorPHIDs",
		"reviewing":   "reviewerPHIDs",
	}
	constraint := roleConstraints[role]
	if constraint == "" {
		return revisionListResult{}, fmt.Errorf("unsupported revision role: %s", role)
	}
	if status != "open" && status != "closed" && status != "all" {
		return revisionListResult{}, fmt.Errorf("unsupported revision status: %s", status)
	}
	if limit < 1 {
		return revisionListResult{}, fmt.Errorf("revision limit must be positive")
	}
	var whoami struct {
		PHID     string `json:"phid"`
		UserName string `json:"userName"`
		RealName string `json:"realName"`
	}
	if err := s.conduit.call("user.whoami", map[string]any{}, &whoami); err != nil {
		return revisionListResult{}, err
	}
	if whoami.PHID == "" {
		return revisionListResult{}, fmt.Errorf("user.whoami returned invalid user data")
	}
	constraints := map[string]any{constraint: []string{whoami.PHID}}
	if status != "all" {
		constraints["statuses"] = []string{status + "()"}
	}
	if modifiedAfter != nil {
		constraints["modifiedStart"] = *modifiedAfter
	}
	search, err := s.conduit.search("differential.revision.search", constraints, map[string]any{"reviewers": true}, "updated", after, limit)
	var network *networkError
	if errors.As(err, &network) && status != "all" && network.status == 406 {
		constraints["statuses"] = fallbackStatusConstraints[status]
		search, err = s.conduit.search("differential.revision.search", constraints, map[string]any{"reviewers": true}, "updated", after, limit)
	}
	if err != nil {
		return revisionListResult{}, err
	}
	revisions := search.Data
	handles, err := s.hydrateRevisionHandles(revisions)
	if err != nil {
		return revisionListResult{}, err
	}
	normalized := make([]revisionRecord, 0, len(revisions))
	for _, revision := range revisions {
		item, err := normalizeRevision(revision, handles)
		if err != nil {
			return revisionListResult{}, err
		}
		normalized = append(normalized, item)
	}
	var modified any
	if modifiedAfter != nil {
		modified = timestamp(*modifiedAfter)
	}
	return revisionListResult{
		Viewer: viewer{PHID: whoami.PHID, Username: whoami.UserName, Name: whoami.RealName},
		Role:   role, Status: status, ModifiedAfter: modified, Count: len(normalized),
		Cursor: search.Cursor, Revisions: normalized,
	}, nil
}

func (s *feedbackService) show(revision string) (revisionSummary, error) {
	revisionID, rawRevision, currentDiff, err := s.revisionContext(revision, true)
	if err != nil {
		return revisionSummary{}, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return revisionSummary{}, err
	}
	timeline := buildTimeline(revisionID, currentDiff, transactions)
	grouped := groupThreads(timeline.InlineComments)
	unresolved, resolved, replies, older := 0, 0, 0, 0
	for _, item := range grouped.Threads {
		if item.Resolved {
			resolved++
		} else {
			unresolved++
		}
	}
	for _, item := range timeline.InlineComments {
		if item.ReplyToCommentPHID != nil {
			replies++
		}
		if !item.OnCurrentDiff {
			older++
		}
	}
	handles, err := s.hydrateRevisionHandles([]map[string]any{rawRevision})
	if err != nil {
		return revisionSummary{}, err
	}
	normalized, err := normalizeRevision(rawRevision, handles)
	if err != nil {
		return revisionSummary{}, err
	}
	return revisionSummary{
		Revision: normalized, CurrentDiff: timeline.CurrentDiff,
		Feedback: feedbackCounts{
			GeneralComments: len(timeline.GeneralComments), InlineComments: len(timeline.InlineComments),
			RootThreads: len(grouped.Threads), UnresolvedThreads: unresolved, ResolvedThreads: resolved,
			Replies: replies, OlderDiffComments: older, OrphanReplies: len(grouped.OrphanReplies),
		},
	}, nil
}

func (s *feedbackService) threads(revision, state string, currentDiffOnly bool) (threadsResult, error) {
	if state != "unresolved" && state != "resolved" && state != "all" {
		return threadsResult{}, fmt.Errorf("unsupported thread state: %s", state)
	}
	timeline, err := s.timeline(revision)
	if err != nil {
		return threadsResult{}, err
	}
	grouped := groupThreads(timeline.InlineComments)
	threads := make([]thread, 0, len(grouped.Threads))
	for _, item := range grouped.Threads {
		resolved := item.Resolved
		if state == "unresolved" && resolved || state == "resolved" && !resolved {
			continue
		}
		if currentDiffOnly && !item.Root.OnCurrentDiff {
			continue
		}
		threads = append(threads, item)
	}
	orphans := make([]feedbackEvent, 0, len(grouped.OrphanReplies))
	for _, item := range grouped.OrphanReplies {
		if !currentDiffOnly || item.OnCurrentDiff {
			orphans = append(orphans, item)
		}
	}
	return threadsResult{
		RevisionID: timeline.RevisionID, CurrentDiff: timeline.CurrentDiff, State: state,
		CurrentDiffOnly: currentDiffOnly, Count: len(threads), Threads: threads, OrphanReplies: orphans,
	}, nil
}

func buildTimeline(revisionID int, currentDiff map[string]any, transactions []map[string]any) timelineResult {
	currentDiffPHID := stringValue(currentDiff["phid"])
	byPHID := map[string]any{}
	for _, transaction := range transactions {
		versions, _ := sliceValue(transaction["comments"])
		for _, raw := range versions {
			version, ok := raw.(map[string]any)
			if ok && stringValue(version["phid"]) != "" {
				byPHID[stringValue(version["phid"])] = version["id"]
			}
		}
	}
	general := make([]feedbackEvent, 0)
	inline := make([]feedbackEvent, 0)
	for _, transaction := range transactions {
		kind := stringValue(transaction["type"])
		if kind != "comment" && kind != "inline" {
			continue
		}
		comment := activeComment(transaction)
		if comment == nil {
			continue
		}
		content, _ := mapValue(comment["content"])
		base := feedbackEvent{
			Kind: map[bool]string{true: "general", false: "inline"}[kind == "comment"],
			ID:   comment["id"], PHID: comment["phid"], TransactionID: transaction["id"],
			TransactionPHID: transaction["phid"], Created: timestampValue(comment["dateCreated"]),
			Content: content["raw"],
		}
		if kind == "comment" {
			general = append(general, base)
			continue
		}
		fields, _ := mapValue(transaction["fields"])
		diff, _ := mapValue(fields["diff"])
		parentPHID := fields["replyToCommentPHID"]
		item := base
		item.DiffID, item.DiffPHID = diff["id"], diff["phid"]
		item.OnCurrentDiff = stringValue(diff["phid"]) == currentDiffPHID
		item.Path, item.Line, item.IsDone = fields["path"], fields["line"], fields["isDone"]
		item.ReplyToCommentID, item.ReplyToCommentPHID = byPHID[stringValue(parentPHID)], parentPHID
		inline = append(inline, item)
	}
	sortFeedbackEvents(general)
	sortFeedbackEvents(inline)
	events := append(append([]feedbackEvent{}, general...), inline...)
	sortFeedbackEvents(events)
	fields, _ := mapValue(currentDiff["fields"])
	return timelineResult{
		RevisionID:  revisionID,
		CurrentDiff: diffInfo{ID: currentDiff["id"], PHID: currentDiffPHID, Created: timestampValue(fields["dateCreated"])},
		Events:      events, GeneralComments: general, InlineComments: inline,
	}
}

func (s *feedbackService) revisionContext(revision string, includeReviewers bool) (int, map[string]any, map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return 0, nil, nil, err
	}
	params := map[string]any{"constraints": map[string]any{"ids": []int{revisionID}}}
	if includeReviewers {
		params["attachments"] = map[string]any{"reviewers": true}
	}
	var revisionSearch searchPage
	if err := s.conduit.call("differential.revision.search", params, &revisionSearch); err != nil {
		return 0, nil, nil, err
	}
	revisions := revisionSearch.Data
	if len(revisions) == 0 {
		return 0, nil, nil, fmt.Errorf("revision D%d was not found", revisionID)
	}
	revisionData := revisions[0]
	fields, ok := mapValue(revisionData["fields"])
	if !ok {
		return 0, nil, nil, fmt.Errorf("differential.revision.search returned invalid fields")
	}
	diffPHID := stringValue(fields["diffPHID"])
	if diffPHID == "" {
		return 0, nil, nil, fmt.Errorf("revision D%d returned no current diff", revisionID)
	}
	var diffSearch searchPage
	if err := s.conduit.call("differential.diff.search", map[string]any{"constraints": map[string]any{"phids": []string{diffPHID}}}, &diffSearch); err != nil {
		return 0, nil, nil, err
	}
	diffs := diffSearch.Data
	if len(diffs) == 0 {
		return 0, nil, nil, fmt.Errorf("current diff for D%d was not found", revisionID)
	}
	currentDiff := diffs[0]
	if currentDiff["phid"] == nil {
		currentDiff["phid"] = diffPHID
	}
	return revisionID, revisionData, currentDiff, nil
}

func (s *feedbackService) hydrateRevisionHandles(revisions []map[string]any) (map[string]map[string]any, error) {
	phidSet := map[string]bool{}
	for _, revision := range revisions {
		fields, ok := mapValue(revision["fields"])
		if !ok {
			return nil, fmt.Errorf("differential.revision.search returned invalid fields")
		}
		for _, key := range []string{"authorPHID", "repositoryPHID"} {
			if value := stringValue(fields[key]); value != "" {
				phidSet[value] = true
			}
		}
		reviewers, err := revisionReviewers(revision)
		if err != nil {
			return nil, err
		}
		for _, reviewer := range reviewers {
			for _, key := range []string{"reviewerPHID", "actorPHID"} {
				if value := stringValue(reviewer[key]); value != "" {
					phidSet[value] = true
				}
			}
		}
	}
	phids := make([]string, 0, len(phidSet))
	for phid := range phidSet {
		phids = append(phids, phid)
	}
	sort.Strings(phids)
	handles := map[string]map[string]any{}
	for offset := 0; offset < len(phids); offset += 100 {
		end := min(offset+100, len(phids))
		var result map[string]map[string]any
		if err := s.conduit.call("phid.query", map[string]any{"phids": phids[offset:end]}, &result); err != nil {
			return nil, err
		}
		maps.Copy(handles, result)
	}
	return handles, nil
}

func normalizeRevision(revision map[string]any, handles map[string]map[string]any) (revisionRecord, error) {
	fields, ok := mapValue(revision["fields"])
	if !ok {
		return revisionRecord{}, fmt.Errorf("differential.revision.search returned invalid fields")
	}
	reviewers, err := revisionReviewers(revision)
	if err != nil {
		return revisionRecord{}, err
	}
	normalizedReviewers := make([]reviewer, 0, len(reviewers))
	for _, rawReviewer := range reviewers {
		item := handle(stringValue(rawReviewer["reviewerPHID"]), handles)
		if item == nil {
			item = &handleInfo{}
		}
		normalizedReviewers = append(normalizedReviewers, reviewer{
			Handle: *item, ReviewerPHID: rawReviewer["reviewerPHID"], Decision: rawReviewer["status"],
			IsBlocking: rawReviewer["isBlocking"], Actor: handle(stringValue(rawReviewer["actorPHID"]), handles),
		})
	}
	return revisionRecord{
		ID: revision["id"], PHID: revision["phid"], Title: fields["title"], URI: fields["uri"],
		Status: revisionStatusValue(fields["status"]), IsDraft: fields["isDraft"],
		Author:     handle(stringValue(fields["authorPHID"]), handles),
		Repository: handle(stringValue(fields["repositoryPHID"]), handles),
		Reviewers:  normalizedReviewers, Created: timestampValue(fields["dateCreated"]),
		Modified: timestampValue(fields["dateModified"]), CurrentDiffPHID: fields["diffPHID"],
		MergeConflictStatus: normalizeMergeConflictStatus(fields["merge.conflict.status"]),
	}, nil
}

func normalizeMergeConflictStatus(value any) *mergeConflictStatus {
	status, ok := mapValue(value)
	if !ok {
		return nil
	}
	return &mergeConflictStatus{
		Status: status["status"], Reason: status["reason"], IsStale: status["isStale"],
		CheckedAt: timestampValue(status["epoch"]), CheckedAgainstCommit: status["checkedAgainstCommit"],
		CheckedAgainstBaseCommit:       status["checkedAgainstBaseCommit"],
		CheckedAgainstBaseRevisionPHID: status["checkedAgainstBaseRevisionPHID"],
		CheckedAgainstDiffID:           status["checkedAgainstDiffID"], CheckedAgainstDiffPHID: status["checkedAgainstDiffPHID"],
	}
}

func (s *feedbackService) postComment(revision, message string) (commentResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return commentResult{}, err
	}
	var result any
	err = s.conduit.call("differential.revision.edit", map[string]any{
		"objectIdentifier": fmt.Sprintf("D%d", revisionID),
		"transactions":     []map[string]any{{"type": "comment", "value": message}},
	}, &result)
	if err != nil {
		return commentResult{}, err
	}
	return commentResult{RevisionID: revisionID, Action: "comment", Posted: true, Published: true, Result: result}, nil
}

func (s *feedbackService) draftInlineReply(revision, parent, message string) (inlineReplyResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return inlineReplyResult{}, err
	}
	comments, err := s.validateInlineComments(revision, []string{parent})
	if err != nil {
		return inlineReplyResult{}, err
	}
	return s.draftInlineReplyValidated(revisionID, comments[0], message)
}

func (s *feedbackService) draftInlineReplyValidated(revisionID int, parent validatedComment, message string) (inlineReplyResult, error) {
	parentPHID := stringValue(parent.Comment["phid"])
	result := inlineReplyResult{
		RevisionID: revisionID, Action: "reply", ParentCommentID: parent.ID,
		ParentCommentPHID: parentPHID,
	}
	if parentPHID == "" {
		return result, fmt.Errorf("inline comment %d has no PHID and cannot be used as a reply parent", parent.ID)
	}
	path := fmt.Sprintf("/differential/comment/inline/edit/%d/", revisionID)
	common := map[string]string{
		"hasContentState": "1", "text": message, "suggestionText": "", "hasSuggestion": "0",
		"on_right": "1", "renderer": "2up", "__wflow__": "true", "__ajax__": "true",
	}
	create := cloneStrings(common)
	create["op"] = "reply"
	create["replyToCommentPHID"] = parentPHID
	created, err := s.web.post(path, create)
	if err != nil {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote mutation outcome is unknown while creating the reply: %w", err),
		}
	}
	payload, _ := mapValue(created["payload"])
	inline, _ := mapValue(payload["inline"])
	replyID, ok := intValue(inline["id"])
	if !ok || replyID < 1 {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote partial failure: inline reply creation returned no comment ID; inspect the revision before retrying"),
		}
	}
	result.DraftCommentID = replyID
	result.CreatedReplyID = replyID
	save := cloneStrings(common)
	save["op"] = "save"
	save["id"] = strconv.Itoa(replyID)
	saved, err := s.web.post(path, save)
	if err != nil {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote partial failure: reply %d was created but could not be saved: %w", replyID, err),
		}
	}
	savedPayload, ok := mapValue(saved["payload"])
	savedInline, inlineOK := mapValue(savedPayload["inline"])
	savedID, idOK := intValue(savedInline["id"])
	if !ok || !inlineOK || !idOK || savedID != replyID {
		dialog := boundedDialogText(savedPayload["dialog"])
		detail := "the save response did not include a rendered inline"
		if dialog != "" {
			detail = "Phabricator returned a dialog: " + dialog
		} else if idOK {
			detail = fmt.Sprintf("the save response identified inline %d instead of %d", savedID, replyID)
		}
		return result, &mutationResultError{
			result: result,
			err: fmt.Errorf(
				"remote partial failure: reply %d was created but its saved state was not confirmed; %s; "+
					"inspect or close any editing inline before submitting",
				replyID, detail,
			),
		}
	}
	result.Saved = true
	result.Draft = true
	return result, nil
}

func (s *feedbackService) reply(revision, parent, message string, done, submit bool) (inlineReplyResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return inlineReplyResult{}, err
	}
	comments, err := s.validateInlineComments(revision, []string{parent})
	if err != nil {
		return inlineReplyResult{}, err
	}
	result, err := s.draftInlineReplyValidated(revisionID, comments[0], message)
	if err != nil {
		if submit {
			result.Submission = unattemptedSubmission(
				revisionID,
				"The reply draft was not created successfully.",
			)
		}
		if done {
			err = fmt.Errorf("%w; parent Done action was not attempted", err)
		}
		if submit || done {
			return result, &mutationResultError{result: result, err: err}
		}
		return result, err
	}
	if done {
		result.Action = "reply+done"
		doneResult, doneErr := s.markDoneValidated(revisionID, []validatedComment{comments[0]})
		if len(doneResult.Comments) > 0 {
			result.Done = &doneResult.Comments[0]
			result.FinalDone = doneResult.Comments[0].FinalDone
		}
		if doneErr != nil {
			if submit {
				result.Submission = unattemptedSubmission(
					revisionID,
					"The parent Done action failed.",
				)
			}
			return result, &mutationResultError{
				result: result,
				err: fmt.Errorf(
					"remote partial failure: reply %d was drafted but comment %d was not marked Done: %w",
					result.CreatedReplyID, comments[0].ID, doneErr,
				),
			}
		}
	}
	if !submit {
		return result, nil
	}
	submission, err := s.submit(revision)
	result.Submission = &submission
	if err != nil {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote partial failure: drafts were created but submission failed: %w", err),
		}
	}
	if !submission.Submitted {
		return result, &mutationResultError{
			result: result,
			err: fmt.Errorf(
				"remote partial failure: reply draft %d was created but Phabricator reported no publishable effect; inspect the revision",
				result.CreatedReplyID,
			),
		}
	}
	result.Draft = false
	result.Published = true
	if result.Done != nil {
		draft, published := false, true
		result.Done.Draft = &draft
		result.Done.Published = &published
	}
	return result, nil
}

func commentActionsHaveDraft(comments []commentAction) bool {
	for _, comment := range comments {
		if boolPointerValue(comment.Draft) {
			return true
		}
	}
	return false
}

func (s *feedbackService) removeComment(revision, target string) (removedCommentResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return removedCommentResult{}, err
	}
	transaction, _, err := s.findComment(revision, target, "comment")
	if err != nil {
		return removedCommentResult{}, err
	}
	if _, err := s.web.post("/transactions/edit/"+stringValue(transaction["phid"])+"/", map[string]string{"text": "", "__form__": "1", "__ajax__": "true"}); err != nil {
		return removedCommentResult{}, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return removedCommentResult{}, err
	}
	confirmed := false
	for _, item := range transactions {
		if stringValue(item["id"]) != stringValue(transaction["id"]) {
			continue
		}
		versions, _ := sliceValue(item["comments"])
		latestVersion := -1
		var latest map[string]any
		for _, raw := range versions {
			version, _ := raw.(map[string]any)
			number, _ := intValue(version["version"])
			if latest == nil || number > latestVersion {
				latest, latestVersion = version, number
			}
		}
		confirmed = latest != nil && boolValue(latest["removed"])
	}
	if !confirmed {
		return removedCommentResult{}, fmt.Errorf("server did not confirm removal of comment %s", target)
	}
	targetID, _ := commentID(target)
	return removedCommentResult{RevisionID: revisionID, Action: "remove-comment", CommentID: targetID, Removed: true}, nil
}

func (s *feedbackService) markDone(revision string, targets []string, submit bool) (commentActionResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return commentActionResult{}, err
	}
	comments, err := s.validateInlineComments(revision, targets)
	if err != nil {
		return commentActionResult{}, err
	}
	result, err := s.markDoneValidated(revisionID, comments)
	if err != nil {
		if submit {
			result.Submission = unattemptedSubmission(
				revisionID,
				"A Done action failed.",
			)
			return result, &mutationResultError{
				result: result,
				err:    err,
			}
		}
		return result, err
	}
	if !submit {
		return result, nil
	}
	if !commentActionsHaveDraft(result.Comments) {
		result.Submission = unattemptedSubmission(
			revisionID,
			"All target comments were already published Done; existing unrelated drafts remain unpublished.",
		)
		return result, nil
	}
	submission, err := s.submit(revision)
	result.Submission = &submission
	if err != nil {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote partial failure: Done drafts were created but submission failed: %w", err),
		}
	}
	if !submission.Submitted {
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("remote partial failure: Done drafts were created but Phabricator reported no publishable effect; inspect the revision"),
		}
	}
	for index := range result.Comments {
		if result.Comments[index].Draft != nil && *result.Comments[index].Draft {
			draft, published := false, true
			result.Comments[index].Draft = &draft
			result.Comments[index].Published = &published
		}
	}
	return result, nil
}

func (s *feedbackService) markDoneValidated(revisionID int, comments []validatedComment) (commentActionResult, error) {
	path := fmt.Sprintf("/differential/comment/inline/edit/%d/", revisionID)
	result := commentActionResult{RevisionID: revisionID, Action: "done", Comments: make([]commentAction, 0, len(comments))}
	for index, comment := range comments {
		identifier := comment.ID
		// Conduit reports both published Done and a pending undo draft as
		// isDone=true, so use the toggle response to establish the final state.
		data := map[string]string{"op": "done", "id": strconv.Itoa(identifier), "__wflow__": "true", "__ajax__": "true"}
		response, err := s.web.post(path, data)
		if err != nil {
			recovery := "The Done mutation outcome is unknown. Inspect this comment before submitting any drafts, then rerun done if needed."
			result.Comments = append(result.Comments, commentAction{
				Action: "done", CommentID: identifier, Recovery: recovery,
			})
			appendNotAttempted(&result, comments[index+1:])
			return result, &mutationResultError{
				result: result,
				err:    fmt.Errorf("remote mutation outcome is unknown while marking inline comment %d Done; %s: %w", identifier, recovery, err),
			}
		}
		checked, draftState, err := doneResponseState(response)
		if err != nil {
			recovery := "The Done response was invalid, so the mutation outcome is unknown. Inspect this comment before submitting any drafts, then rerun done if needed."
			result.Comments = append(result.Comments, commentAction{
				Action: "done", CommentID: identifier, Recovery: recovery,
			})
			appendNotAttempted(&result, comments[index+1:])
			return result, &mutationResultError{
				result: result,
				err:    fmt.Errorf("invalid Done response for inline comment %d; %s: %w", identifier, recovery, err),
			}
		}
		if !checked {
			firstDraftState := draftState
			response, err = s.web.post(path, data)
			if err != nil {
				observedChecked := false
				recovery := doneRecoveryMessage(firstDraftState, true)
				result.Comments = append(result.Comments, commentAction{
					Action: "done", CommentID: identifier,
					ObservedChecked: &observedChecked, ObservedDraftState: &firstDraftState,
					Recovery: recovery,
				})
				appendNotAttempted(&result, comments[index+1:])
				return result, &mutationResultError{
					result: result,
					err:    fmt.Errorf("done retry failed for inline comment %d; %s: %w", identifier, recovery, err),
				}
			}
			checked, draftState, err = doneResponseState(response)
			if err != nil {
				observedChecked := false
				recovery := doneRecoveryMessage(firstDraftState, true)
				result.Comments = append(result.Comments, commentAction{
					Action: "done", CommentID: identifier,
					ObservedChecked: &observedChecked, ObservedDraftState: &firstDraftState,
					Recovery: recovery,
				})
				appendNotAttempted(&result, comments[index+1:])
				return result, &mutationResultError{
					result: result,
					err:    fmt.Errorf("invalid Done retry response for inline comment %d; %s: %w", identifier, recovery, err),
				}
			}
		}
		if !checked {
			observedChecked := false
			recovery := doneRecoveryMessage(draftState, false)
			result.Comments = append(result.Comments, commentAction{
				Action: "done", CommentID: identifier,
				ObservedChecked: &observedChecked, ObservedDraftState: &draftState,
				Recovery: recovery,
			})
			appendNotAttempted(&result, comments[index+1:])
			return result, &mutationResultError{
				result: result,
				err:    fmt.Errorf("server left inline comment %d unchecked; %s", identifier, recovery),
			}
		}
		isDone, draft := true, draftState
		published := !draft
		result.Comments = append(result.Comments, commentAction{
			Action: "done", CommentID: identifier, IsDone: &isDone, FinalDone: &isDone,
			Draft: &draft, Published: &published,
		})
	}
	return result, nil
}

func appendNotAttempted(result *commentActionResult, comments []validatedComment) {
	for _, comment := range comments {
		result.NotAttempted = append(result.NotAttempted, comment.ID)
	}
}

func doneResponseState(response map[string]any) (bool, bool, error) {
	payload, ok := mapValue(response["payload"])
	if !ok {
		return false, false, fmt.Errorf("response payload is missing")
	}
	checked, ok := payload["isChecked"].(bool)
	if !ok {
		return false, false, fmt.Errorf("response payload has no boolean isChecked")
	}
	draftState, ok := payload["draftState"].(bool)
	if !ok {
		return false, false, fmt.Errorf("response payload has no boolean draftState")
	}
	return checked, draftState, nil
}

func doneRecoveryMessage(draftState, retryOutcomeUnknown bool) string {
	qualifier := ""
	if retryOutcomeUnknown {
		qualifier = " The retry outcome is unknown; inspect the comment first."
	}
	if draftState {
		return "The first confirmed state contains a pending undo-Done draft." + qualifier +
			" Rerun done for this comment before submitting any drafts, or clear the pending state in Phabricator."
	}
	return "The first confirmed state cleared the pending Done draft." + qualifier +
		" Rerun done for this comment before submitting."
}

func (s *feedbackService) rate(revision string, targets []string, helpful bool) (commentActionResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return commentActionResult{}, err
	}
	ids, err := s.validateComments(revision, targets, "inline")
	if err != nil {
		return commentActionResult{}, err
	}
	results := make([]commentAction, 0, len(ids))
	for _, identifier := range ids {
		feedbackType := "down"
		if helpful {
			feedbackType = "up"
		}
		response, err := s.web.post("/reviewhelper/feedback/", map[string]string{"commentID": strconv.Itoa(identifier), "feedbackType": feedbackType, "__ajax__": "true"})
		if err != nil {
			return commentActionResult{}, err
		}
		payload, _ := mapValue(response["payload"])
		helpfulness := helpful
		results = append(results, commentAction{CommentID: identifier, Helpful: &helpfulness, Message: payload["message"]})
	}
	return commentActionResult{RevisionID: revisionID, Action: "rate", MozillaReviewHelper: true, Comments: results}, nil
}

func (s *feedbackService) submit(revision string) (submissionResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return submissionResult{}, err
	}
	result := submissionResult{
		RevisionID: revisionID, Action: "submit", Outcome: submissionOutcomeNotAttempted,
	}
	csrf, err := s.web.csrf()
	if err != nil {
		result.Outcome = submissionOutcomeBlocked
		result.Recovery = "The CSRF token could not be loaded."
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("%s: %w", result.Recovery, err),
		}
	}
	result.Attempted = true
	result.Outcome = submissionOutcomeUnknown
	response, err := s.web.post(fmt.Sprintf("/differential/revision/edit/%d/comment/", revisionID), map[string]string{
		"__csrf__": csrf, "__form__": "1", "editengine.actions": "[]", "comment": "", "comment_metadata": "{}", "__ajax__": "true",
	})
	if err != nil {
		result.OutcomeUnknown = true
		result.Recovery = "The submission outcome is unknown. Inspect the revision before retrying."
		return result, &mutationResultError{result: result, err: fmt.Errorf("%s: %w", result.Recovery, err)}
	}
	payload, ok := mapValue(response["payload"])
	if !ok {
		result.OutcomeUnknown = true
		result.Recovery = "The submission response was invalid. Inspect the revision before retrying."
		return result, &mutationResultError{result: result, err: errors.New(result.Recovery)}
	}
	redirect := strings.TrimSpace(stringValue(payload["redirect"]))
	if redirect == "" {
		result.Dialog = boundedDialogText(payload["dialog"])
		if result.Dialog == "" {
			result.OutcomeUnknown = true
			result.Recovery = "The submission response contained no redirect. Inspect the revision before retrying."
			return result, &mutationResultError{
				result: result,
				err:    errors.New(result.Recovery),
			}
		}
		result.Outcome = submissionOutcomeRejected
		result.Recovery = "No drafts were published. Resolve the Phabricator dialog, especially any unsaved inline comment or warning, then retry."
		return result, &mutationResultError{
			result: result,
			err:    fmt.Errorf("submission was not accepted; Phabricator returned a dialog: %s; %s", result.Dialog, result.Recovery),
		}
	}
	result.Outcome = submissionOutcomeSubmitted
	result.Submitted = true
	result.Redirect = redirect
	return result, nil
}

func unattemptedSubmission(revisionID int, recovery string) *submissionResult {
	return &submissionResult{
		RevisionID: revisionID, Action: "submit",
		Outcome: submissionOutcomeNotAttempted, Recovery: recovery,
	}
}

func boundedDialogText(value any) string {
	summary := safe(strings.TrimSpace(stringValue(value)))
	const maxDialogRunes = 500
	runes := []rune(summary)
	if len(runes) > maxDialogRunes {
		summary = string(runes[:maxDialogRunes-3]) + "..."
	}
	return summary
}

func (s *feedbackService) requestAIReview(revision string) (aiReviewResult, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return aiReviewResult{}, err
	}
	response, err := s.web.post(fmt.Sprintf("/reviewhelper/request/%d/", revisionID), map[string]string{"__wflow__": "true", "__ajax__": "true", "__metablock__": "6"})
	if err != nil {
		return aiReviewResult{}, err
	}
	payload, _ := mapValue(response["payload"])
	dialog := stringValue(payload["dialog"])
	status := "response-received"
	if strings.Contains(dialog, "successfully") {
		status = "requested"
	} else if strings.Contains(dialog, "being processed") {
		status = "already-in-progress"
	}
	return aiReviewResult{RevisionID: revisionID, Action: "ai-review", MozillaReviewHelper: true, Status: status}, nil
}

func (s *feedbackService) validateComments(revision string, values []string, expectedType string) ([]int, error) {
	comments, err := s.validateCommentRecords(revision, values, expectedType)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(comments))
	for _, comment := range comments {
		ids = append(ids, comment.ID)
	}
	return ids, nil
}

type validatedComment struct {
	ID      int
	Comment map[string]any
}

func (s *feedbackService) validateInlineComments(revision string, values []string) ([]validatedComment, error) {
	return s.validateCommentRecords(revision, values, "inline")
}

func (s *feedbackService) validateCommentRecords(revision string, values []string, expectedType string) ([]validatedComment, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one comment ID is required")
	}
	ids := make([]int, 0, len(values))
	seen := map[int]bool{}
	for _, value := range values {
		identifier, err := commentID(value)
		if err != nil {
			return nil, err
		}
		if seen[identifier] {
			return nil, fmt.Errorf("comment %d was specified more than once", identifier)
		}
		seen[identifier] = true
		ids = append(ids, identifier)
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return nil, err
	}
	byID := map[int]map[string]any{}
	for _, transaction := range transactions {
		versions, _ := sliceValue(transaction["comments"])
		for _, raw := range versions {
			version, _ := raw.(map[string]any)
			if identifier, ok := intValue(version["id"]); ok {
				byID[identifier] = transaction
			}
		}
	}
	revisionID, _ := revisionNumber(revision)
	result := make([]validatedComment, 0, len(ids))
	for _, identifier := range ids {
		transaction := byID[identifier]
		if transaction == nil {
			return nil, fmt.Errorf("comment %d was not found on D%d", identifier, revisionID)
		}
		actual := stringValue(transaction["type"])
		if actual == "" {
			actual = "non-comment"
		}
		if actual != expectedType {
			return nil, fmt.Errorf("comment %d is a %s transaction, not %s", identifier, actual, expectedType)
		}
		comment := activeComment(transaction)
		if comment == nil {
			return nil, fmt.Errorf("comment %d has been removed", identifier)
		}
		if expectedType == "inline" && stringValue(comment["phid"]) == "" {
			return nil, fmt.Errorf("inline comment %d has no PHID", identifier)
		}
		result = append(result, validatedComment{ID: identifier, Comment: comment})
	}
	return result, nil
}

func (s *feedbackService) findComment(revision, target, expectedType string) (map[string]any, map[string]any, error) {
	identifier, err := commentID(target)
	if err != nil {
		return nil, nil, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return nil, nil, err
	}
	for _, transaction := range transactions {
		versions, _ := sliceValue(transaction["comments"])
		found := false
		for _, raw := range versions {
			version, _ := raw.(map[string]any)
			id, _ := intValue(version["id"])
			found = found || id == identifier
		}
		if !found {
			continue
		}
		actual := stringValue(transaction["type"])
		if actual == "" {
			actual = "non-comment"
		}
		if actual != expectedType {
			return nil, nil, fmt.Errorf("comment %d is a %s transaction, not %s", identifier, actual, expectedType)
		}
		comment := activeComment(transaction)
		if comment == nil {
			return nil, nil, fmt.Errorf("comment %d has been removed", identifier)
		}
		return transaction, comment, nil
	}
	revisionID, _ := revisionNumber(revision)
	return nil, nil, fmt.Errorf("comment %d was not found on D%d", identifier, revisionID)
}

func handle(phid string, handles map[string]map[string]any) *handleInfo {
	if phid == "" {
		return nil
	}
	raw := handles[phid]
	return &handleInfo{
		PHID: phid, Name: raw["name"], FullName: raw["fullName"], Type: raw["type"],
		TypeName: raw["typeName"], Status: raw["status"], URI: raw["uri"],
	}
}

func revisionReviewers(revision map[string]any) ([]map[string]any, error) {
	if revision["attachments"] == nil {
		return []map[string]any{}, nil
	}
	attachments, ok := mapValue(revision["attachments"])
	if !ok {
		return nil, fmt.Errorf("differential.revision.search returned invalid attachments")
	}
	if attachments["reviewers"] == nil {
		return []map[string]any{}, nil
	}
	attachment, ok := mapValue(attachments["reviewers"])
	if !ok {
		return nil, fmt.Errorf("differential.revision.search returned invalid reviewer attachments")
	}
	if attachment["reviewers"] == nil {
		return []map[string]any{}, nil
	}
	reviewers, ok := sliceValue(attachment["reviewers"])
	if !ok {
		return nil, fmt.Errorf("differential.revision.search returned invalid reviewers")
	}
	result := make([]map[string]any, 0, len(reviewers))
	for _, raw := range reviewers {
		reviewer, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("differential.revision.search returned invalid reviewers")
		}
		result = append(result, reviewer)
	}
	return result, nil
}

func revisionStatusValue(value any) revisionStatus {
	if status, ok := value.(map[string]any); ok {
		return revisionStatus{Value: status["value"], Name: status["name"], Color: status["color"]}
	}
	return revisionStatus{Value: value, Name: value}
}

type groupedThreads struct {
	Threads       []thread
	OrphanReplies []feedbackEvent
}

func groupThreads(inline []feedbackEvent) groupedThreads {
	byPHID := map[string]feedbackEvent{}
	roots := map[string]feedbackEvent{}
	var orderedRoots []feedbackEvent
	for _, item := range inline {
		phid := stringValue(item.PHID)
		if phid != "" {
			byPHID[phid] = item
			if item.ReplyToCommentPHID == nil {
				roots[phid] = item
				orderedRoots = append(orderedRoots, item)
			}
		}
	}
	repliesByRoot := map[string][]feedbackEvent{}
	for phid := range roots {
		repliesByRoot[phid] = make([]feedbackEvent, 0)
	}
	orphanReplies := make([]feedbackEvent, 0)
	for _, item := range inline {
		parentPHID := stringValue(item.ReplyToCommentPHID)
		if parentPHID == "" {
			continue
		}
		seen := map[string]bool{stringValue(item.PHID): true}
		ancestor, found := byPHID[parentPHID]
		for found && stringValue(ancestor.ReplyToCommentPHID) != "" {
			ancestorPHID := stringValue(ancestor.PHID)
			if seen[ancestorPHID] {
				found = false
				break
			}
			seen[ancestorPHID] = true
			ancestor, found = byPHID[stringValue(ancestor.ReplyToCommentPHID)]
		}
		rootPHID := ""
		if found {
			rootPHID = stringValue(ancestor.PHID)
		}
		if _, ok := roots[rootPHID]; !ok {
			orphan := item
			orphan.OrphanReason = "missing-or-cyclic-parent"
			orphanReplies = append(orphanReplies, orphan)
			continue
		}
		repliesByRoot[rootPHID] = append(repliesByRoot[rootPHID], item)
	}
	threads := make([]thread, 0, len(orderedRoots))
	for _, root := range orderedRoots {
		phid := stringValue(root.PHID)
		replies := repliesByRoot[phid]
		sortFeedbackEvents(replies)
		threads = append(threads, thread{
			Root: root, Replies: replies, Resolved: boolValue(root.IsDone), OnCurrentDiff: root.OnCurrentDiff,
		})
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return stringValue(threads[i].Root.Created) < stringValue(threads[j].Root.Created)
	})
	sortFeedbackEvents(orphanReplies)
	return groupedThreads{Threads: threads, OrphanReplies: orphanReplies}
}

func timestampValue(value any) any {
	number, ok := intValue(value)
	if !ok {
		return nil
	}
	return timestamp(int64(number))
}

func timestamp(value int64) string {
	return time.Unix(value, 0).UTC().Format("2006-01-02T15:04:05+00:00")
}

func sortFeedbackEvents(items []feedbackEvent) {
	sort.SliceStable(items, func(i, j int) bool {
		return stringValue(items[i].Created) < stringValue(items[j].Created)
	})
}

func cloneStrings(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	maps.Copy(result, source)
	return result
}
