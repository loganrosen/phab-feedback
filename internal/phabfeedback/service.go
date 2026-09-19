package phabfeedback

import (
	"fmt"
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
		return 0, fmt.Errorf("Invalid revision identifier: %s", revision)
	}
	return number, nil
}

func commentID(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("Invalid comment ID: %s", value)
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

func (s *feedbackService) timeline(revision string) (map[string]any, error) {
	revisionID, _, currentDiff, err := s.revisionContext(revision, false)
	if err != nil {
		return nil, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return nil, err
	}
	return buildTimeline(revisionID, currentDiff, transactions), nil
}

func (s *feedbackService) listRevisions(role, status string, modifiedAfter *int64, limit int, after string) (map[string]any, error) {
	roleConstraints := map[string]string{
		"responsible": "responsiblePHIDs",
		"authored":    "authorPHIDs",
		"reviewing":   "reviewerPHIDs",
	}
	constraint := roleConstraints[role]
	if constraint == "" {
		return nil, fmt.Errorf("Unsupported revision role: %s", role)
	}
	if status != "open" && status != "closed" && status != "all" {
		return nil, fmt.Errorf("Unsupported revision status: %s", status)
	}
	if limit < 1 {
		return nil, fmt.Errorf("Revision limit must be positive")
	}
	var viewer struct {
		PHID     string `json:"phid"`
		UserName string `json:"userName"`
		RealName string `json:"realName"`
	}
	if err := s.conduit.call("user.whoami", map[string]any{}, &viewer); err != nil {
		return nil, err
	}
	if viewer.PHID == "" {
		return nil, fmt.Errorf("user.whoami returned invalid user data")
	}
	constraints := map[string]any{constraint: []string{viewer.PHID}}
	if status != "all" {
		constraints["statuses"] = []string{status + "()"}
	}
	if modifiedAfter != nil {
		constraints["modifiedStart"] = *modifiedAfter
	}
	search, err := s.conduit.search("differential.revision.search", constraints, map[string]any{"reviewers": true}, "updated", after, limit)
	if network, ok := err.(*networkError); ok && status != "all" && network.status == 406 {
		constraints["statuses"] = fallbackStatusConstraints[status]
		search, err = s.conduit.search("differential.revision.search", constraints, map[string]any{"reviewers": true}, "updated", after, limit)
	}
	if err != nil {
		return nil, err
	}
	revisions := search.Data
	handles, err := s.hydrateRevisionHandles(revisions)
	if err != nil {
		return nil, err
	}
	normalized := make([]any, 0, len(revisions))
	for _, revision := range revisions {
		item, err := normalizeRevision(revision, handles)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, item)
	}
	var modified any
	if modifiedAfter != nil {
		modified = timestamp(*modifiedAfter)
	}
	return map[string]any{
		"viewer": map[string]any{
			"phid":     viewer.PHID,
			"username": viewer.UserName,
			"name":     viewer.RealName,
		},
		"role":           role,
		"status":         status,
		"modified_after": modified,
		"count":          len(normalized),
		"cursor":         search.Cursor,
		"revisions":      normalized,
	}, nil
}

func (s *feedbackService) show(revision string) (map[string]any, error) {
	revisionID, rawRevision, currentDiff, err := s.revisionContext(revision, true)
	if err != nil {
		return nil, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return nil, err
	}
	timeline := buildTimeline(revisionID, currentDiff, transactions)
	inline, _ := objectSlice(timeline["inline_comments"], "timeline")
	grouped := groupThreads(inline)
	threads, _ := objectSlice(grouped["threads"], "threads")
	orphans, _ := objectSlice(grouped["orphan_replies"], "orphan replies")
	unresolved, resolved, replies, older := 0, 0, 0, 0
	for _, thread := range threads {
		if boolValue(thread["resolved"]) {
			resolved++
		} else {
			unresolved++
		}
	}
	for _, item := range inline {
		if item["reply_to_comment_phid"] != nil {
			replies++
		}
		if !boolValue(item["on_current_diff"]) {
			older++
		}
	}
	handles, err := s.hydrateRevisionHandles([]map[string]any{rawRevision})
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeRevision(rawRevision, handles)
	if err != nil {
		return nil, err
	}
	general, _ := sliceValue(timeline["general_comments"])
	return map[string]any{
		"revision":     normalized,
		"current_diff": timeline["current_diff"],
		"feedback": map[string]any{
			"general_comments":    len(general),
			"inline_comments":     len(inline),
			"root_threads":        len(threads),
			"unresolved_threads":  unresolved,
			"resolved_threads":    resolved,
			"replies":             replies,
			"older_diff_comments": older,
			"orphan_replies":      len(orphans),
		},
	}, nil
}

