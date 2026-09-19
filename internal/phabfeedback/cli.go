package phabfeedback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/relvacode/iso8601"
	"github.com/spf13/cobra"
)

type appOptions struct {
	host, config, firefoxProfile string
	format                       string
	firefoxCookies               bool
	stdin                        io.Reader
	stdout, stderr               io.Writer
}

type messageOptions struct {
	message, messageFile       string
	messageSet, messageFileSet bool
}

const (
	defaultListRole   = "responsible"
	defaultListStatus = "open"
	defaultListLimit  = 25
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCommand(args, stdin, stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	options := &appOptions{stdin: stdin, stdout: stdout, stderr: stderr}
	root := &cobra.Command{
		Use:   "phab-feedback",
		Short: "Manage Phabricator and Phorge review feedback",
		Long:  "List review work, or pass a revision first to inspect and respond to it.",
		Example: strings.Join([]string{
			"  phab-feedback",
			"  phab-feedback list --role reviewing",
			"  phab-feedback D123",
			"  phab-feedback D123 --threads=all",
			"  phab-feedback D123 reply 456 --message-file reply.txt",
		}, "\n"),
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return options.execute(command.Context(), "list", true, false, func(service *feedbackService) (map[string]any, error) {
				return service.listRevisions(defaultListRole, defaultListStatus, nil, defaultListLimit, "")
			})
		},
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&options.host, "host", "", "Phabricator/Phorge base URL")
	root.PersistentFlags().StringVar(&options.config, "config", "", "Path to config JSON (default: XDG config directory)")
	root.PersistentFlags().BoolVar(&options.firefoxCookies, "firefox-cookies", false, "Find a web session across local Firefox profiles")
	root.PersistentFlags().StringVar(&options.firefoxProfile, "firefox-profile", "", "Firefox profile directory (implies --firefox-cookies)")
	root.PersistentFlags().StringVar(&options.format, "format", "text", "Output format: text or json")
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		return validateFormat(options.format)
	}
	root.AddCommand(newListCommand(options))
	if revision := revisionArgument(root, args); revision != "" {
		root.AddCommand(newRevisionGroup(options, revision))
	}
	return root
}

