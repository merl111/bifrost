package cursorclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
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
}

// Run sends one user message and serves local tool calls until the turn ends.
func (c Client) Run(ctx context.Context, in RunInput) (RunOutput, error) {
	if strings.TrimSpace(c.AccessToken) == "" {
		return RunOutput{}, errors.New("cursor access token is required")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return RunOutput{}, errors.New("cursor prompt is empty")
	}
	if in.MaxTools <= 0 || in.MaxTools > 200 {
		in.MaxTools = 64
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
	if err := c.append(ctx, requestID, 0, runMsg); err != nil {
		return RunOutput{}, err
	}
	env := &ToolEnv{Root: in.Root, Files: in.Files, AllowWrite: in.AllowWrite, AllowExec: in.AllowExec, MaxOutput: in.MaxOutput}
	env.seedBlobs(in.Blobs)
	var (
		mu         sync.Mutex
		toolCancel = func() {}
		out        RunOutput
		seq        int64 = 1
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
			select {
			case <-ctx.Done():
				return
			case incomingC <- incoming{msg: msg, err: err}:
			}
			if err != nil {
				return
			}
			if msg != nil && msg.Oneof("message") == "exec_server_control_message" {
				mu.Lock()
				cancel := toolCancel
				mu.Unlock()
				cancel()
			}
		}
	}()
	ended := false
	for item := range incomingC {
		if item.err != nil {
			if errors.Is(item.err, io.EOF) || errors.Is(item.err, context.Canceled) && ended {
				break
			}
			if errors.Is(item.err, io.EOF) {
				break
			}
			out.Text = text.String()
			saveBlobs()
			return out, item.err
		}
		toolCtx, cancel := context.WithCancel(ctx)
		mu.Lock()
		toolCancel = cancel
		mu.Unlock()
		replies, checkpoint, turnEnded, ev, send := env.handle(toolCtx, item.msg)
		cancel()
		if len(checkpoint) > 0 {
			out.Checkpoint = checkpoint
		}
		if ev.Kind == "delta" {
			text.WriteString(ev.Text)
		}
		if ev.Kind == "usage" {
			out.InputTokens += ev.InputTokens
			out.OutputTokens += ev.OutputTokens
		}
		executed := false
		for _, reply := range replies {
			if send && reply != nil && reply.Oneof("message") == "exec_client_message" {
				executed = true
			}
			if send && reply != nil {
				if err := c.append(ctx, requestID, seq, reply); err != nil {
					out.Text = text.String()
					saveBlobs()
					return out, err
				}
				seq++
			}
		}
		if executed {
			out.ToolCalls++
			if ev.Kind == "" {
				ev = Event{Kind: "tool", Text: item.msg.Message("exec_server_message").Oneof("message")}
			}
		}
		if in.OnEvent != nil && ev.Kind != "" && ev.Kind != "usage" {
			in.OnEvent(ev)
		}
		if turnEnded {
			ended = true
		}
		if out.ToolCalls > in.MaxTools {
			out.Text = text.String()
			saveBlobs()
			return out, errors.New("cursor tool-call budget exceeded")
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