func (s *feedbackService) threads(revision, state string, currentDiffOnly bool) (map[string]any, error) {
	if state != "unresolved" && state != "resolved" && state != "all" {
		return nil, fmt.Errorf("Unsupported thread state: %s", state)
	}
	timeline, err := s.timeline(revision)
	if err != nil {
		return nil, err
	}
	inline, _ := objectSlice(timeline["inline_comments"], "timeline")
	grouped := groupThreads(inline)
	allThreads, _ := objectSlice(grouped["threads"], "threads")
	allOrphans, _ := objectSlice(grouped["orphan_replies"], "orphan replies")
	threads := make([]any, 0, len(allThreads))
	for _, item := range allThreads {
		resolved := boolValue(item["resolved"])
		if state == "unresolved" && resolved || state == "resolved" && !resolved {
			continue
		}
		root, _ := mapValue(item["root"])
		if currentDiffOnly && !boolValue(root["on_current_diff"]) {
			continue
		}
		threads = append(threads, item)
	}
	orphans := make([]any, 0, len(allOrphans))
	for _, item := range allOrphans {
		if !currentDiffOnly || boolValue(item["on_current_diff"]) {
			orphans = append(orphans, item)
		}
	}
	return map[string]any{
		"revision_id":       timeline["revision_id"],
		"current_diff":      timeline["current_diff"],
		"state":             state,
		"current_diff_only": currentDiffOnly,
		"count":             len(threads),
		"threads":           threads,
		"orphan_replies":    orphans,
	}, nil
}

func buildTimeline(revisionID int, currentDiff map[string]any, transactions []map[string]any) map[string]any {
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
	var general, inline []map[string]any
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
		base := map[string]any{
			"kind":             map[bool]string{true: "general", false: "inline"}[kind == "comment"],
			"id":               comment["id"],
			"phid":             comment["phid"],
			"transaction_id":   transaction["id"],
			"transaction_phid": transaction["phid"],
			"created":          timestampValue(comment["dateCreated"]),
			"content":          content["raw"],
		}
		if kind == "comment" {
			general = append(general, base)
			continue
		}
		fields, _ := mapValue(transaction["fields"])
		diff, _ := mapValue(fields["diff"])
		parentPHID := fields["replyToCommentPHID"]
		item := cloneMap(base)
		item["diff_id"] = diff["id"]
		item["diff_phid"] = diff["phid"]
		item["on_current_diff"] = stringValue(diff["phid"]) == currentDiffPHID
		item["path"] = fields["path"]
		item["line"] = fields["line"]
		item["is_done"] = fields["isDone"]
		item["reply_to_comment_id"] = byPHID[stringValue(parentPHID)]
		item["reply_to_comment_phid"] = parentPHID
		inline = append(inline, item)
	}
	sortItems(general)
	sortItems(inline)
	events := append(append([]map[string]any{}, general...), inline...)
	sortItems(events)
	fields, _ := mapValue(currentDiff["fields"])
	return map[string]any{
		"revision_id": revisionID,
		"current_diff": map[string]any{
			"id":      currentDiff["id"],
			"phid":    currentDiffPHID,
			"created": timestampValue(fields["dateCreated"]),
		},
		"events":           mapsToAny(events),
		"general_comments": mapsToAny(general),
		"inline_comments":  mapsToAny(inline),
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
		return 0, nil, nil, fmt.Errorf("D%d was not found", revisionID)
	}
	revisionData := revisions[0]
	fields, ok := mapValue(revisionData["fields"])
	if !ok {
		return 0, nil, nil, fmt.Errorf("differential.revision.search returned invalid fields")
	}
	diffPHID := stringValue(fields["diffPHID"])
	if diffPHID == "" {
		return 0, nil, nil, fmt.Errorf("D%d returned no current diff", revisionID)
	}
	var diffSearch searchPage
	if err := s.conduit.call("differential.diff.search", map[string]any{"constraints": map[string]any{"phids": []string{diffPHID}}}, &diffSearch); err != nil {
		return 0, nil, nil, err
	}
	diffs := diffSearch.Data
	if len(diffs) == 0 {
		return 0, nil, nil, fmt.Errorf("Current diff for D%d was not found", revisionID)
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
		for phid, handle := range result {
			handles[phid] = handle
		}
	}
	return handles, nil
}