func revisionArgument(root *cobra.Command, args []string) string {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "" {
			continue
		}
		if arg == "help" || arg == cobra.ShellCompRequestCmd || arg == cobra.ShellCompNoDescRequestCmd {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			flag := root.PersistentFlags().Lookup(name)
			if !hasValue && flag != nil && flag.NoOptDefVal == "" {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			name := strings.TrimPrefix(arg, "-")
			flag := root.PersistentFlags().ShorthandLookup(name)
			if flag != nil && flag.NoOptDefVal == "" {
				index++
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(arg), "d") || arg[0] >= '0' && arg[0] <= '9' {
			return arg
		}
		return ""
	}
	return ""
}

func newRevisionGroup(app *appOptions, revision string) *cobra.Command {
	var threadState string
	var timeline, currentDiffOnly bool
	_, revisionErr := revisionNumber(revision)
	command := &cobra.Command{
		Use:   revision,
		Short: "Inspect and respond to " + strings.ToUpper(revision),
		Args: func(command *cobra.Command, args []string) error {
			if len(args) == 1 && command.Flags().Changed("threads") {
				return fmt.Errorf("unexpected argument %q; use --threads=%s", args[0], args[0])
			}
			return cobra.NoArgs(command, args)
		},
		PreRunE: func(command *cobra.Command, _ []string) error {
			if revisionErr != nil {
				return revisionErr
			}
			hasThreads := command.Flags().Changed("threads")
			if timeline && hasThreads {
				return fmt.Errorf("--timeline and --threads are mutually exclusive")
			}
			if hasThreads && !slices.Contains([]string{"unresolved", "resolved", "all"}, threadState) {
				return fmt.Errorf("invalid --threads value %q", threadState)
			}
			if currentDiffOnly && !hasThreads {
				return fmt.Errorf("--current-diff-only requires --threads")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if timeline {
				return app.execute(command.Context(), "timeline", true, false, func(service *feedbackService) (map[string]any, error) {
					return service.timeline(revision)
				})
			}
			if command.Flags().Changed("threads") {
				return app.execute(command.Context(), "threads", true, false, func(service *feedbackService) (map[string]any, error) {
					return service.threads(revision, threadState, currentDiffOnly)
				})
			}
			return app.execute(command.Context(), "overview", true, false, func(service *feedbackService) (map[string]any, error) {
				summary, err := service.show(revision)
				if err != nil {
					return nil, err
				}
				threads, err := service.threads(revision, "unresolved", false)
				if err != nil {
					return nil, err
				}
				return map[string]any{"summary": summary, "threads": threads}, nil
			})
		},
	}
	command.Flags().BoolVar(&timeline, "timeline", false, "Show the complete chronological feedback timeline")
	command.Flags().StringVar(&threadState, "threads", "", "Show threads: unresolved, resolved, or all")
	command.Flags().Lookup("threads").NoOptDefVal = "unresolved"
	command.Flags().BoolVar(&currentDiffOnly, "current-diff-only", false, "Only include threads rooted on the current diff")
	command.AddGroup(
		&cobra.Group{ID: "respond", Title: "Respond:"},
		&cobra.Group{ID: "mozilla", Title: "Mozilla Review Helper:"},
	)
	command.AddCommand(
		newCommentCommand(app, revision),
		newReplyCommand(app, revision),
		newRemoveCommentCommand(app, revision),
		newDoneCommand(app, revision),
		newSubmitCommand(app, revision),
		newRateCommand(app, revision),
		newAIReviewCommand(app, revision),
	)
	return command
}

func newListCommand(app *appOptions) *cobra.Command {
	var role, status, modifiedAfter, after string
	var limit int
	command := &cobra.Command{
		Use:   "list",
		Short: "List revisions for the authenticated user",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
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
			return app.execute(command.Context(), "list", true, false, func(service *feedbackService) (map[string]any, error) {
				return service.listRevisions(role, status, modified, limit, after)
			})
		},
	}
	command.Flags().StringVar(&role, "role", defaultListRole, "Relationship to listed revisions: responsible, authored, or reviewing")
	command.Flags().StringVar(&status, "status", defaultListStatus, "Revision status filter: open, closed, or all")
	command.Flags().StringVar(&modifiedAfter, "modified-after", "", "Only revisions updated after an ISO 8601 time or Unix timestamp")
	command.Flags().IntVar(&limit, "limit", defaultListLimit, "Maximum revisions to return")
	command.Flags().StringVar(&after, "after", "", "Continue from a cursor returned by an earlier list command")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if !slices.Contains([]string{"responsible", "authored", "reviewing"}, role) {
			return fmt.Errorf("invalid --role value %q", role)
		}
		if !slices.Contains([]string{"open", "closed", "all"}, status) {
			return fmt.Errorf("invalid --status value %q", status)
		}
		return nil
	}
	return command
}

func newCommentCommand(app *appOptions, revision string) *cobra.Command {
	message := messageOptions{}
	command := &cobra.Command{
		Use:     "comment",
		Short:   "Post an immediate top-level revision comment",
		GroupID: "respond",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text, err := readMessage(message, app.stdin)
			if err != nil {
				return err
			}
			return app.execute(command.Context(), "comment", true, false, func(service *feedbackService) (map[string]any, error) {
				return service.postComment(revision, text)
			})
		},
	}
	addMessageFlags(command, &message)
	return command
}

func newReplyCommand(app *appOptions, revision string) *cobra.Command {
	message := messageOptions{}
	var submit bool
	command := &cobra.Command{
		Use:     "reply COMMENT_ID",
		Short:   "Draft a true reply to an inline comment",
		GroupID: "respond",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			text, err := readMessage(message, app.stdin)
			if err != nil {
				return err
			}
			return app.execute(command.Context(), "reply", true, true, func(service *feedbackService) (map[string]any, error) {
				result, err := service.draftInlineReply(revision, args[0], text)
				if err == nil && submit {
					result["submission"], err = service.submit(revision)
				}
				return result, err
			})
		},
	}
	addMessageFlags(command, &message)
	command.Flags().BoolVar(&submit, "submit", false, "Explicitly publish the new reply draft immediately")
	return command
}

