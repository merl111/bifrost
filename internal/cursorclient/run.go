package cursorclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// Event is one normalized update from a Cursor agent turn.
type Event struct {
	Kind         string
	Text         string
	ID           string
	Detail       string
	InputTokens  int
	OutputTokens int
}

// RunInput is one native Cursor turn. Checkpoint bytes, when set, are resent
// without being rewritten.
type RunInput struct {
	Model          string
	Prompt         string
	Instructions   string
	ConversationID string
	Checkpoint     []byte
	Blobs          map[string][]byte
	Root           string
	Files          Files
	AllowWrite     bool
	AllowExec      bool
	MaxOutput      int
	MaxTools       int
	MaxSteps       int
	Tools          []Tool
	OnEvent        func(Event)
}

// RunOutput is the text and continuation state of a completed turn.
type RunOutput struct {
	Text           string
	ConversationID string
	Checkpoint     []byte
	Blobs          map[string][]byte
	InputTokens    int
	OutputTokens   int
	ToolCalls      int
	Steps          int
}

// Run sends one user message and serves local tool calls until the turn ends.
func (c Client) Run(ctx context.Context, in RunInput) (RunOutput, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if strings.TrimSpace(c.AccessToken) == "" {
		return RunOutput{}, errors.New("cursor access token is required")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return RunOutput{}, errors.New("cursor prompt is empty")
	}
	if in.MaxTools <= 0 || in.MaxTools > 200 {
		in.MaxTools = 64
	}
	if in.MaxSteps <= 0 || in.MaxSteps > 100 {
		in.MaxSteps = 64
	}
	conv := in.ConversationID
	if conv == "" {
		id, err := newUUID()
		if err != nil {
			return RunOutput{}, err
		}
		conv = id
	}
	requestID, err := newUUID()
	if err != nil {
		return RunOutput{}, err
	}
	runMsg, err := buildRun(in, conv, requestID)
	if err != nil {
		return RunOutput{}, err
	}
	stream, err := c.openRun(ctx, requestID)
	if err != nil {
		return RunOutput{}, err
	}
	defer stream.Close()
	sendMessage := c.runSender(ctx, requestID)
	if err := sendMessage(runMsg); err != nil {
		return RunOutput{}, err
	}
	env := &ToolEnv{Root: in.Root, Files: in.Files, AllowWrite: in.AllowWrite, AllowExec: in.AllowExec, MaxOutput: in.MaxOutput, Tools: in.Tools}
	env.seedBlobs(in.Blobs)
	var (
		mu         sync.Mutex
		toolCancel = func() {}
		toolID     int64
		out        RunOutput
		text       strings.Builder
	)
	out.ConversationID = conv
	out.Checkpoint = append([]byte(nil), in.Checkpoint...)
	saveBlobs := func() {
		out.Blobs = persistedBlobs(env.blobs, out.Checkpoint)
	}
	type incoming struct {
		msg *Value
		err error
	}
	incomingC := make(chan incoming, 8)
	go func() {
		defer close(incomingC)
		for {
			msg, err := stream.Next()
			if msg != nil && msg.Oneof("message") == "exec_server_control_message" {
				mu.Lock()
				if abort := msg.Message("exec_server_control_message").Message("abort"); abort != nil && abort.Int("id") == toolID {
					toolCancel()
				}
				mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case incomingC <- incoming{msg: msg, err: err}:
			}
			if err != nil {
				return
			}
		}
	}()
	ended := false
	var events eventStream
	budget := runBudget{maxSteps: in.MaxSteps, maxTools: in.MaxTools, steps: make(map[int64]bool)}
	for item := range incomingC {
		if item.err != nil {
			if errors.Is(item.err, io.EOF) || errors.Is(item.err, context.Canceled) && ended {
				break
			}
			out.Text = text.String()
			saveBlobs()
			return out, item.err
		}
		req := item.msg.Message("exec_server_message")
		counted := executionCounts(req)
		if err := budget.check(item.msg, &out); err != nil {
			out.Text = text.String()
			saveBlobs()
			return out, err
		}
		if item.msg.Message("interaction_update").Oneof("message") == "thinking_completed" {
			events.inThinking = false
		}
		toolCtx, cancel := context.WithCancel(ctx)
		mu.Lock()
		toolCancel = cancel
		toolID = req.Int("id")
		mu.Unlock()
		finishHeartbeat := executionHeartbeat(toolCtx, req, 3*time.Second, sendMessage, stop)
		replies, checkpoint, turnEnded, ev, send := env.handle(toolCtx, item.msg)
		if err := finishHeartbeat(); err != nil {
			cancel()
			out.Text = text.String()
			saveBlobs()
			return out, err
		}
		cancel()
		if len(checkpoint) > 0 {
			out.Checkpoint = checkpoint
		}
		if err := events.appendText(&text, ev, counted); err != nil {
			out.Text = text.String()
			saveBlobs()
			return out, err
		}
		if ev.Kind == "usage" {
			out.InputTokens += ev.InputTokens
			out.OutputTokens += ev.OutputTokens
		}
		executed, err := sendExecutionReplies(req, replies, send, sendMessage)
		if err != nil {
			out.Text = text.String()
			saveBlobs()
			return out, err
		}
		if executed && counted {
			out.ToolCalls++
			if ev.Kind == "" {
				ev = executionActivity(req, replies)
			}
		}
		if in.OnEvent != nil && ev.Kind != "" && ev.Kind != "usage" {
			in.OnEvent(events.normalize(ev))
		}
		if turnEnded {
			ended = true
		}
	}
	out.Text = text.String()
	saveBlobs()
	if err := ctx.Err(); err != nil && !ended {
		return out, err
	}
	return out, nil
}

