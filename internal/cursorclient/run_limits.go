package cursorclient

import (
	"errors"
	"strings"
)

type runBudget struct {
	maxTools, maxSteps int
	steps              map[int64]bool
}

// check runs before local execution; reaching a limit cannot perform one extra
// mutation. Context and permission negotiation are not model tool calls.
func (b *runBudget) check(msg *Value, out *RunOutput) error {
	if executionCounts(msg.Message("exec_server_message")) && out.ToolCalls >= b.maxTools {
		return errors.New("cursor tool-call budget exceeded before executing the next tool")
	}
	step := msg.Message("interaction_update").Message("step_started")
	if step == nil {
		return nil
	}
	id := step.Int("step_id")
	if b.steps[id] {
		return nil
	}
	if out.Steps >= b.maxSteps {
		return errors.New("cursor model-step budget exceeded")
	}
	b.steps[id] = true
	out.Steps++
	return nil
}

func (s *eventStream) appendText(text *strings.Builder, ev Event, counted bool) error {
	if ev.Kind != "delta" {
		if ev.Kind == "tool" || counted {
			s.textBoundary = true
		}
		return nil
	}
	separator := ""
	if s.textBoundary && text.Len() > 0 {
		separator = "\n\n"
	}
	if text.Len()+len(separator)+len(ev.Text) > 4<<20 {
		return errors.New("cursor response exceeds 4 MiB")
	}
	s.textBoundary = false
	text.WriteString(separator)
	text.WriteString(ev.Text)
	return nil
}
