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

func renderText(command string, result map[string]any) (string, error) {
	switch command {
	case "list":
		return revisionList(result), nil
	case "overview":
		return renderOverview(result)
	case "threads":
		return renderThreads(result), nil
	case "timeline":
		return renderTimeline(result), nil
	case "comment":
		return successStyle.Render(fmt.Sprintf("Posted a comment on D%v.", result["revision_id"])), nil
	case "reply":
		return renderInlineReply(result), nil
	case "remove-comment":
		return successStyle.Render(fmt.Sprintf("Removed comment #%v from D%v.", result["comment_id"], result["revision_id"])), nil
	case "done":
		return renderCommentAction(result, "Marked", "Done as drafts"), nil
	case "submit":
		return successStyle.Render(fmt.Sprintf("Submitted pending drafts on D%v.", result["revision_id"])), nil
	case "rate-helpful":
		return renderCommentAction(result, "Rated", "helpful"), nil
	case "rate-unhelpful":
		return renderCommentAction(result, "Rated", "unhelpful"), nil
	case "ai-review":
		return renderAIReview(result), nil
	default:
		return "", fmt.Errorf("text output is not supported for %s", command)
	}
}

func renderOverview(result map[string]any) (string, error) {
	summary, ok := mapValue(result["summary"])
	if !ok {
		return "", fmt.Errorf("overview returned an invalid summary")
	}
	threads, ok := mapValue(result["threads"])
	if !ok {
		return "", fmt.Errorf("overview returned invalid threads")
	}
	return revisionSummary(summary) + "\n\n" + renderThreads(threads), nil
}

func renderInlineReply(result map[string]any) string {
	lines := []string{successStyle.Render(fmt.Sprintf(
		"Drafted inline reply #%v to comment #%v on D%v.",
		result["draft_comment_id"],
		result["parent_comment_id"],
		result["revision_id"],
	))}
	if submission, ok := mapValue(result["submission"]); ok {
		lines = append(lines, successStyle.Render(fmt.Sprintf("Submitted pending drafts on D%v.", submission["revision_id"])))
	}
	return strings.Join(lines, "\n")
}

func renderCommentAction(result map[string]any, verb, outcome string) string {
	comments, _ := sliceValue(result["comments"])
	ids := make([]string, 0, len(comments))
	for _, raw := range comments {
		comment, _ := mapValue(raw)
		ids = append(ids, "#"+safe(comment["comment_id"]))
	}
	return successStyle.Render(fmt.Sprintf("%s %s %s on D%v.", verb, strings.Join(ids, ", "), outcome, result["revision_id"]))
}

func renderAIReview(result map[string]any) string {
	switch stringValue(result["status"]) {
	case "requested":
		return successStyle.Render(fmt.Sprintf("Requested a Review Helper AI review on D%v.", result["revision_id"]))
	case "already-in-progress":
		return detailStyle.Render(fmt.Sprintf("A Review Helper AI review is already in progress on D%v.", result["revision_id"]))
	default:
		return successStyle.Render(fmt.Sprintf("Review Helper responded to the AI review request for D%v.", result["revision_id"]))
	}
}

func revisionList(result map[string]any) string {
	lines := []string{headerStyle.Render(fmt.Sprintf("%v revisions", result["count"])) + detailStyle.Render(fmt.Sprintf(" (%s, %s)", safe(result["role"]), safe(result["status"])))}
	revisions, _ := sliceValue(result["revisions"])
	for index, raw := range revisions {
		if index > 0 {
			lines = append(lines, "")
		}
		revision, _ := mapValue(raw)
		status, _ := mapValue(revision["status"])
		lines = append(lines, fmt.Sprintf(
			"%s  %s  %s",
			idStyle.Render(fmt.Sprintf("D%v", revision["id"])),
			statusStyle(status).Render(statusText(status)),
			titleStyle.Render(safe(orDefault(revision["title"], "(untitled)"))),
		))
		reviewers, _ := sliceValue(revision["reviewers"])
		reviewerText := make([]string, 0, len(reviewers))
		for _, rawReviewer := range reviewers {
			reviewer, _ := mapValue(rawReviewer)
			reviewerStatus := safe(orDefault(reviewer["status"], "unknown"))
			reviewerText = append(reviewerText, fmt.Sprintf("%s %s", name(reviewer), decisionStyle(reviewerStatus).Render(decisionText(reviewerStatus))))
		}
		if len(reviewerText) == 0 {
			reviewerText = []string{"none"}
		}
		author, _ := mapValue(revision["author"])
		lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Author   "), name(author)))
		lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Reviewers"), strings.Join(reviewerText, ", ")))
		if mergeStatus := renderMergeStatus(revision, false); mergeStatus != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Merge    "), mergeStatus))
		}
		if revision["modified"] != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("Updated  "), detailStyle.Render(safe(revision["modified"]))))
		}
		if revision["uri"] != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", labelStyle.Render("URL      "), linkStyle.Render(safe(revision["uri"]))))
		}
	}
	cursor, _ := mapValue(result["cursor"])
	if stringValue(cursor["after"]) != "" {
		lines = append(lines, "", labelStyle.Render("Next cursor  ")+safe(cursor["after"]))
	}
	return strings.Join(lines, "\n")
}

