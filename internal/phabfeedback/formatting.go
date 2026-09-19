package phabfeedback

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true)
	idStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightBlue)
	titleStyle   = lipgloss.NewStyle().Bold(true)
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	detailStyle  = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	linkStyle    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlue).Underline(true)
	successStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightGreen)
)

func renderText(command string, result any) (string, error) {
	switch command {
	case "list":
		value, err := typedResult[revisionListResult](result, command)
		return revisionList(value), err
	case "overview":
		value, err := typedResult[overviewResult](result, command)
		return renderOverview(value), err
	case "threads":
		value, err := typedResult[threadsResult](result, command)
		return renderThreads(value), err
	case "timeline":
		value, err := typedResult[timelineResult](result, command)
		return renderTimeline(value), err
	case "comment":
		value, err := typedResult[commentResult](result, command)
		if err != nil {
			return "", err
		}
		return successStyle.Render(fmt.Sprintf("Posted a comment on D%d.", value.RevisionID)), nil
	case "reply":
		value, err := typedResult[inlineReplyResult](result, command)
		return renderInlineReply(value), err
	case "remove-comment":
		value, err := typedResult[removedCommentResult](result, command)
		if err != nil {
			return "", err
		}
		return successStyle.Render(fmt.Sprintf("Removed comment #%d from D%d.", value.CommentID, value.RevisionID)), nil
	case "done":
		value, err := typedResult[commentActionResult](result, command)
		return renderDone(value), err
	case "submit":
		value, err := typedResult[submissionResult](result, command)
		if err != nil {
			return "", err
		}
		return successStyle.Render(fmt.Sprintf("Submitted every pending draft you own on D%d.", value.RevisionID)), nil
	case "rate-helpful":
		value, err := typedResult[commentActionResult](result, command)
		return renderCommentAction(value, "Rated", "helpful"), err
	case "rate-unhelpful":
		value, err := typedResult[commentActionResult](result, command)
		return renderCommentAction(value, "Rated", "unhelpful"), err
	case "ai-review":
		value, err := typedResult[aiReviewResult](result, command)
		return renderAIReview(value), err
	case "doctor":
		value, err := typedResult[doctorResult](result, command)
		return renderDoctor(value), err
	case "batch":
		value, err := typedResult[batchResult](result, command)
		return renderBatch(value), err
	case "verify":
		value, err := typedResult[verificationResult](result, command)
		return renderVerification(value), err
	default:
		return "", fmt.Errorf("text output is not supported for %s", command)
	}
}

func typedResult[T any](result any, command string) (T, error) {
	value, ok := result.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%s returned invalid result data", command)
	}
	return value, nil
}

func renderOverview(result overviewResult) string {
	return revisionSummaryText(result.Summary) + "\n\n" + renderThreads(result.Threads)
}

func renderInlineReply(result inlineReplyResult) string {
	replyID := result.CreatedReplyID
	if replyID == 0 {
		replyID = result.DraftCommentID
	}
	if replyID == 0 {
		return decisionStyle("unresolved").Render(fmt.Sprintf(
			"Could not confirm reply creation for comment #%d on D%d.",
			result.ParentCommentID, result.RevisionID,
		))
	}
	if !result.Saved && !result.Draft && !result.Published {
		return decisionStyle("unresolved").Render(fmt.Sprintf(
			"Created inline reply #%d to comment #%d on D%d, but did not confirm its saved state.",
			replyID, result.ParentCommentID, result.RevisionID,
		))
	}
	state := "Drafted"
	if result.Published {
		state = "Published"
	}
	lines := []string{successStyle.Render(fmt.Sprintf(
		"%s inline reply #%d to comment #%d on D%d.",
		state, replyID, result.ParentCommentID, result.RevisionID,
	))}
	if result.Done != nil {
		switch {
		case result.Done.Recovery != "":
			lines = append(lines, decisionStyle("unresolved").Render(fmt.Sprintf(
				"Done action for comment #%d requires recovery: %s",
				result.ParentCommentID, result.Done.Recovery,
			)))
		case boolPointerValue(result.Done.Draft):
			lines = append(lines, successStyle.Render(fmt.Sprintf("Created a Done draft for comment #%d.", result.ParentCommentID)))
		default:
			lines = append(lines, successStyle.Render(fmt.Sprintf("Confirmed comment #%d Done.", result.ParentCommentID)))
		}
	}
	if result.Submission != nil && result.Submission.Submitted {
		lines = append(lines, successStyle.Render(fmt.Sprintf("Submitted every pending draft you own on D%d.", result.Submission.RevisionID)))
	}
	return strings.Join(lines, "\n")
}