func buildRun(in RunInput, conversationID, runID string) (*Value, error) {
	user, err := New("UserMessage")
	if err != nil {
		return nil, err
	}
	messageID, err := newUUID()
	if err != nil {
		return nil, err
	}
	user.Set("text", promptWithInstructions(in.Prompt, in.Instructions))
	user.Set("message_id", messageID)
	actionMsg, err := New("UserMessageAction")
	if err != nil {
		return nil, err
	}
	actionMsg.Set("user_message", user)
	action, err := New("ConversationAction")
	if err != nil {
		return nil, err
	}
	action.Set("user_message_action", actionMsg)
	modelName := strings.TrimSpace(in.Model)
	if modelName == "" {
		modelName = "auto"
	}
	details, err := New("ModelDetails2")
	if err != nil {
		return nil, err
	}
	details.Set("model_id", modelName)
	requested, err := New("RequestedModel")
	if err != nil {
		return nil, err
	}
	requested.Set("model_id", modelName)
	requested.Set("built_in_model", true)
	run, err := New("AgentRunRequest")
	if err != nil {
		return nil, err
	}
	var state *Value
	if len(in.Checkpoint) > 0 {
		state, err = Decode("ConversationStateStructure", in.Checkpoint)
		if err != nil {
			return nil, err
		}
	} else {
		// Cursor rejects a run whose conversation_state field is absent. A new
		// conversation is an empty ConversationStateStructure, the same object
		// the agent host constructs before any turns exist.
		state, err = New("ConversationStateStructure")
		if err != nil {
			return nil, err
		}
	}
	run.Set("conversation_state", state)
	if prefetched := prefetchBlobs(state, in.Blobs); len(prefetched) > 0 {
		run.Set("pre_fetched_blobs", prefetched)
	}
	run.Set("action", action)
	run.Set("model_details", details)
	run.Set("requested_model", requested)
	toolDefs, _ := New("McpTools")
	toolDefs.Set("mcp_tools", bridgeDefinitions(in.Tools))
	run.Set("mcp_tools", toolDefs)
	run.Set("conversation_id", conversationID)
	run.Set("run_id", runID)
	return wrapClient("run_request", run), nil
}

// promptWithInstructions keeps project instructions in the user message.
// Cursor's custom_system_prompt field is the hidden team-only --system-prompt
// option, and the server rejects it with invalid_argument.
func promptWithInstructions(prompt, instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return instructions
	}
	return instructions + "\n\n" + prompt
}