func revisionSummary(result map[string]any) string {
	revision, _ := mapValue(result["revision"])
	feedback, _ := mapValue(result["feedback"])
	status, _ := mapValue(revision["status"])
	reviewers, _ := sliceValue(revision["reviewers"])
	reviewerText := make([]string, 0, len(reviewers))
	for _, raw := range reviewers {
		reviewer, _ := mapValue(raw)
		reviewerStatus := safe(orDefault(reviewer["status"], "unknown"))
		reviewerText = append(reviewerText, fmt.Sprintf("%s %s", name(reviewer), decisionStyle(reviewerStatus).Render(decisionText(reviewerStatus))))
	}
	if len(reviewerText) == 0 {
		reviewerText = []string{"none"}
	}
	author, _ := mapValue(revision["author"])
	lines := []string{
		fmt.Sprintf("%s  %s  %s", idStyle.Render(fmt.Sprintf("D%v", revision["id"])), statusStyle(status).Render(statusText(status)), titleStyle.Render(safe(orDefault(revision["title"], "(untitled)")))),
		fmt.Sprintf("%s  %s", labelStyle.Render("Author             "), name(author)),
		fmt.Sprintf("%s  %s", labelStyle.Render("Reviewers          "), strings.Join(reviewerText, ", ")),
		fmt.Sprintf("%s  %v unresolved, %v resolved, %v replies, %v general comments", labelStyle.Render("Feedback           "), feedback["unresolved_threads"], feedback["resolved_threads"], feedback["replies"], feedback["general_comments"]),
		fmt.Sprintf("%s  %v", labelStyle.Render("Older diff comments"), feedback["older_diff_comments"]),
	}
	if mergeStatus := renderMergeStatus(revision, true); mergeStatus != "" {
		lines = append(lines, fmt.Sprintf("%s  %s", labelStyle.Render("Merge              "), mergeStatus))
	}
	if revision["uri"] != nil {
		lines = append(lines, fmt.Sprintf("%s  %s", labelStyle.Render("URL                "), linkStyle.Render(safe(revision["uri"]))))
	}
	return strings.Join(lines, "\n")
}

func renderThreads(result map[string]any) string {
	lines := []string{headerStyle.Render(fmt.Sprintf("D%v: %v %s threads", result["revision_id"], result["count"], safe(result["state"])))}
	threads, _ := sliceValue(result["threads"])
	for index, raw := range threads {
		if index > 0 {
			lines = append(lines, "")
		}
		thread, _ := mapValue(raw)
		root, _ := mapValue(thread["root"])
		state := "unresolved"
		if boolValue(thread["resolved"]) {
			state = "resolved"
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", decisionStyle(state).Render(state), idStyle.Render(fmt.Sprintf("#%v", root["id"])), detailStyle.Render(location(root))))
		lines = append(lines, "  "+safe(orDefault(root["content"], "")))
		replies, _ := sliceValue(thread["replies"])
		for _, rawReply := range replies {
			reply, _ := mapValue(rawReply)
			lines = append(lines, fmt.Sprintf("  %s %s to %s: %s", labelStyle.Render("Reply"), idStyle.Render(fmt.Sprintf("#%v", reply["id"])), idStyle.Render(fmt.Sprintf("#%v", reply["reply_to_comment_id"])), safe(orDefault(reply["content"], ""))))
		}
	}
	orphans, _ := sliceValue(result["orphan_replies"])
	for _, raw := range orphans {
		orphan, _ := mapValue(raw)
		parent := orphan["reply_to_comment_id"]
		if parent == nil {
			parent = orphan["reply_to_comment_phid"]
		}
		lines = append(lines, "", fmt.Sprintf("%s  %s -> %s", decisionStyle("orphan").Render("orphan reply"), idStyle.Render(fmt.Sprintf("#%v", orphan["id"])), safe(parent)))
		lines = append(lines, "  "+safe(orDefault(orphan["content"], "")))
	}
	return strings.Join(lines, "\n")
}

func renderTimeline(result map[string]any) string {
	events, _ := sliceValue(result["events"])
	lines := []string{headerStyle.Render(fmt.Sprintf("D%v: %d feedback events", result["revision_id"], len(events)))}
	for _, raw := range events {
		event, _ := mapValue(raw)
		locationText := ""
		if stringValue(event["kind"]) == "inline" {
			locationText = "  " + detailStyle.Render(location(event))
		}
		kind := safe(event["kind"])
		lines = append(lines, fmt.Sprintf("%s  %s%s  %s", decisionStyle(kind).Render(kind), idStyle.Render(fmt.Sprintf("#%v", event["id"])), locationText, safe(orDefault(event["content"], ""))))
	}
	return strings.Join(lines, "\n")
}

func statusStyle(status map[string]any) lipgloss.Style {
	value := strings.ToLower(stringValue(status["value"]))
	if value == "" {
		value = strings.ToLower(stringValue(status["name"]))
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

func renderMergeStatus(revision map[string]any, includeClean bool) string {
	merge, ok := mapValue(revision["merge_conflict_status"])
	if !ok {
		return ""
	}
	status := strings.ToLower(stringValue(merge["status"]))
	if boolValue(merge["is_stale"]) {
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
	if reason := stringValue(merge["reason"]); reason != "" {
		label += detailStyle.Render(" - " + safe(reason))
	}
	return label
}

func statusText(status map[string]any) string {
	if status == nil {
		return "unknown"
	}
	if stringValue(status["name"]) != "" {
		return safe(status["name"])
	}
	return safe(orDefault(status["value"], "unknown"))
}

func name(handle map[string]any) string {
	if handle == nil {
		return "unknown"
	}
	for _, key := range []string{"full_name", "name", "phid"} {
		if stringValue(handle[key]) != "" {
			return safe(handle[key])
		}
	}
	return "unknown"
}

func location(comment map[string]any) string {
	path := safe(orDefault(comment["path"], "(unknown path)"))
	if comment["line"] == nil {
		return path
	}
	return fmt.Sprintf("%s:%v", path, comment["line"])
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
