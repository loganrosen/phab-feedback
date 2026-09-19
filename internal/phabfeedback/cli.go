package phabfeedback

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/relvacode/iso8601"
	"github.com/spf13/cobra"
)

type appOptions struct {
	host, config, firefoxProfile string
	firefoxCookies               bool
	stdin                        io.Reader
	stdout, stderr               io.Writer
}

type messageOptions struct {
	message, messageFile       string
	messageSet, messageFileSet bool
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCommand(stdin, stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	options := &appOptions{stdin: stdin, stdout: stdout, stderr: stderr}
	root := &cobra.Command{
		Use:           "phab-feedback",
		Short:         "Manage Phabricator and Phorge review feedback",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&options.host, "host", "", "Phabricator/Phorge base URL")
	root.PersistentFlags().StringVar(&options.config, "config", "", "Path to config JSON (default: XDG config directory)")
	root.PersistentFlags().BoolVar(&options.firefoxCookies, "firefox-cookies", false, "Find a web session across local Firefox profiles")
	root.PersistentFlags().StringVar(&options.firefoxProfile, "firefox-profile", "", "Firefox profile directory (implies --firefox-cookies)")

	root.AddCommand(
		newListCommand(options),
		newShowCommand(options),
		newThreadsCommand(options),
		newTimelineCommand(options),
		newCommentCommand(options),
		newReplyInlineCommand(options),
		newRemoveCommentCommand(options),
		newMarkDoneCommand(options),
		newSubmitCommand(options),
		newRateCommand(options, true),
		newRateCommand(options, false),
		newRequestAIReviewCommand(options),
	)
	return root
}

func newListCommand(app *appOptions) *cobra.Command {
	var role, status, modifiedAfter, after, format string
	var limit int
	command := &cobra.Command{
		Use:   "list",
		Short: "List revisions for the authenticated user",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if limit < 1 {
				return fmt.Errorf("revision limit must be positive")
			}
			var modified *int64
			if modifiedAfter != "" {
				value, err := parseTime(modifiedAfter)
				if err != nil {
					return err
				}
				modified = &value
			}
			return app.execute("list", true, false, format, func(service *feedbackService) (map[string]any, error) {
				return service.listRevisions(role, status, modified, limit, after)
			})
		},
	}
	command.Flags().StringVar(&role, "role", "responsible", "Relationship to listed revisions: responsible, authored, or reviewing")
	command.Flags().StringVar(&status, "status", "open", "Revision status filter: open, closed, or all")
	command.Flags().StringVar(&modifiedAfter, "modified-after", "", "Only revisions updated after an ISO 8601 time or Unix timestamp")
	command.Flags().IntVar(&limit, "limit", 25, "Maximum revisions to return")
	command.Flags().StringVar(&after, "after", "", "Continue from a cursor returned by an earlier list command")
	addFormatFlag(command, &format)
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if !oneOf(role, "responsible", "authored", "reviewing") {
			return fmt.Errorf("invalid --role value %q", role)
		}
		if !oneOf(status, "open", "closed", "all") {
			return fmt.Errorf("invalid --status value %q", status)
		}
		return validateFormat(format)
	}
	return command
}

func newShowCommand(app *appOptions) *cobra.Command {
	return newFormattedRevisionCommand(app, "show", "Show revision metadata and feedback counts", (*feedbackService).show)
}

func newThreadsCommand(app *appOptions) *cobra.Command {
	var state, format string
	var currentDiffOnly bool
	command := &cobra.Command{
		Use:   "threads REVISION",
		Short: "Show inline feedback grouped into threads",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.execute("threads", true, false, format, func(service *feedbackService) (map[string]any, error) {
				return service.threads(args[0], state, currentDiffOnly)
			})
		},
	}
	command.Flags().StringVar(&state, "state", "unresolved", "Thread state filter: unresolved, resolved, or all")
	command.Flags().BoolVar(&currentDiffOnly, "current-diff-only", false, "Only include threads rooted on the current diff")
	addFormatFlag(command, &format)
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if !oneOf(state, "unresolved", "resolved", "all") {
			return fmt.Errorf("invalid --state value %q", state)
		}
		return validateFormat(format)
	}
	return command
}

func newTimelineCommand(app *appOptions) *cobra.Command {
	return newFormattedRevisionCommand(app, "timeline", "Show structured general and inline feedback", (*feedbackService).timeline)
}

func newCommentCommand(app *appOptions) *cobra.Command {
	message := messageOptions{}
	command := &cobra.Command{
		Use:   "comment REVISION",
		Short: "Post an immediate top-level revision comment",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			text, err := readMessage(message, app.stdin)
			if err != nil {
				return err
			}
			return app.execute("comment", true, false, "json", func(service *feedbackService) (map[string]any, error) {
				return service.postComment(args[0], text)
			})
		},
	}
	addMessageFlags(command, &message)
	return command
}

func newReplyInlineCommand(app *appOptions) *cobra.Command {
	message := messageOptions{}
	var submit bool
	command := &cobra.Command{
		Use:   "reply-inline REVISION COMMENT_ID",
		Short: "Draft a true reply to an inline comment",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			text, err := readMessage(message, app.stdin)
			if err != nil {
				return err
			}
			return app.execute("reply-inline", true, true, "json", func(service *feedbackService) (map[string]any, error) {
				result, err := service.draftInlineReply(args[0], args[1], text)
				if err == nil && submit {
					result["submission"], err = service.submit(args[0])
				}
				return result, err
			})
		},
	}
	addMessageFlags(command, &message)
	command.Flags().BoolVar(&submit, "submit", false, "Explicitly publish the new reply draft immediately")
	return command
}