func renderDone(result commentActionResult) string {
	if len(result.Comments) == 0 {
		return decisionStyle("unresolved").Render(fmt.Sprintf("No Done changes were confirmed on D%d.", result.RevisionID))
	}
	drafted := make([]string, 0, len(result.Comments))
	published := make([]string, 0, len(result.Comments))
	confirmed := make([]string, 0, len(result.Comments))
	recovery := make([]string, 0)
	for _, comment := range result.Comments {
		if comment.Recovery != "" {
			recovery = append(recovery, decisionStyle("unresolved").Render(fmt.Sprintf(
				"Done action for comment #%d on D%d requires recovery: %s",
				comment.CommentID, result.RevisionID, comment.Recovery,
			)))
			continue
		}
		identifier := fmt.Sprintf("#%d", comment.CommentID)
		switch {
		case boolPointerValue(comment.Draft):
			drafted = append(drafted, identifier)
		case boolPointerValue(comment.Published):
			published = append(published, identifier)
		default:
			confirmed = append(confirmed, identifier)
		}
	}
	lines := make([]string, 0, 4)
	if len(drafted) > 0 {
		lines = append(lines, successStyle.Render(fmt.Sprintf(
			"Created Done drafts for %s on D%d.", strings.Join(drafted, ", "), result.RevisionID,
		)))
	}
	if len(published) > 0 {
		message := fmt.Sprintf("Confirmed %s already Done on D%d.", strings.Join(published, ", "), result.RevisionID)
		if result.Submission != nil && result.Submission.Submitted {
			message = fmt.Sprintf("Confirmed %s Done on D%d.", strings.Join(published, ", "), result.RevisionID)
		}
		lines = append(lines, successStyle.Render(message))
	}
	if len(confirmed) > 0 {
		lines = append(lines, successStyle.Render(fmt.Sprintf(
			"Confirmed %s Done on D%d.", strings.Join(confirmed, ", "), result.RevisionID,
		)))
	}
	if result.Submission != nil && result.Submission.Submitted {
		lines = append(lines, successStyle.Render(fmt.Sprintf("Submitted every pending draft you own on D%d.", result.RevisionID)))
	}
	lines = append(lines, recovery...)
	return strings.Join(lines, "\n")
}

func renderCommentAction(result commentActionResult, verb, outcome string) string {
	ids := make([]string, 0, len(result.Comments))
	for _, comment := range result.Comments {
		ids = append(ids, fmt.Sprintf("#%d", comment.CommentID))
	}
	return successStyle.Render(fmt.Sprintf("%s %s %s on D%d.", verb, strings.Join(ids, ", "), outcome, result.RevisionID))
}

func renderAIReview(result aiReviewResult) string {
	switch result.Status {
	case "requested":
		return successStyle.Render(fmt.Sprintf("Requested a Review Helper AI review on D%d.", result.RevisionID))
	case "already-in-progress":
		return detailStyle.Render(fmt.Sprintf("A Review Helper AI review is already in progress on D%d.", result.RevisionID))
	default:
		return successStyle.Render(fmt.Sprintf("Review Helper responded to the AI review request for D%d.", result.RevisionID))
	}
}

