package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"bifrost/internal/agent"
	"bifrost/internal/gitview"
	"bifrost/internal/provider"
	"bifrost/internal/ui"
)

// attachWorkspaceViewerTools gives the chat agent direct control over the
// Bifrost VIEWER pane so it can open files, highlight requested functions or
// line ranges, show diffs, and walk the user through multi-step code flows.
func attachWorkspaceViewerTools(cfg *agent.Config, emit func(ui.ChatEvent)) {
	if cfg == nil || emit == nil || (cfg.RunExternal != nil && !cfg.ExternalToolBridge) {
		return
	}
	for _, t := range cfg.ExtraTools {
		if t.Definition.Name == "viewer_open" {
			return
		}
	}

	root := cfg.Root
	viewerGuidance := "Viewer tools: When the user asks you to show code, highlight a function/type/section, show a diff, or walk through a code flow in the viewer, call `viewer_open`, `viewer_diff`, or `viewer_flow` so it immediately opens and highlights in the Bifrost VIEWER pane. For step-by-step explanations, concepts and PR reviews, inspect the code/diff first and use viewer_flow with a title and explanation for each step. Mix code, concept and diff steps; diff content must be the actual reviewed diff, not invented changes."
	if !strings.Contains(cfg.Instructions, "Viewer tools: When the user asks you to show code") {
		cfg.Instructions = strings.TrimSpace(cfg.Instructions + "\n\n" + viewerGuidance)
	}

	cfg.ExtraTools = append(cfg.ExtraTools,
		agent.Tool{
			Definition: provider.ToolDefinition{
				Name:        "viewer_open",
				Description: "Open a workspace file (or code snippet) in the Bifrost VIEWER pane, optionally scrolling to and highlighting a function/symbol or line range with an annotation.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "Relative path to the file in the workspace"},
						"symbol": {"type": "string", "description": "Optional function, method, struct, or class name to locate and highlight"},
						"start_line": {"type": "integer", "description": "Optional 1-indexed start line to highlight and scroll to"},
						"end_line": {"type": "integer", "description": "Optional 1-indexed end line of the highlight"},
						"note": {"type": "string", "description": "Optional explanation or banner note shown at the top of the viewer and on the highlight"},
						"highlights": {
							"type": "array",
							"description": "Optional multiple highlight ranges in the file",
							"items": {
								"type": "object",
								"required": ["start_line", "end_line"],
								"properties": {
									"start_line": {"type": "integer"},
									"end_line": {"type": "integer"},
									"label": {"type": "string"}
								}
							}
						}
					},
					"required": ["path"]
				}`),
			},
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args struct {
					Path       string                    `json:"path"`
					Symbol     string                    `json:"symbol"`
					StartLine  int                       `json:"start_line"`
					EndLine    int                       `json:"end_line"`
					Note       string                    `json:"note"`
					Highlights []ui.ViewerHighlightRange `json:"highlights"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", err
				}
				cleanPath := strings.TrimPrefix(strings.TrimSpace(args.Path), "./")
				if cleanPath == "" {
					return "", errors.New("path is required")
				}
				payload := ui.ViewerCommandPayload{
					Action:     "open",
					Path:       cleanPath,
					Symbol:     args.Symbol,
					StartLine:  args.StartLine,
					EndLine:    args.EndLine,
					Note:       args.Note,
					Highlights: args.Highlights,
				}
				b, _ := json.Marshal(payload)
				emit(ui.ChatEvent{Kind: "viewer", Detail: string(b)})
				return fmt.Sprintf("Opened %s in the Bifrost viewer.", cleanPath), nil
			},
		},
		agent.Tool{
			Definition: provider.ToolDefinition{
				Name:        "viewer_diff",
				Description: "Display a Git diff or custom unified diff in the Bifrost VIEWER pane with syntax coloring and optional line highlights.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "Optional relative file or directory path to diff (empty for full workspace git review)"},
						"ref": {"type": "string", "description": "Optional git ref/commit/branch to diff against (e.g. HEAD~1 or main)"},
						"diff": {"type": "string", "description": "Optional custom unified diff text to display directly"},
						"note": {"type": "string", "description": "Optional summary banner shown above the diff"}
					}
				}`),
			},
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args struct {
					Path string `json:"path"`
					Ref  string `json:"ref"`
					Diff string `json:"diff"`
					Note string `json:"note"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", err
				}
				diffText := strings.TrimSpace(args.Diff)
				title := "diff"
				if args.Path != "" {
					title = "diff · " + args.Path
				}
				if diffText == "" && root != "" {
					gitArgs := []string{"-C", root, "diff"}
					if strings.TrimSpace(args.Ref) != "" && !strings.HasPrefix(args.Ref, "-") {
						gitArgs = append(gitArgs, strings.TrimSpace(args.Ref))
					}
					if strings.TrimSpace(args.Path) != "" && !strings.HasPrefix(args.Path, "-") {
						gitArgs = append(gitArgs, "--", filepath.Clean(args.Path))
					}
					if out, err := exec.CommandContext(ctx, "git", gitArgs...).CombinedOutput(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
						diffText = string(out)
					} else if rev, sErr := gitview.Load(ctx, root); sErr == nil {
						diffText = rev.Text()
					}
				}
				if diffText == "" {
					diffText = "Working tree is clean (no uncommitted diff lines found)."
				}
				payload := ui.ViewerCommandPayload{
					Action:  "diff",
					Path:    args.Path,
					Title:   title,
					Content: diffText,
					Note:    args.Note,
				}
				b, _ := json.Marshal(payload)
				emit(ui.ChatEvent{Kind: "viewer", Detail: string(b)})
				return fmt.Sprintf("Displayed %s in the Bifrost viewer.", title), nil
			},
		},
		agent.Tool{
			Definition: provider.ToolDefinition{
				Name:        "viewer_flow",
				Description: "Present an interactive step-by-step explanation, concept lesson, code flow or PR review in the Bifrost VIEWER pane. The first step opens immediately with highlights, and the user can press n/p in the viewer to step through the flow.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"required": ["steps"],
					"properties": {
						"title": {"type": "string", "description": "Title of the flow or architecture walkthrough"},
						"steps": {
							"type": "array",
							"description": "Ordered sequence of code locations in the flow",
							"items": {
								"type": "object",
								"properties": {
									"kind": {"type":"string", "enum":["code","concept","diff"]},
									"explanation": {"type":"string", "description":"Explain what happens, why it matters and how it connects to the next step"},
									"content": {"type":"string", "description":"Code snippet or actual unified diff; required for diff steps"},
									"title": {"type": "string"},
									"path": {"type": "string"},
									"start_line": {"type": "integer"},
									"end_line": {"type": "integer"},
									"note": {"type": "string"}
								}
							}
						}
					}
				}`),
			},
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args struct {
					Title string          `json:"title"`
					Steps []ui.ViewerStep `json:"steps"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", err
				}
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if err := ui.ValidateWalkthrough(args.Steps); err != nil {
					return "", err
				}
				payload := ui.ViewerCommandPayload{
					Action: "flow",
					Title:  args.Title,
					Steps:  args.Steps,
				}
				b, _ := json.Marshal(payload)
				emit(ui.ChatEvent{Kind: "viewer", Detail: string(b)})
				return fmt.Sprintf("Loaded %d-step code flow %q in the Bifrost viewer (user can press n/p in viewer to step through).", len(args.Steps), args.Title), nil
			},
		},
	)
}
