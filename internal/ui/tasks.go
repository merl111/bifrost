package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bifrost/internal/domain"
	"bifrost/internal/tasks"
	tea "charm.land/bubbletea/v2"
)

// TaskMonitorCallbacks supplies a read-only live task view. Refresh may read
// an active scheduler or a tasks.Monitor; Cancel is optional and is only used
// by the explicit palette command `tasks cancel ID`.
type TaskMonitorCallbacks struct {
	Refresh func(context.Context) ([]tasks.Summary, error)
	Events  func(context.Context, uint64, int) ([]tasks.Event, uint64, error)
	Cancel  func(context.Context, domain.ID) error
}

// WithTaskMonitor adds a bounded task status/event view to the workspace.
func WithTaskMonitor(callbacks TaskMonitorCallbacks) Option {
	return func(m *Model) { m.taskMonitor = callbacks }
}

type taskMonitorMsg struct {
	id        uint64
	summaries []tasks.Summary
	events    []tasks.Event
	cursor    uint64
	err       error
}

type taskMonitorTickMsg struct{ id uint64 }

func (m *Model) startTaskMonitor() tea.Cmd {
	if m.taskMonitor.Refresh == nil {
		m.status = "task monitor unavailable"
		return nil
	}
	m.taskID++
	m.viewPath, m.viewerOffset, m.focus = "tasks", 0, focusViewer
	id := m.taskID
	if m.taskCancel != nil {
		m.taskCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.taskCancel = cancel
	refresh, events := m.taskMonitor.Refresh, m.taskMonitor.Events
	return func() tea.Msg {
		summaries, err := refresh(ctx)
		if err != nil {
			return taskMonitorMsg{id: id, err: err}
		}
		var eventItems []tasks.Event
		var cursor uint64
		if events != nil {
			eventItems, cursor, err = events(ctx, 0, 64)
		}
		return taskMonitorMsg{id: id, summaries: summaries, events: eventItems, cursor: cursor, err: err}
	}
}

func (m *Model) updateTaskMonitor(msg taskMonitorMsg) tea.Cmd {
	if msg.id != m.taskID {
		return nil
	}
	m.taskCancel = nil
	if msg.err != nil {
		m.taskErr = msg.err.Error()
		m.status = "task monitor failed"
		return m.scheduleTaskRefresh()
	}
	m.taskSummaries, m.taskEvents, m.taskCursor, m.taskErr = msg.summaries, msg.events, msg.cursor, ""
	m.viewPath, m.viewerOffset, m.focus = "tasks", 0, focusViewer
	m.status = fmt.Sprintf("%d tasks", len(m.taskSummaries))
	return m.scheduleTaskRefresh()
}

func (m *Model) scheduleTaskRefresh() tea.Cmd {
	if m.viewPath != "tasks" || m.taskMonitor.Refresh == nil {
		return nil
	}
	id := m.taskID
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return taskMonitorTickMsg{id: id} })
}

func (m *Model) updateTaskMonitorTick(msg taskMonitorTickMsg) tea.Cmd {
	if msg.id != m.taskID || m.viewPath != "tasks" {
		return nil
	}
	return m.startTaskMonitor()
}

func (m *Model) cancelTask(id domain.ID) error {
	if m.taskMonitor.Cancel == nil {
		return fmt.Errorf("task cancellation unavailable")
	}
	return m.taskMonitor.Cancel(context.Background(), id)
}

func (m *Model) renderTasks() string {
	if m.taskErr != "" {
		return "!  task monitor error: " + sanitizeDisplay(m.taskErr)
	}
	var b strings.Builder
	b.WriteString("TASK MONITOR\n")
	b.WriteString("live scheduler activity and recent events\n\n")
	liveTools := make(map[domain.ID]int)
	for _, event := range m.taskEvents {
		if strings.Contains(strings.ToLower(event.Type), "tool") || strings.Contains(strings.ToLower(event.Message), "tool") {
			liveTools[event.TaskID]++
		}
	}
	if len(m.taskSummaries) == 0 {
		b.WriteString("◇  No tasks running.\n")
		b.WriteString("   Use the palette to refresh when work starts.\n")
	} else {
		for _, item := range m.taskSummaries {
			tools := item.Result.ToolCalls
			if liveTools[item.ID] > tools {
				tools = liveTools[item.ID]
			}
			icon := "·"
			status := string(item.Status)
			if strings.EqualFold(status, "running") || strings.EqualFold(status, "active") {
				icon = "◉"
			} else if strings.Contains(strings.ToLower(status), "error") || strings.Contains(strings.ToLower(status), "fail") {
				icon = "!"
			} else if strings.Contains(strings.ToLower(status), "complete") || strings.Contains(strings.ToLower(status), "done") {
				icon = "✓"
			}
			fmt.Fprintf(&b, "%s  %s  %-11s  %s\n", icon, item.ID, item.Status, formatElapsed(item.Elapsed))
			fmt.Fprintf(&b, "   steps %d   tools %d   tokens %d\n", item.Result.Steps, tools, item.Result.Tokens.TotalTokens)
			if item.ParentID != "" {
				b.WriteString("  parent: " + sanitizeDisplay(string(item.ParentID)) + "\n")
			}
			if item.Model != "" || item.Profile != "" {
				b.WriteString("  provider: " + sanitizeDisplay(item.Profile) + "/" + sanitizeDisplay(item.Model) + "\n")
			}
			if item.WorktreeRoot != "" {
				b.WriteString("  worktree: " + sanitizeDisplay(item.WorktreeRoot) + "\n")
			}
			if item.Error != "" {
				b.WriteString("  error: " + sanitizeDisplay(item.Error) + "\n")
			}
		}
	}
	if len(m.taskEvents) > 0 {
		b.WriteString("\nEVENT STREAM\n")
		for _, event := range m.taskEvents {
			fmt.Fprintf(&b, "#%d  %-10s %s\n", event.Sequence, event.Type, sanitizeDisplay(event.Message))
		}
	}
	b.WriteString("\n:tasks refresh   :tasks cancel ID")
	return strings.TrimSuffix(b.String(), "\n")
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	return d.Round(time.Second).String()
}