func normalizeRevision(revision map[string]any, handles map[string]map[string]any) (map[string]any, error) {
	fields, ok := mapValue(revision["fields"])
	if !ok {
		return nil, fmt.Errorf("differential.revision.search returned invalid fields")
	}
	reviewers, err := revisionReviewers(revision)
	if err != nil {
		return nil, err
	}
	normalizedReviewers := make([]any, 0, len(reviewers))
	for _, reviewer := range reviewers {
		item := handle(stringValue(reviewer["reviewerPHID"]), handles)
		if item == nil {
			item = map[string]any{}
		}
		item["reviewer_phid"] = reviewer["reviewerPHID"]
		item["status"] = reviewer["status"]
		item["is_blocking"] = reviewer["isBlocking"]
		item["actor"] = handle(stringValue(reviewer["actorPHID"]), handles)
		normalizedReviewers = append(normalizedReviewers, item)
	}
	return map[string]any{
		"id":                revision["id"],
		"phid":              revision["phid"],
		"title":             fields["title"],
		"uri":               fields["uri"],
		"status":            revisionStatus(fields["status"]),
		"is_draft":          fields["isDraft"],
		"author":            handle(stringValue(fields["authorPHID"]), handles),
		"repository":        handle(stringValue(fields["repositoryPHID"]), handles),
		"reviewers":         normalizedReviewers,
		"created":           timestampValue(fields["dateCreated"]),
		"modified":          timestampValue(fields["dateModified"]),
		"current_diff_phid": fields["diffPHID"],
	}, nil
}

func (s *feedbackService) postComment(revision, message string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	var result any
	err = s.conduit.call("differential.revision.edit", map[string]any{
		"objectIdentifier": fmt.Sprintf("D%d", revisionID),
		"transactions":     []map[string]any{{"type": "comment", "value": message}},
	}, &result)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision_id": revisionID, "posted": true, "result": result}, nil
}

func (s *feedbackService) draftInlineReply(revision, parent, message string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	_, parentComment, err := s.findComment(revision, parent, "inline")
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/differential/comment/inline/edit/%d/", revisionID)
	common := map[string]string{
		"hasContentState": "1", "text": message, "suggestionText": "", "hasSuggestion": "0",
		"on_right": "1", "renderer": "2up", "__wflow__": "true", "__ajax__": "true",
	}
	create := cloneStrings(common)
	create["op"] = "reply"
	create["replyToCommentPHID"] = stringValue(parentComment["phid"])
	created, err := s.web.post(path, create)
	if err != nil {
		return nil, err
	}
	payload, _ := mapValue(created["payload"])
	inline, _ := mapValue(payload["inline"])
	replyID, ok := intValue(inline["id"])
	if !ok || replyID < 1 {
		return nil, fmt.Errorf("Inline reply creation returned no comment ID")
	}
	save := cloneStrings(common)
	save["op"] = "save"
	save["id"] = strconv.Itoa(replyID)
	if _, err := s.web.post(path, save); err != nil {
		return nil, err
	}
	parentID, _ := commentID(parent)
	return map[string]any{
		"revision_id":         revisionID,
		"parent_comment_id":   parentID,
		"parent_comment_phid": parentComment["phid"],
		"draft_comment_id":    replyID,
		"draft":               true,
	}, nil
}

func (s *feedbackService) removeComment(revision, target string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	transaction, _, err := s.findComment(revision, target, "comment")
	if err != nil {
		return nil, err
	}
	if _, err := s.web.post("/transactions/edit/"+stringValue(transaction["phid"])+"/", map[string]string{"text": "", "__form__": "1", "__ajax__": "true"}); err != nil {
		return nil, err
	}
	transactions, err := s.revisionTransactions(revision)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("Server did not confirm removal of comment %s", target)
	}
	targetID, _ := commentID(target)
	return map[string]any{"revision_id": revisionID, "comment_id": targetID, "removed": true}, nil
}

