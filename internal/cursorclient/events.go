package cursorclient

import (
	"encoding/json"
	"fmt"
	"strings"
)

// eventStream coalesces provider-supplied thinking fragments under a stable ID.
// It does not synthesize reasoning or mix separate blocks around tool activity.
type eventStream struct {
	thinking     string
	thinkingID   int
	inThinking   bool
	textBoundary bool
}

func (s *eventStream) normalize(ev Event) Event {
	if ev.Kind == "thinking" {
		if !s.inThinking {
			s.thinkingID++
			s.thinking = ""
			s.inThinking = true
		}
		if len(s.thinking) < 64<<10 {
			s.thinking += ev.Text
			s.thinking = clipEventText(s.thinking, 64<<10)
		}
		ev.ID = fmt.Sprintf("cursor-thinking-%d", s.thinkingID)
		ev.Text = s.thinking
	} else if ev.Kind != "" {
		s.inThinking = false
	}
	return ev
}

func toolInteraction(update *Value, completed bool) Event {
	call := update.Message("tool_call")
	if call == nil {
		return Event{}
	}
	kind := call.Oneof("tool")
	wrapper := call.Message(kind)
	if wrapper == nil {
		return Event{}
	}
	id := call.String("tool_call_id")
	if id == "" {
		id = wrapper.Message("args").String("tool_call_id")
	}
	if id == "" {
		id = update.String("call_id")
	}
	state := "Running"
	if completed {
		state = "Completed"
	}
	return toolActivity(kind, id, wrapper.Message("args"), wrapper.Message("result"), state)
}

func toolActivity(kind, id string, args, result *Value, state string) Event {
	name := strings.TrimSuffix(strings.TrimSuffix(kind, "_args"), "_tool_call")
	name = strings.TrimPrefix(name, "pi_")
	if name == "mcp" && args != nil {
		name = args.String("tool_name")
		if name == "" {
			name = args.String("name")
		}
	}
	name = strings.ReplaceAll(name, "_", " ")
	target := ""
	if args != nil {
		for _, key := range []string{"path", "file_path", "pattern", "command", "url"} {
			if value := args.String(key); value != "" {
				target = value
				break
			}
		}
		if pattern := args.String("pattern"); pattern != "" && pattern != target {
			target += " · " + pattern
		}
	}
	if result != nil {
		outcome := result.Oneof("result")
		if outcome != "" && outcome != "success" {
			state = "Failed"
		}
	}
	text := state + " · " + name
	if target != "" {
		text += " · " + target
	}
	detail := boundedValueDetail(args)
	if result != nil {
		detail += "\n\nResult\n" + boundedValueDetail(result)
	}
	return Event{Kind: "tool", ID: id, Text: clipEventText(text, 240), Detail: clipEventText(detail, 12<<10)}
}

func boundedValueDetail(v *Value) string {
	budget := 8 << 10
	raw, _ := json.MarshalIndent(eventValue(v, 0, &budget), "", "  ")
	return clipEventText(string(raw), 8<<10)
}

func eventValue(v any, depth int, budget *int) any {
	if depth > 12 || *budget <= 0 {
		return "[truncated]"
	}
	*budget -= 16
	switch x := v.(type) {
	case *Value:
		if x == nil {
			return nil
		}
		return eventValue(x.fields, depth+1, budget)
	case map[string]any:
		m := make(map[string]any)
		for k, child := range x {
			if *budget <= 0 {
				break
			}
			m[k] = eventValue(child, depth+1, budget)
		}
		return m
	case []*Value:
		a := make([]any, 0)
		for _, child := range x {
			if *budget <= 0 {
				break
			}
			a = append(a, eventValue(child, depth+1, budget))
		}
		return a
	case []byte:
		return fmt.Sprintf("[%d bytes]", len(x))
	case string:
		value := clipEventText(x, min(*budget, 4096))
		*budget -= len(value)
		return value
	default:
		return x
	}
}

func clipEventText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:max(0, limit)], "") + "…"
}

func executionCounts(req *Value) bool {
	if req == nil {
		return false
	}
	name := req.Oneof("message")
	switch name {
	case "", "request_context_args", "mcp_state_exec_args", "shell_allowlist_precheck_args", "mcp_allowlist_precheck_args", "web_fetch_allowlist_precheck_args", "smart_mode_classifier_args", "span_context", "accept_hook_additional_contexts":
		return false
	}
	return true
}

func executionActivity(req *Value, replies []*Value) Event {
	name := req.Oneof("message")
	args := req.Message(name)
	id := args.String("tool_call_id")
	if id == "" {
		id = "exec:" + execKey(req)
	}
	var result *Value
	for _, reply := range replies {
		msg := reply.Message("exec_client_message")
		if msg != nil {
			result = msg.Message(msg.Oneof("message"))
		}
	}
	return toolActivity(name, id, args, result, "Completed")
}
