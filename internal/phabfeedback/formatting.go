package phabfeedback

import (
	"fmt"
	"strings"
	"unicode"
)

func renderText(command string, result map[string]any) (string, error) {
	switch command {
	case "list":
		return revisionList(result), nil
	case "show":
		return revisionSummary(result), nil
	case "threads":
		return renderThreads(result), nil
	case "timeline":
		return renderTimeline(result), nil
	case "comment":
		return fmt.Sprintf("Posted a comment on D%v.", result["revision_id"]), nil
	case "reply-inline":
		return renderInlineReply(result), nil
	case "remove-comment":
		return fmt.Sprintf("Removed comment #%v from D%v.", result["comment_id"], result["revision_id"]), nil
	case "mark-done":
		return renderCommentAction(result, "Marked", "Done as drafts"), nil
	case "submit":
		return fmt.Sprintf("Submitted pending drafts on D%v.", result["revision_id"]), nil
	case "mark-helpful":
		return renderCommentAction(result, "Rated", "helpful"), nil
	case "mark-unhelpful":
		return renderCommentAction(result, "Rated", "unhelpful"), nil
	case "request-ai-review":
		return renderAIReview(result), nil
	default:
		return "", fmt.Errorf("Text output is not supported for %s", command)
	}
}

func renderInlineReply(result map[string]any) string {
	lines := []string{fmt.Sprintf(
		"Drafted inline reply #%v to comment #%v on D%v.",
		result["draft_comment_id"],
		result["parent_comment_id"],
		result["revision_id"],
	)}
	if submission, ok := mapValue(result["submission"]); ok {
		lines = append(lines, fmt.Sprintf("Submitted pending drafts on D%v.", submission["revision_id"]))
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
	return fmt.Sprintf("%s %s %s on D%v.", verb, strings.Join(ids, ", "), outcome, result["revision_id"])
}

func renderAIReview(result map[string]any) string {
	switch stringValue(result["status"]) {
	case "requested":
		return fmt.Sprintf("Requested a Review Helper AI review on D%v.", result["revision_id"])
	case "already-in-progress":
		return fmt.Sprintf("A Review Helper AI review is already in progress on D%v.", result["revision_id"])
	default:
		return fmt.Sprintf("Review Helper responded to the AI review request for D%v.", result["revision_id"])
	}
}

func revisionList(result map[string]any) string {
	lines := []string{fmt.Sprintf("%v revisions (%s, %s)", result["count"], safe(result["role"]), safe(result["status"]))}
	revisions, _ := sliceValue(result["revisions"])
	for _, raw := range revisions {
		revision, _ := mapValue(raw)
		status, _ := mapValue(revision["status"])
		lines = append(lines, fmt.Sprintf("D%v [%s] %s", revision["id"], statusText(status), safe(orDefault(revision["title"], "(untitled)"))))
		reviewers, _ := sliceValue(revision["reviewers"])
		reviewerText := make([]string, 0, len(reviewers))
		for _, rawReviewer := range reviewers {
			reviewer, _ := mapValue(rawReviewer)
			reviewerText = append(reviewerText, fmt.Sprintf("%s [%s]", name(reviewer), safe(orDefault(reviewer["status"], "unknown"))))
		}
		if len(reviewerText) == 0 {
			reviewerText = []string{"none"}
		}
		author, _ := mapValue(revision["author"])
		lines = append(lines, fmt.Sprintf("  author: %s; reviewers: %s", name(author), strings.Join(reviewerText, ", ")))
		if revision["modified"] != nil {
			lines = append(lines, "  updated: "+safe(revision["modified"]))
		}
		if revision["uri"] != nil {
			lines = append(lines, "  "+safe(revision["uri"]))
		}
	}
	cursor, _ := mapValue(result["cursor"])
	if stringValue(cursor["after"]) != "" {
		lines = append(lines, "next cursor: "+safe(cursor["after"]))
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
		reviewerText = append(reviewerText, fmt.Sprintf("%s [%s]", name(reviewer), safe(orDefault(reviewer["status"], "unknown"))))
	}
	if len(reviewerText) == 0 {
		reviewerText = []string{"none"}
	}
	author, _ := mapValue(revision["author"])
	lines := []string{
		fmt.Sprintf("D%v [%s] %s", revision["id"], statusText(status), safe(orDefault(revision["title"], "(untitled)"))),
		"author: " + name(author),
		"reviewers: " + strings.Join(reviewerText, ", "),
		fmt.Sprintf("feedback: %v unresolved, %v resolved, %v replies, %v general comments", feedback["unresolved_threads"], feedback["resolved_threads"], feedback["replies"], feedback["general_comments"]),
		fmt.Sprintf("older diff comments: %v", feedback["older_diff_comments"]),
	}
	if revision["uri"] != nil {
		lines = append(lines, safe(revision["uri"]))
	}
	return strings.Join(lines, "\n")
}

func renderThreads(result map[string]any) string {
	lines := []string{fmt.Sprintf("D%v: %v %s threads", result["revision_id"], result["count"], safe(result["state"]))}
	threads, _ := sliceValue(result["threads"])
	for _, raw := range threads {
		thread, _ := mapValue(raw)
		root, _ := mapValue(thread["root"])
		state := "unresolved"
		if boolValue(thread["resolved"]) {
			state = "resolved"
		}
		lines = append(lines, fmt.Sprintf("[%s] #%v %s", state, root["id"], location(root)))
		lines = append(lines, "  "+safe(orDefault(root["content"], "")))
		replies, _ := sliceValue(thread["replies"])
		for _, rawReply := range replies {
			reply, _ := mapValue(rawReply)
			lines = append(lines, fmt.Sprintf("  reply #%v to #%v: %s", reply["id"], reply["reply_to_comment_id"], safe(orDefault(reply["content"], ""))))
		}
	}
	orphans, _ := sliceValue(result["orphan_replies"])
	for _, raw := range orphans {
		orphan, _ := mapValue(raw)
		parent := orphan["reply_to_comment_id"]
		if parent == nil {
			parent = orphan["reply_to_comment_phid"]
		}
		lines = append(lines, fmt.Sprintf("[orphan reply] #%v -> %s", orphan["id"], safe(parent)))
		lines = append(lines, "  "+safe(orDefault(orphan["content"], "")))
	}
	return strings.Join(lines, "\n")
}

func renderTimeline(result map[string]any) string {
	events, _ := sliceValue(result["events"])
	lines := []string{fmt.Sprintf("D%v: %d feedback events", result["revision_id"], len(events))}
	for _, raw := range events {
		event, _ := mapValue(raw)
		locationText := ""
		if stringValue(event["kind"]) == "inline" {
			locationText = " " + location(event)
		}
		lines = append(lines, fmt.Sprintf("[%s] #%v%s: %s", safe(event["kind"]), event["id"], locationText, safe(orDefault(event["content"], ""))))
	}
	return strings.Join(lines, "\n")
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