func revisionList(result revisionListResult) string {
	lines := []string{headerStyle.Render(fmt.Sprintf("%d revisions", result.Count)) + detailStyle.Render(fmt.Sprintf(" (%s, %s)", safe(result.Role), safe(result.Status)))}
	for index, revision := range result.Revisions {
		if index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf(
			"%s  %s  %s",
			idStyle.Render(fmt.Sprintf("D%v", revision.ID)),
			statusStyle(revision.Status).Render(statusText(revision.Status)),
			titleStyle.Render(safe(orDefault(revision.Title, "(untitled)"))),
		))
		reviewerText := make([]string, 0, len(revision.Reviewers))
		for _, reviewer := range revision.Reviewers {
			reviewerStatus := safe(orDefault(reviewer.Decision, "unknown"))
			reviewerText = append(reviewerText, fmt.Sprintf("%s %s", name(&reviewer.Handle), decisionStyle(reviewerStatus).Render(decisionText(reviewerStatus))))
		}
		if len(reviewerText) == 0 {
			reviewerText = []string{"none"}
		}
		lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Author   "), name(revision.Author)))
		lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Reviewers"), strings.Join(reviewerText, ", ")))
		if mergeStatus := renderMergeStatus(revision, false); mergeStatus != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Merge    "), mergeStatus))
		}
		if revision.Modified != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Updated  "), detailStyle.Render(safe(revision.Modified))))
		}
		if revision.URI != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("URL      "), linkStyle.Render(safe(revision.URI))))
		}
	}
	if stringValue(result.Cursor["after"]) != "" {
		lines = append(lines, "", labelStyle.Render("Next cursor  ")+safe(result.Cursor["after"]))
	}
	return strings.Join(lines, "\n")
}

func revisionSummaryText(result revisionSummary) string {
	revision := result.Revision
	feedback := result.Feedback
	reviewerText := make([]string, 0, len(revision.Reviewers))
	for _, reviewer := range revision.Reviewers {
		reviewerStatus := safe(orDefault(reviewer.Decision, "unknown"))
		reviewerText = append(reviewerText, fmt.Sprintf("%s %s", name(&reviewer.Handle), decisionStyle(reviewerStatus).Render(decisionText(reviewerStatus))))
	}
	if len(reviewerText) == 0 {
		reviewerText = []string{"none"}
	}
	lines := []string{
		fmt.Sprintf("%s  %s  %s", idStyle.Render(fmt.Sprintf("D%v", revision.ID)), statusStyle(revision.Status).Render(statusText(revision.Status)), titleStyle.Render(safe(orDefault(revision.Title, "(untitled)")))),
		fmt.Sprintf("%s  %s", labelStyle.Render("Author             "), name(revision.Author)),
		fmt.Sprintf("%s  %s", labelStyle.Render("Reviewers          "), strings.Join(reviewerText, ", ")),
		fmt.Sprintf("%s  %d unresolved, %d resolved, %d replies, %d general comments", labelStyle.Render("Feedback           "), feedback.UnresolvedThreads, feedback.ResolvedThreads, feedback.Replies, feedback.GeneralComments),
		fmt.Sprintf("%s  %d", labelStyle.Render("Older diff comments"), feedback.OlderDiffComments),
	}
	if mergeStatus := renderMergeStatus(revision, true); mergeStatus != "" {
		lines = append(lines, fmt.Sprintf("%s  %s", labelStyle.Render("Merge              "), mergeStatus))
	}
	if revision.URI != nil {
		lines = append(lines, fmt.Sprintf("%s  %s", labelStyle.Render("URL                "), linkStyle.Render(safe(revision.URI))))
	}
	return strings.Join(lines, "\n")
}