func newRemoveCommentCommand(app *appOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "remove-comment REVISION COMMENT_ID",
		Short: "Remove an accidental top-level comment",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.execute("remove-comment", true, true, "json", func(service *feedbackService) (map[string]any, error) {
				return service.removeComment(args[0], args[1])
			})
		},
	}
}

func newMarkDoneCommand(app *appOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "mark-done REVISION COMMENT_ID...",
		Short: "Mark inline comments Done as drafts",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.execute("mark-done", true, true, "json", func(service *feedbackService) (map[string]any, error) {
				return service.markDone(args[0], args[1:])
			})
		},
	}
}

func newSubmitCommand(app *appOptions) *cobra.Command {
	return newRevisionCommand(app, "submit", "Submit pending draft actions and comments", false, true, (*feedbackService).submit)
}

func newRateCommand(app *appOptions, helpful bool) *cobra.Command {
	name, short := "mark-unhelpful", "Rate Review Helper feedback unhelpful (Mozilla only)"
	if helpful {
		name, short = "mark-helpful", "Rate Review Helper feedback helpful (Mozilla only)"
	}
	return &cobra.Command{
		Use:   name + " REVISION COMMENT_ID...",
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.execute(name, true, true, "json", func(service *feedbackService) (map[string]any, error) {
				return service.rate(args[0], args[1:], helpful)
			})
		},
	}
}

func newRequestAIReviewCommand(app *appOptions) *cobra.Command {
	return newRevisionCommand(app, "request-ai-review", "Request a Review Helper AI review (Mozilla only)", false, true, (*feedbackService).requestAIReview)
}

type revisionAction func(*feedbackService, string) (map[string]any, error)

func newFormattedRevisionCommand(app *appOptions, name, short string, action revisionAction) *cobra.Command {
	var format string
	command := newRevisionCommand(app, name, short, true, false, func(service *feedbackService, revision string) (map[string]any, error) {
		return action(service, revision)
	})
	addFormatFlag(command, &format)
	command.PreRunE = func(_ *cobra.Command, _ []string) error { return validateFormat(format) }
	command.RunE = func(command *cobra.Command, args []string) error {
		return app.execute(name, true, false, format, func(service *feedbackService) (map[string]any, error) {
			return action(service, args[0])
		})
	}
	return command
}

func newRevisionCommand(app *appOptions, name, short string, requireToken, requireCookie bool, action revisionAction) *cobra.Command {
	return &cobra.Command{
		Use:   name + " REVISION",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.execute(name, requireToken, requireCookie, "json", func(service *feedbackService) (map[string]any, error) {
				return action(service, args[0])
			})
		},
	}
}

func (app *appOptions) execute(
	command string,
	requireToken, requireCookie bool,
	format string,
	action func(*feedbackService) (map[string]any, error),
) error {
	credentials, err := resolveCredentials(credentialOptions{
		host: app.host, configPath: app.config, firefoxProfile: app.firefoxProfile,
		firefoxCookies: app.firefoxCookies || app.firefoxProfile != "",
		requireToken:   requireToken, requireCookie: requireCookie,
	})
	if err != nil {
		return err
	}
	client := httpTransport{client: defaultHTTPClient()}
	service := &feedbackService{}
	if requireToken {
		service.conduit = &conduitClient{host: credentials.host, token: credentials.token, transport: client}
	}
	if requireCookie {
		service.web = &webClient{host: credentials.host, cookie: credentials.cookie, transport: client}
	}
	result, err := action(service)
	if err != nil {
		return err
	}
	if format == "text" {
		text, err := renderText(command, result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(app.stdout, text)
		return err
	}
	encoder := json.NewEncoder(app.stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func addFormatFlag(command *cobra.Command, format *string) {
	command.Flags().StringVar(format, "format", "json", "Output format: json or text")
}

func validateFormat(format string) error {
	if !oneOf(format, "json", "text") {
		return fmt.Errorf("invalid --format value %q", format)
	}
	return nil
}

func addMessageFlags(command *cobra.Command, options *messageOptions) {
	command.Flags().StringVar(&options.message, "message", "", "Message text")
	command.Flags().StringVar(&options.messageFile, "message-file", "", "Read message from a file, or use - for stdin")
	command.PreRunE = func(command *cobra.Command, _ []string) error {
		options.messageSet = command.Flags().Changed("message")
		options.messageFileSet = command.Flags().Changed("message-file")
		if options.messageSet && options.messageFileSet {
			return fmt.Errorf("--message and --message-file are mutually exclusive")
		}
		return nil
	}
}

func readMessage(options messageOptions, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	switch {
	case options.messageSet:
		data = []byte(options.message)
	case options.messageFile == "-":
		data, err = io.ReadAll(stdin)
	case options.messageFileSet:
		data, err = os.ReadFile(options.messageFile)
		if err != nil {
			return "", fmt.Errorf("Could not read message file: %s", options.messageFile)
		}
	case stdinAvailable(stdin):
		data, err = io.ReadAll(stdin)
	default:
		return "", fmt.Errorf("Provide --message, --message-file, or redirected stdin")
	}
	if err != nil {
		return "", fmt.Errorf("Could not read message: %v", err)
	}
	message := string(data)
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("Message must not be empty")
	}
	return message, nil
}

func stdinAvailable(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice == 0
}

func parseTime(value string) (int64, error) {
	if number, err := strconv.ParseInt(value, 10, 64); err == nil {
		return number, nil
	}
	if parsed, err := iso8601.ParseString(value); err == nil {
		return parsed.Unix(), nil
	}
	return 0, fmt.Errorf("--modified-after must be an ISO 8601 time or Unix timestamp")
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}