func newRemoveCommentCommand(app *appOptions, revision string) *cobra.Command {
	return &cobra.Command{
		Use:     "remove-comment COMMENT_ID",
		Short:   "Remove an accidental top-level comment",
		GroupID: "respond",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return app.execute(command.Context(), "remove-comment", true, true, func(service *feedbackService) (map[string]any, error) {
				return service.removeComment(revision, args[0])
			})
		},
	}
}

func newDoneCommand(app *appOptions, revision string) *cobra.Command {
	return &cobra.Command{
		Use:     "done COMMENT_ID...",
		Short:   "Mark inline comments Done as drafts",
		GroupID: "respond",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return app.execute(command.Context(), "done", true, true, func(service *feedbackService) (map[string]any, error) {
				return service.markDone(revision, args)
			})
		},
	}
}

func newSubmitCommand(app *appOptions, revision string) *cobra.Command {
	command := newContextActionCommand(app, revision, "submit", "Submit pending draft actions and comments", false, true, (*feedbackService).submit)
	command.GroupID = "respond"
	return command
}

func newRateCommand(app *appOptions, revision string) *cobra.Command {
	var helpful, unhelpful bool
	command := &cobra.Command{
		Use:     "rate COMMENT_ID...",
		Short:   "Rate Review Helper feedback (Mozilla only)",
		GroupID: "mozilla",
		Args:    cobra.MinimumNArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if helpful == unhelpful {
				return fmt.Errorf("exactly one of --helpful or --unhelpful is required")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			name := "rate-unhelpful"
			if helpful {
				name = "rate-helpful"
			}
			return app.execute(command.Context(), name, true, true, func(service *feedbackService) (map[string]any, error) {
				return service.rate(revision, args, helpful)
			})
		},
	}
	command.Flags().BoolVar(&helpful, "helpful", false, "Rate the comments helpful")
	command.Flags().BoolVar(&unhelpful, "unhelpful", false, "Rate the comments unhelpful")
	return command
}

func newAIReviewCommand(app *appOptions, revision string) *cobra.Command {
	command := newContextActionCommand(app, revision, "ai-review", "Request a Review Helper AI review (Mozilla only)", false, true, (*feedbackService).requestAIReview)
	command.GroupID = "mozilla"
	return command
}

type revisionAction func(*feedbackService, string) (map[string]any, error)

func newContextActionCommand(app *appOptions, revision, name, short string, requireToken, requireCookie bool, action revisionAction) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return app.execute(command.Context(), name, requireToken, requireCookie, func(service *feedbackService) (map[string]any, error) {
				return action(service, revision)
			})
		},
	}
}

func (app *appOptions) execute(
	ctx context.Context,
	command string,
	requireToken, requireCookie bool,
	action func(*feedbackService) (map[string]any, error),
) error {
	credentials, err := resolveCredentials(ctx, credentialOptions{
		host: app.host, configPath: app.config, firefoxProfile: app.firefoxProfile,
		firefoxCookies: app.firefoxCookies || app.firefoxProfile != "",
		requireToken:   requireToken, requireCookie: requireCookie,
	})
	if err != nil {
		return err
	}
	client := httpTransport{client: defaultHTTPClient()}
	requests := transportFunc(func(method, target string, headers http.Header, data io.Reader) ([]byte, error) {
		return client.Request(ctx, method, target, headers, data)
	})
	service := &feedbackService{}
	if requireToken {
		service.conduit = &conduitClient{host: credentials.host, token: credentials.token, transport: requests}
	}
	if requireCookie {
		service.web = &webClient{host: credentials.host, cookie: credentials.cookie, transport: requests}
	}
	result, err := action(service)
	if err != nil {
		return err
	}
	if app.format == "text" {
		text, err := renderText(command, result)
		if err != nil {
			return err
		}
		_, err = lipgloss.Fprintln(app.stdout, text)
		return err
	}
	encoder := json.NewEncoder(app.stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func validateFormat(format string) error {
	if !slices.Contains([]string{"json", "text"}, format) {
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
			return "", fmt.Errorf("could not read message file: %s", options.messageFile)
		}
	case stdinAvailable(stdin):
		data, err = io.ReadAll(stdin)
	default:
		return "", fmt.Errorf("provide --message, --message-file, or redirected stdin")
	}
	if err != nil {
		return "", fmt.Errorf("could not read message: %w", err)
	}
	message := string(data)
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("message must not be empty")
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

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}