func renderThreads(result threadsResult) string {
	lines := []string{headerStyle.Render(fmt.Sprintf("D%d: %d %s threads", result.RevisionID, result.Count, safe(result.State)))}
	for index, item := range result.Threads {
		if index > 0 {
			lines = append(lines, "")
		}
		state := "unresolved"
		if item.Resolved {
			state = "resolved"
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", decisionStyle(state).Render(state), idStyle.Render(fmt.Sprintf("#%v", item.Root.ID)), detailStyle.Render(location(item.Root))))
		lines = append(lines, "  "+safe(orDefault(item.Root.Content, "")))
		for _, reply := range item.Replies {
			lines = append(lines, fmt.Sprintf("  %s %s to %s: %s", labelStyle.Render("Reply"), idStyle.Render(fmt.Sprintf("#%v", reply.ID)), idStyle.Render(fmt.Sprintf("#%v", reply.ReplyToCommentID)), safe(orDefault(reply.Content, ""))))
		}
	}
	for _, orphan := range result.OrphanReplies {
		parent := orphan.ReplyToCommentID
		if parent == nil {
			parent = orphan.ReplyToCommentPHID
		}
		lines = append(lines, "", fmt.Sprintf("%s  %s -> %s", decisionStyle("orphan").Render("orphan reply"), idStyle.Render(fmt.Sprintf("#%v", orphan.ID)), safe(parent)))
		lines = append(lines, "  "+safe(orDefault(orphan.Content, "")))
	}
	return strings.Join(lines, "\n")
}

func renderTimeline(result timelineResult) string {
	lines := []string{headerStyle.Render(fmt.Sprintf("D%d: %d feedback events", result.RevisionID, len(result.Events)))}
	for _, event := range result.Events {
		locationText := ""
		if event.Kind == "inline" {
			locationText = "  " + detailStyle.Render(location(event))
		}
		kind := safe(event.Kind)
		lines = append(lines, fmt.Sprintf("%s  %s%s  %s", decisionStyle(kind).Render(kind), idStyle.Render(fmt.Sprintf("#%v", event.ID)), locationText, safe(orDefault(event.Content, ""))))
	}
	return strings.Join(lines, "\n")
}

func statusStyle(status revisionStatus) lipgloss.Style {
	value := strings.ToLower(stringValue(status.Value))
	if value == "" {
		value = strings.ToLower(stringValue(status.Name))
	}
	return decisionStyle(value)
}

func decisionStyle(value string) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	switch strings.ToLower(value) {
	case "accepted", "approved", "closed", "published", "resolved", "helpful":
		return style.Foreground(lipgloss.BrightGreen)
	case "rejected", "abandoned", "needs revision", "needs-revision", "unresolved", "unhelpful":
		return style.Foreground(lipgloss.BrightRed)
	case "blocking", "orphan", "orphan reply":
		return style.Foreground(lipgloss.BrightMagenta)
	case "needs review", "needs-review", "open", "unknown", "recomputing":
		return style.Foreground(lipgloss.BrightYellow)
	case "clean":
		return style.Foreground(lipgloss.BrightGreen)
	case "conflict":
		return style.Foreground(lipgloss.BrightRed)
	default:
		return style.Foreground(lipgloss.BrightCyan)
	}
}

func decisionText(value string) string {
	if strings.EqualFold(value, "rejected") {
		return "requested changes"
	}
	return value
}

func renderMergeStatus(revision revisionRecord, includeClean bool) string {
	merge := revision.MergeConflictStatus
	if merge == nil {
		return ""
	}
	status := strings.ToLower(stringValue(merge.Status))
	if boolValue(merge.IsStale) {
		return decisionStyle("recomputing").Render("Recomputing")
	}
	if status == "clean" && !includeClean {
		return ""
	}

	var label string
	switch status {
	case "clean":
		label = decisionStyle("clean").Render("Merges cleanly")
	case "conflict":
		label = decisionStyle("conflict").Render("Merge conflict")
	default:
		label = decisionStyle("unknown").Render("Unknown")
	}
	if reason := stringValue(merge.Reason); reason != "" {
		label += detailStyle.Render(" - " + safe(reason))
	}
	return label
}

