package ui

import "context"

// MentionContext adapts the same indexing, matching, and bounded context
// expansion used by the TUI. Methods may perform I/O; call them off the UI thread.
type MentionContext struct{ model *Model }
type MentionCandidate struct{ Token, Label, Detail string }

func NewMentionContext(root string, fs Filesystem, status ProjectStatus, remote bool, review func() (string, error), terminal *NativeTerminal) *MentionContext {
	m := Model{root: root, fs: fs, remoteFS: remote, projectStatus: status}
	if review != nil {
		m.reviewLoader = func(_ context.Context) (string, error) { return review() }
	}
	if terminal != nil {
		m.pty = terminal.session
	}
	return &MentionContext{&m}
}
func (c *MentionContext) Candidates(query string) []MentionCandidate {
	c.model.ensureChatMentionIndex()
	var result []MentionCandidate
	for _, v := range c.model.buildChatMentionCandidates(query) {
		result = append(result, MentionCandidate{v.Token, v.Label, v.Detail})
	}
	return result
}
func (c *MentionContext) Expand(prompt string) (string, []string) {
	return c.model.expandChatPromptMentions(prompt)
}
func ActiveMention(input string) (int, string, bool) { return activeChatMentionToken(input) }