func (s *feedbackService) markDone(revision string, targets []string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	ids, err := s.validateComments(revision, targets, "inline")
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/differential/comment/inline/edit/%d/", revisionID)
	results := make([]any, 0, len(ids))
	for _, identifier := range ids {
		data := map[string]string{"op": "done", "id": strconv.Itoa(identifier), "__wflow__": "true", "__ajax__": "true"}
		response, err := s.web.post(path, data)
		if err != nil {
			return nil, err
		}
		payload, _ := mapValue(response["payload"])
		if !boolValue(payload["isChecked"]) {
			response, err = s.web.post(path, data)
			if err != nil {
				return nil, err
			}
			payload, _ = mapValue(response["payload"])
		}
		if !boolValue(payload["isChecked"]) {
			return nil, fmt.Errorf("Server did not mark inline comment %d Done", identifier)
		}
		results = append(results, map[string]any{"comment_id": identifier, "is_done": true, "draft": boolValue(payload["draftState"])})
	}
	return map[string]any{"revision_id": revisionID, "comments": results}, nil
}

func (s *feedbackService) rate(revision string, targets []string, helpful bool) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	ids, err := s.validateComments(revision, targets, "inline")
	if err != nil {
		return nil, err
	}
	results := make([]any, 0, len(ids))
	for _, identifier := range ids {
		feedbackType := "down"
		if helpful {
			feedbackType = "up"
		}
		response, err := s.web.post("/reviewhelper/feedback/", map[string]string{"commentID": strconv.Itoa(identifier), "feedbackType": feedbackType, "__ajax__": "true"})
		if err != nil {
			return nil, err
		}
		payload, _ := mapValue(response["payload"])
		results = append(results, map[string]any{"comment_id": identifier, "helpful": helpful, "message": payload["message"]})
	}
	return map[string]any{"revision_id": revisionID, "mozilla_review_helper": true, "comments": results}, nil
}

func (s *feedbackService) submit(revision string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	csrf, err := s.web.csrf()
	if err != nil {
		return nil, err
	}
	response, err := s.web.post(fmt.Sprintf("/differential/revision/edit/%d/comment/", revisionID), map[string]string{
		"__csrf__": csrf, "__form__": "1", "editengine.actions": "[]", "comment": "", "comment_metadata": "{}", "__ajax__": "true",
	})
	if err != nil {
		return nil, err
	}
	payload, _ := mapValue(response["payload"])
	return map[string]any{"revision_id": revisionID, "submitted": true, "redirect": payload["redirect"]}, nil
}

func (s *feedbackService) requestAIReview(revision string) (map[string]any, error) {
	revisionID, err := revisionNumber(revision)
	if err != nil {
		return nil, err
	}
	response, err := s.web.post(fmt.Sprintf("/reviewhelper/request/%d/", revisionID), map[string]string{"__wflow__": "true", "__ajax__": "true", "__metablock__": "6"})
	if err != nil {
		return nil, err
	}
	payload, _ := mapValue(response["payload"])
	dialog := stringValue(payload["dialog"])
	status := "response-received"
	if strings.Contains(dialog, "successfully") {
		status = "requested"
	} else if strings.Contains(dialog, "being processed") {
		status = "already-in-progress"
	}
	return map[string]any{"revision_id": revisionID, "mozilla_review_helper": true, "status": status}, nil
}

func (s *feedbackService) validateComments(revision string, values []string, expectedType string) ([]int, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("At least one comment ID is required")
	}
	ids := make([]int, 0, len(values))
	for _, value := range values {
		identifier, err := commentID(value)
		if err != nil {
			return nil, err
		}
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
	for _, identifier := range ids {
		transaction := byID[identifier]
		if transaction == nil {
			return nil, fmt.Errorf("Comment %d was not found on D%d", identifier, revisionID)
		}
		actual := stringValue(transaction["type"])
		if actual == "" {
			actual = "non-comment"
		}
		if actual != expectedType {
			return nil, fmt.Errorf("Comment %d is a %s transaction, not %s", identifier, actual, expectedType)
		}
		if activeComment(transaction) == nil {
			return nil, fmt.Errorf("Comment %d has been removed", identifier)
		}
	}
	return ids, nil
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
			return nil, nil, fmt.Errorf("Comment %d is a %s transaction, not %s", identifier, actual, expectedType)
		}
		comment := activeComment(transaction)
		if comment == nil {
			return nil, nil, fmt.Errorf("Comment %d has been removed", identifier)
		}
		return transaction, comment, nil
	}
	revisionID, _ := revisionNumber(revision)
	return nil, nil, fmt.Errorf("Comment %d was not found on D%d", identifier, revisionID)
}

