package main

import (
	"bifrost/internal/tasks"
	cui "cui.local/cui"
	"fmt"
	"strings"
	"time"
)

type taskPresentation struct {
	query, filter, count, title, status, meta, error, tabs, empty, detail cui.Widget
	visible                                                               []tasks.Summary
}

func (g *desktop) buildTaskPanel(parent cui.Widget) {
	w := g.workbench
	v := &w.taskUI
	bar := parent.Box(cui.Horizontal, 8)
	v.query = bar.Search("")
	v.query.Placeholder("Search tasks…")
	v.query.Expand(true)
	v.query.OnAction(func(cui.Widget) { g.filterTasks(); g.showTaskDetails() })
	v.filter = bar.Select([]string{"All tasks", "Running", "Completed", "Failed"})
	v.filter.SetSelected(0)
	v.filter.OnAction(func(cui.Widget) { g.filterTasks(); g.showTaskDetails() })
	iconButton(bar, cui.SymbolRepeat, "Refresh tasks", g.refreshTasks)
	v.count = label(parent, "Loading tasks…", 10, muted)
	split := parent.Split(cui.Horizontal, .30)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(4)
	right.Padding(20)
	style(right, surface, foreground, 10, false, 20)
	w.taskList = left.List(nil)
	w.taskList.Expand(true)
	style(w.taskList, background, foreground, 6, false)
	v.empty = label(right, "Select a task to inspect its outcome, activity and execution details.", 13, muted)
	v.detail = right.Box(cui.Vertical, 12)
	v.detail.Expand(true)
	head := v.detail.Box(cui.Horizontal, 8)
	v.status = label(head, "", 10, cyan)
	v.status.Expand(true)
	w.taskCancel = button(head, "Stop task", g.cancelSelectedTask)
	w.taskCancel.SetEnabled(false)
	v.title = label(v.detail, "", 18, foreground)
	v.title.Font("", 18, 600)
	v.meta = label(v.detail, "", 11, muted)
	v.error = v.detail.Feedback(cui.Banner)
	v.tabs = v.detail.Select([]string{"Result & activity", "Execution details"})
	v.tabs.SetSelected(0)
	v.tabs.OnAction(func(cui.Widget) { g.showTaskDetails() })
	w.taskDetail = v.detail.Code("")
	w.taskDetail.ReadOnly(true)
	w.taskDetail.Expand(true)
	style(w.taskDetail, surface, foreground, 0, false, 4)
	w.taskDetail.Font("monospace", 11, 400)
	w.taskList.OnAction(func(item cui.Widget) {
		i := item.Selected()
		if i >= 0 && i < len(v.visible) {
			w.selectedTask = v.visible[i].ID
			g.showTaskDetails()
		}
	})
	v.detail.SetVisible(false)
}

func taskMatches(t tasks.Summary, query string, filter int) bool {
	if query != "" && !strings.Contains(strings.ToLower(t.Prompt+" "+t.Model+" "+string(t.ID)), query) {
		return false
	}
	switch filter {
	case 1:
		return t.Status == tasks.StatusActive || t.Status == tasks.StatusQueued
	case 2:
		return string(t.Status) == "completed"
	case 3:
		return string(t.Status) == "failed"
	}
	return true
}
func (g *desktop) filterTasks() {
	w := g.workbench
	v := &w.taskUI
	query := strings.ToLower(strings.TrimSpace(v.query.Text()))
	v.visible = nil
	var labels []string
	selected := -1
	running := 0
	for _, t := range w.tasks {
		if t.Status == tasks.StatusActive || t.Status == tasks.StatusQueued {
			running++
		}
		if !taskMatches(t, query, v.filter.Selected()) {
			continue
		}
		title := compactText(t.Prompt, 46)
		if title == "" {
			title = "Untitled task"
		}
		if t.ParentID != "" {
			title = "↳ " + title
		}
		labels = append(labels, title+"\n"+string(t.Status)+" · "+t.Elapsed.Round(time.Second).String())
		if t.ID == w.selectedTask {
			selected = len(v.visible)
		}
		v.visible = append(v.visible, t)
	}
	if selected < 0 && len(v.visible) > 0 {
		selected = 0
		w.selectedTask = v.visible[0].ID
	}
	w.taskList.Items(labels)
	w.taskList.SetSelected(selected)
	v.count.SetText(fmt.Sprintf("%d running   ·   %d total   ·   %d shown", running, len(w.tasks), len(v.visible)))
	v.empty.SetText("No matching tasks. Change the filter or start a conversation to create work.")
}
func (g *desktop) showTaskDetails() {
	w := g.workbench
	v := &w.taskUI
	w.taskCancel.SetEnabled(false)
	v.detail.SetVisible(false)
	v.empty.SetVisible(true)
	for _, t := range v.visible {
		if t.ID != w.selectedTask {
			continue
		}
		v.detail.SetVisible(true)
		v.empty.SetVisible(false)
		v.title.SetText(compactText(t.Prompt, 120))
		v.status.SetText(strings.ToUpper(string(t.Status)) + "  ·  " + t.Elapsed.Round(time.Second).String())
		var metadata []string
		for _, value := range []string{t.Profile, t.Model} {
			if value != "" {
				metadata = append(metadata, value)
			}
		}
		metadata = append(metadata, fmt.Sprintf("%d steps", t.Result.Steps), fmt.Sprintf("%d tokens", t.Result.Tokens.TotalTokens))
		v.meta.SetText(strings.Join(metadata, "  ·  "))
		v.error.FeedbackDismiss()
		if t.Error != "" {
			v.error.FeedbackShow("Task failed", safeText(t.Error), cui.Danger, "", 0)
		}
		var out strings.Builder
		if v.tabs.Selected() == 1 {
			fmt.Fprintf(&out, "Task ID\n%s\n\nWorkspace\n%s\n\nWorktree\n%s\n\nParent\n%s\n\nMode: %s\nStep budget: %d\nTimeout: %s\nOutput limit: %d bytes\nChildren: %v\n\nCreated: %s\nUpdated: %s", t.ID, t.Root, t.WorktreeRoot, t.ParentID, t.Mode, t.StepBudget, t.Timeout, t.MaxOutputBytes, t.Children, t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339))
		} else {
			if t.Result.Output != "" {
				out.WriteString(t.Result.Output)
			} else {
				out.WriteString("No result yet.")
			}
			for _, e := range w.events {
				if e.TaskID == t.ID {
					fmt.Fprintf(&out, "\n\n%s  %s\n%s", e.Time.Format("15:04:05"), e.Type, e.Message)
				}
			}
		}
		if content := cleanText(out.String()); w.taskDetail.Text() != content {
			w.taskDetail.SetText(content)
		}
		w.taskCancel.SetEnabled(t.Status == tasks.StatusActive || t.Status == tasks.StatusQueued)
		return
	}
}