func statusText(status revisionStatus) string {
	if stringValue(status.Name) != "" {
		return safe(status.Name)
	}
	return safe(orDefault(status.Value, "unknown"))
}

func name(handle *handleInfo) string {
	if handle == nil {
		return "unknown"
	}
	for _, value := range []any{handle.FullName, handle.Name, handle.PHID} {
		if stringValue(value) != "" {
			return safe(value)
		}
	}
	return "unknown"
}

func location(comment feedbackEvent) string {
	path := safe(orDefault(comment.Path, "(unknown path)"))
	if comment.Line == nil {
		return path
	}
	return fmt.Sprintf("%s:%v", path, comment.Line)
}

func renderDoctor(result doctorResult) string {
	lines := []string{headerStyle.Render("Diagnostics for " + result.Host)}
	for _, check := range result.Checks {
		style := successStyle
		if check.Status == "warning" {
			style = decisionStyle("unknown")
		} else if check.Status != "ok" {
			style = decisionStyle("unresolved")
		}
		lines = append(lines, fmt.Sprintf("%s  %s", style.Render(check.Name), check.Message))
	}
	return strings.Join(lines, "\n")
}

func renderBatch(result batchResult) string {
	switch result.State {
	case "planned":
		submission := "without submission"
		if result.Submit {
			submission = "with one final revision-wide submission"
		}
		return fmt.Sprintf("Validated %d planned mutations on D%d %s.", len(result.Mutations), result.RevisionID, submission)
	case "published":
		return successStyle.Render(fmt.Sprintf(
			"Published %d batch mutations and every other pending draft you own on D%d with one submission.",
			len(result.Mutations), result.RevisionID,
		))
	case "partial", "failed":
		if result.Failure == nil {
			return decisionStyle("unresolved").Render(fmt.Sprintf("Batch stopped after an unreported failure on D%d.", result.RevisionID))
		}
		if result.Failure.Action == "submit" {
			return decisionStyle("unresolved").Render(fmt.Sprintf(
				"Batch submission stopped after %d completed mutations on D%d.",
				result.Failure.CompletedMutations, result.RevisionID,
			))
		}
		return decisionStyle("unresolved").Render(fmt.Sprintf(
			"Batch stopped at manifest action %d, mutation %d (%s), after %d completed mutations on D%d.",
			result.Failure.ActionIndex, result.Failure.MutationIndex, result.Failure.Action,
			result.Failure.CompletedMutations, result.RevisionID,
		))
	default:
		return successStyle.Render(fmt.Sprintf("Created %d batch mutation drafts on D%d.", len(result.Mutations), result.RevisionID))
	}
}

func renderVerification(result verificationResult) string {
	var summary string
	if result.Verified {
		summary = successStyle.Render(fmt.Sprintf(
			"Verified %d reply links and %d Conduit Done indicators on D%d.",
			len(result.Replies), len(result.Done), result.RevisionID,
		))
	} else {
		failed := 0
		for _, reply := range result.Replies {
			if !reply.Found || !reply.Linked {
				failed++
			}
		}
		for _, done := range result.Done {
			if !done.Found || !done.ConduitIsDone {
				failed++
			}
		}
		summary = decisionStyle("unresolved").Render(fmt.Sprintf("%d verification checks failed on D%d.", failed, result.RevisionID))
	}
	lines := []string{summary}
	for _, limitation := range result.Limitations {
		lines = append(lines, detailStyle.Render("Limitation: "+limitation))
	}
	return strings.Join(lines, "\n")
}

func safe(value any) string {
	var result strings.Builder
	for _, character := range fmt.Sprint(value) {
		switch character {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if character < 32 || (character >= 127 && character <= 159) || unicode.Is(unicode.Cc, character) {
				fmt.Fprintf(&result, `\x%02x`, character)
			} else {
				result.WriteRune(character)
			}
		}
	}
	return result.String()
}

func orDefault(value any, fallback string) any {
	if value == nil || stringValue(value) == "" {
		return fallback
	}
	return value
}