func objectSlice(value any, method string) ([]map[string]any, error) {
	items, ok := sliceValue(value)
	if !ok {
		return nil, fmt.Errorf("%s returned invalid result data", method)
	}
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s returned an invalid result item", method)
		}
		result = append(result, item)
	}
	return result, nil
}

func handle(phid string, handles map[string]map[string]any) map[string]any {
	if phid == "" {
		return nil
	}
	raw := handles[phid]
	return map[string]any{
		"phid": phid, "name": raw["name"], "full_name": raw["fullName"], "type": raw["type"],
		"type_name": raw["typeName"], "status": raw["status"], "uri": raw["uri"],
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

func revisionStatus(value any) map[string]any {
	if status, ok := value.(map[string]any); ok {
		return map[string]any{"value": status["value"], "name": status["name"], "color": status["color"]}
	}
	return map[string]any{"value": value, "name": value, "color": nil}
}

func groupThreads(inline []map[string]any) map[string]any {
	byPHID := map[string]map[string]any{}
	roots := map[string]map[string]any{}
	var orderedRoots []map[string]any
	for _, item := range inline {
		phid := stringValue(item["phid"])
		if phid != "" {
			byPHID[phid] = item
			if item["reply_to_comment_phid"] == nil {
				roots[phid] = item
				orderedRoots = append(orderedRoots, item)
			}
		}
	}
	repliesByRoot := map[string][]map[string]any{}
	for phid := range roots {
		repliesByRoot[phid] = nil
	}
	var orphanReplies []map[string]any
	for _, item := range inline {
		parentPHID := stringValue(item["reply_to_comment_phid"])
		if parentPHID == "" {
			continue
		}
		seen := map[string]bool{stringValue(item["phid"]): true}
		ancestor := byPHID[parentPHID]
		for ancestor != nil && stringValue(ancestor["reply_to_comment_phid"]) != "" {
			ancestorPHID := stringValue(ancestor["phid"])
			if seen[ancestorPHID] {
				ancestor = nil
				break
			}
			seen[ancestorPHID] = true
			ancestor = byPHID[stringValue(ancestor["reply_to_comment_phid"])]
		}
		rootPHID := ""
		if ancestor != nil {
			rootPHID = stringValue(ancestor["phid"])
		}
		if roots[rootPHID] == nil {
			orphan := cloneMap(item)
			orphan["orphan_reason"] = "missing-or-cyclic-parent"
			orphanReplies = append(orphanReplies, orphan)
			continue
		}
		repliesByRoot[rootPHID] = append(repliesByRoot[rootPHID], item)
	}
	var threads []map[string]any
	for _, root := range orderedRoots {
		phid := stringValue(root["phid"])
		replies := repliesByRoot[phid]
		sortItems(replies)
		threads = append(threads, map[string]any{
			"root": root, "replies": mapsToAny(replies), "resolved": boolValue(root["is_done"]), "on_current_diff": boolValue(root["on_current_diff"]),
		})
	}
	sort.SliceStable(threads, func(i, j int) bool {
		a, _ := mapValue(threads[i]["root"])
		b, _ := mapValue(threads[j]["root"])
		return stringValue(a["created"]) < stringValue(b["created"])
	})
	sortItems(orphanReplies)
	return map[string]any{"threads": mapsToAny(threads), "orphan_replies": mapsToAny(orphanReplies)}
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

func sortItems(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		return stringValue(items[i]["created"]) < stringValue(items[j]["created"])
	})
}

func mapsToAny(items []map[string]any) []any {
	result := make([]any, len(items))
	for index := range items {
		result[index] = items[index]
	}
	return result
}

func cloneStrings(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}
