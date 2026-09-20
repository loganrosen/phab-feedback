package phabfeedback

import "encoding/json"

type viewer struct {
	PHID     string `json:"phid"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type handleInfo struct {
	PHID     string `json:"phid"`
	Name     any    `json:"name"`
	FullName any    `json:"full_name"`
	Type     any    `json:"type"`
	TypeName any    `json:"type_name"`
	Status   any    `json:"status"`
	URI      any    `json:"uri"`
}

type revisionStatus struct {
	Value any `json:"value"`
	Name  any `json:"name"`
	Color any `json:"color"`
}

type reviewer struct {
	Handle       handleInfo  `json:"-"`
	ReviewerPHID any         `json:"reviewer_phid"`
	Decision     any         `json:"status"`
	IsBlocking   any         `json:"is_blocking"`
	Actor        *handleInfo `json:"actor"`
}

func (value reviewer) MarshalJSON() ([]byte, error) {
	result := map[string]any{
		"reviewer_phid": value.ReviewerPHID, "status": value.Decision,
		"is_blocking": value.IsBlocking, "actor": value.Actor,
	}
	if value.Handle.PHID != "" {
		result["phid"] = value.Handle.PHID
		result["name"] = value.Handle.Name
		result["full_name"] = value.Handle.FullName
		result["type"] = value.Handle.Type
		result["type_name"] = value.Handle.TypeName
		result["uri"] = value.Handle.URI
	}
	return json.Marshal(result)
}

type mergeConflictStatus struct {
	Status                         any `json:"status"`
	Reason                         any `json:"reason"`
	IsStale                        any `json:"is_stale"`
	CheckedAt                      any `json:"checked_at"`
	CheckedAgainstCommit           any `json:"checked_against_commit"`
	CheckedAgainstBaseCommit       any `json:"checked_against_base_commit"`
	CheckedAgainstBaseRevisionPHID any `json:"checked_against_base_revision_phid"`
	CheckedAgainstDiffID           any `json:"checked_against_diff_id"`
	CheckedAgainstDiffPHID         any `json:"checked_against_diff_phid"`
}

type revisionRecord struct {
	ID                  any                  `json:"id"`
	PHID                any                  `json:"phid"`
	Title               any                  `json:"title"`
	URI                 any                  `json:"uri"`
	Status              revisionStatus       `json:"status"`
	IsDraft             any                  `json:"is_draft"`
	Author              *handleInfo          `json:"author"`
	Repository          *handleInfo          `json:"repository"`
	Reviewers           []reviewer           `json:"reviewers"`
	Created             any                  `json:"created"`
	Modified            any                  `json:"modified"`
	CurrentDiffPHID     any                  `json:"current_diff_phid"`
	MergeConflictStatus *mergeConflictStatus `json:"merge_conflict_status"`
}

type revisionListResult struct {
	Viewer        viewer           `json:"viewer"`
	Role          string           `json:"role"`
	Status        string           `json:"status"`
	ModifiedAfter any              `json:"modified_after"`
	Count         int              `json:"count"`
	Cursor        map[string]any   `json:"cursor"`
	Revisions     []revisionRecord `json:"revisions"`
}

type diffInfo struct {
	ID      any `json:"id"`
	PHID    any `json:"phid"`
	Created any `json:"created"`
}

type feedbackEvent struct {
	Kind               string `json:"kind"`
	ID                 any    `json:"id"`
	PHID               any    `json:"phid"`
	TransactionID      any    `json:"transaction_id"`
	TransactionPHID    any    `json:"transaction_phid"`
	Created            any    `json:"created"`
	Content            any    `json:"content"`
	DiffID             any    `json:"diff_id,omitempty"`
	DiffPHID           any    `json:"diff_phid,omitempty"`
	OnCurrentDiff      bool   `json:"on_current_diff,omitempty"`
	Path               any    `json:"path,omitempty"`
	Line               any    `json:"line,omitempty"`
	IsDone             any    `json:"is_done,omitempty"`
	ReplyToCommentID   any    `json:"reply_to_comment_id,omitempty"`
	ReplyToCommentPHID any    `json:"reply_to_comment_phid,omitempty"`
	OrphanReason       string `json:"orphan_reason,omitempty"`
}

func (event feedbackEvent) MarshalJSON() ([]byte, error) {
	result := map[string]any{
		"kind": event.Kind, "id": event.ID, "phid": event.PHID,
		"transaction_id": event.TransactionID, "transaction_phid": event.TransactionPHID,
		"created": event.Created, "content": event.Content,
	}
	if event.Kind == "inline" {
		result["diff_id"] = event.DiffID
		result["diff_phid"] = event.DiffPHID
		result["on_current_diff"] = event.OnCurrentDiff
		result["path"] = event.Path
		result["line"] = event.Line
		result["is_done"] = event.IsDone
		result["reply_to_comment_id"] = event.ReplyToCommentID
		result["reply_to_comment_phid"] = event.ReplyToCommentPHID
	}
	if event.OrphanReason != "" {
		result["orphan_reason"] = event.OrphanReason
	}
	return json.Marshal(result)
}

type timelineResult struct {
	RevisionID      int             `json:"revision_id"`
	CurrentDiff     diffInfo        `json:"current_diff"`
	Events          []feedbackEvent `json:"events"`
	GeneralComments []feedbackEvent `json:"general_comments"`
	InlineComments  []feedbackEvent `json:"inline_comments"`
}

type thread struct {
	Root          feedbackEvent   `json:"root"`
	Replies       []feedbackEvent `json:"replies"`
	Resolved      bool            `json:"resolved"`
	OnCurrentDiff bool            `json:"on_current_diff"`
}

type threadsResult struct {
	RevisionID      int             `json:"revision_id"`
	CurrentDiff     diffInfo        `json:"current_diff"`
	State           string          `json:"state"`
	CurrentDiffOnly bool            `json:"current_diff_only"`
	Count           int             `json:"count"`
	Threads         []thread        `json:"threads"`
	OrphanReplies   []feedbackEvent `json:"orphan_replies"`
}

type feedbackCounts struct {
	GeneralComments   int `json:"general_comments"`
	InlineComments    int `json:"inline_comments"`
	RootThreads       int `json:"root_threads"`
	UnresolvedThreads int `json:"unresolved_threads"`
	ResolvedThreads   int `json:"resolved_threads"`
	Replies           int `json:"replies"`
	OlderDiffComments int `json:"older_diff_comments"`
	OrphanReplies     int `json:"orphan_replies"`
}

type revisionSummary struct {
	Revision    revisionRecord `json:"revision"`
	CurrentDiff diffInfo       `json:"current_diff"`
	Feedback    feedbackCounts `json:"feedback"`
}

type overviewResult struct {
	Summary revisionSummary `json:"summary"`
	Threads threadsResult   `json:"threads"`
}

type commentResult struct {
	RevisionID int    `json:"revision_id"`
	Action     string `json:"action"`
	Posted     bool   `json:"posted"`
	Published  bool   `json:"published"`
	Result     any    `json:"result"`
}

type submissionResult struct {
	RevisionID     int    `json:"revision_id"`
	Action         string `json:"action"`
	Outcome        string `json:"outcome"`
	Attempted      bool   `json:"attempted"`
	Submitted      bool   `json:"submitted"`
	OutcomeUnknown bool   `json:"outcome_unknown,omitempty"`
	Redirect       string `json:"redirect,omitempty"`
	Dialog         string `json:"dialog,omitempty"`
	Recovery       string `json:"recovery,omitempty"`
}

const (
	submissionOutcomeNotAttempted = "not-attempted"
	submissionOutcomeBlocked      = "blocked"
	submissionOutcomeUnknown      = "unknown"
	submissionOutcomeNoEffect     = "no-effect"
	submissionOutcomeRejected     = "rejected"
	submissionOutcomeSubmitted    = "submitted"
)

type inlineReplyResult struct {
	RevisionID        int               `json:"revision_id"`
	Action            string            `json:"action"`
	ParentCommentID   int               `json:"parent_comment_id"`
	ParentCommentPHID any               `json:"parent_comment_phid"`
	DraftCommentID    int               `json:"draft_comment_id"`
	CreatedReplyID    int               `json:"created_reply_id"`
	Saved             bool              `json:"saved"`
	Draft             bool              `json:"draft"`
	Published         bool              `json:"published"`
	FinalDone         *bool             `json:"final_done,omitempty"`
	Done              *commentAction    `json:"done,omitempty"`
	Submission        *submissionResult `json:"submission,omitempty"`
}

type removedCommentResult struct {
	RevisionID int    `json:"revision_id"`
	Action     string `json:"action"`
	CommentID  int    `json:"comment_id"`
	Removed    bool   `json:"removed"`
}

type commentAction struct {
	Action             string `json:"action,omitempty"`
	CommentID          int    `json:"comment_id"`
	IsDone             *bool  `json:"is_done,omitempty"`
	FinalDone          *bool  `json:"final_done,omitempty"`
	Draft              *bool  `json:"draft,omitempty"`
	Published          *bool  `json:"published,omitempty"`
	ObservedChecked    *bool  `json:"observed_checked,omitempty"`
	ObservedDraftState *bool  `json:"observed_draft_state,omitempty"`
	Recovery           string `json:"recovery,omitempty"`
	Helpful            *bool  `json:"helpful,omitempty"`
	Message            any    `json:"message,omitempty"`
}

type commentActionResult struct {
	RevisionID          int               `json:"revision_id"`
	Action              string            `json:"action"`
	MozillaReviewHelper bool              `json:"mozilla_review_helper,omitempty"`
	Comments            []commentAction   `json:"comments"`
	NotAttempted        []int             `json:"not_attempted,omitempty"`
	Submission          *submissionResult `json:"submission,omitempty"`
}

type aiReviewResult struct {
	RevisionID          int    `json:"revision_id"`
	Action              string `json:"action"`
	MozillaReviewHelper bool   `json:"mozilla_review_helper"`
	Status              string `json:"status"`
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorResult struct {
	Host   string        `json:"host"`
	Checks []doctorCheck `json:"checks"`
	OK     bool          `json:"ok"`
}

type batchManifest struct {
	Revision string                `json:"revision"`
	Actions  []batchManifestAction `json:"actions"`
}

type batchManifestAction struct {
	CommentID int     `json:"comment_id"`
	Reply     *string `json:"reply,omitempty"`
	Done      *bool   `json:"done,omitempty"`
}

type batchMutation struct {
	ActionIndex        int    `json:"action_index"`
	MutationIndex      int    `json:"mutation_index"`
	Action             string `json:"action"`
	CommentID          int    `json:"comment_id"`
	ParentCommentID    int    `json:"parent_comment_id,omitempty"`
	CreatedReplyID     int    `json:"created_reply_id,omitempty"`
	Planned            bool   `json:"planned,omitempty"`
	Saved              bool   `json:"saved,omitempty"`
	Draft              *bool  `json:"draft,omitempty"`
	Published          *bool  `json:"published,omitempty"`
	FinalDone          *bool  `json:"final_done,omitempty"`
	ObservedChecked    *bool  `json:"observed_checked,omitempty"`
	ObservedDraftState *bool  `json:"observed_draft_state,omitempty"`
	Recovery           string `json:"recovery,omitempty"`
}

type batchFailure struct {
	ActionIndex        int    `json:"action_index,omitempty"`
	MutationIndex      int    `json:"mutation_index,omitempty"`
	Action             string `json:"action"`
	CompletedMutations int    `json:"completed_mutations"`
	Error              string `json:"error"`
}

type batchResult struct {
	RevisionID int               `json:"revision_id"`
	Action     string            `json:"action"`
	DryRun     bool              `json:"dry_run"`
	Submit     bool              `json:"submit"`
	State      string            `json:"state"`
	Mutations  []batchMutation   `json:"mutations"`
	Submission *submissionResult `json:"submission,omitempty"`
	Failure    *batchFailure     `json:"failure,omitempty"`
}

type replyExpectation struct {
	ReplyID  int
	ParentID int
}

type replyVerification struct {
	ReplyID         int  `json:"reply_id"`
	ParentCommentID int  `json:"parent_comment_id"`
	Found           bool `json:"found"`
	Linked          bool `json:"linked"`
}

type doneVerification struct {
	CommentID     int    `json:"comment_id"`
	Found         bool   `json:"found"`
	ConduitIsDone bool   `json:"conduit_is_done"`
	State         string `json:"state"`
}

type verificationResult struct {
	RevisionID         int                 `json:"revision_id"`
	Action             string              `json:"action"`
	Status             string              `json:"status"`
	ChecksPassed       bool                `json:"checks_passed"`
	Replies            []replyVerification `json:"replies"`
	Done               []doneVerification  `json:"done"`
	DoneStateAmbiguous bool                `json:"done_state_ambiguous,omitempty"`
	Limitations        []string            `json:"limitations,omitempty"`
}
