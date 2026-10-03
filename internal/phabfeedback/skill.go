package phabfeedback

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/loganrosen/phab-feedback/skills"
	"github.com/spf13/cobra"
)

//go:embed resources/workflow.md
var workflowGuide string

type skillInstallResult struct {
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
}

func newSkillCommand(app *appOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "skill",
		Short: "Read bundled agent instructions or explicitly install the discovery skill",
		Long:  "The workflow guide and discovery skill are embedded in this CLI. These commands require no credentials or network access.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	command.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the complete version-matched workflow guide as Markdown",
		Long:  "Print the embedded workflow guide as plain Markdown, or a JSON object with a markdown field when --format=json is selected.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if app.format == "json" {
				encoder := json.NewEncoder(app.stdout)
				encoder.SetIndent("", "  ")
				encoder.SetEscapeHTML(false)
				return encoder.Encode(struct {
					Markdown string `json:"markdown"`
				}{Markdown: workflowGuide})
			}
			_, err := fmt.Fprint(app.stdout, workflowGuide)
			return err
		},
	})
	var directory string
	var force bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Explicitly install the small discovery SKILL.md",
		Long: "Install only the discovery stub, not the full workflow guide. The default is\n" +
			"~/.agents/skills/phab-feedback. Skill discovery paths vary by agent; --dir\n" +
			"selects the skill directory containing SKILL.md for another location.\n" +
			"Matching content is left unchanged. Different content requires --force;\n" +
			"symlinks and non-regular SKILL.md files are never replaced.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if directory == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve skill destination: %w", err)
				}
				directory = filepath.Join(home, ".agents", "skills", "phab-feedback")
			}
			result, err := installSkill(directory, force)
			if err != nil {
				return err
			}
			if app.format == "json" {
				encoder := json.NewEncoder(app.stdout)
				encoder.SetIndent("", "  ")
				encoder.SetEscapeHTML(false)
				return encoder.Encode(result)
			}
			action := "Installed"
			if !result.Changed {
				action = "Already installed"
			}
			_, err = fmt.Fprintf(app.stdout, "%s discovery skill: %s\n", action, result.Path)
			return err
		},
	}
	install.Flags().StringVar(&directory, "dir", "", "Destination skill directory (default: ~/.agents/skills/phab-feedback)")
	install.Flags().BoolVar(&force, "force", false, "Explicitly replace differing regular SKILL.md content")
	command.AddCommand(install)
	return command
}

func installSkill(directory string, force bool) (result skillInstallResult, resultErr error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return skillInstallResult{}, fmt.Errorf("resolve skill directory: %w", err)
	}
	result = skillInstallResult{Path: filepath.Join(directory, "SKILL.md")}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return result, fmt.Errorf("create skill directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return result, fmt.Errorf("open skill directory: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close skill directory: %w", err))
		}
	}()
	info, err := root.Lstat("SKILL.md")
	if err == nil {
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("refusing to replace non-regular skill file %s (including symlinks)", result.Path)
		}
		content, err := root.ReadFile("SKILL.md")
		if err != nil {
			return result, fmt.Errorf("read installed skill: %w", err)
		}
		if string(content) == skills.Discovery {
			return result, nil
		}
		if !force {
			return result, fmt.Errorf("skill file %s differs; review local changes and use --force to replace it", result.Path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("inspect installed skill: %w", err)
	}

	temporary := ".SKILL.md-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, fmt.Errorf("create temporary skill file: %w", err)
	}
	defer func() {
		if err := root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary skill file: %w", err))
		}
	}()
	_, writeErr := file.WriteString(skills.Discovery)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return result, fmt.Errorf("write discovery skill: %w", err)
	}
	if force {
		err = root.Rename(temporary, "SKILL.md")
	} else {
		// Link atomically fails if another install creates SKILL.md after inspection.
		err = root.Link(temporary, "SKILL.md")
	}
	if err != nil {
		return result, fmt.Errorf("install discovery skill at %s: %w", result.Path, err)
	}
	result.Changed = true
	return result, nil
}
