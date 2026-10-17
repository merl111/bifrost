package agent

import (
	"encoding/json"

	"bifrost/internal/environment"
	"bifrost/internal/provider"
	"context"
)

type toolExecutor struct {
	cfg     Config
	backend environment.Backend
}

// executeBatch keeps every tool result ahead of visual observations. Providers
// require a complete response for the assistant's entire function-call batch.
func (e toolExecutor) executeBatch(ctx context.Context, calls []provider.ToolCall, result *Result) ([]provider.Message, error) {
	var messages []provider.Message
	var images []provider.Image
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.ToolCalls++
		argsDetail := toolArgsDetail(call.Arguments)
		emitTool(e.cfg, call, "started", argsDetail)
		if err := e.hook(ctx, "before-tool", call); err != nil {
			return nil, err
		}
		text := dispatchTool(ctx, e.backend, e.cfg, call, e.cfg.MaxOutputBytes)
		if err := e.hook(ctx, "after-tool", map[string]any{"name": call.Name, "id": call.ID, "result": text}); err != nil {
			return nil, err
		}
		observed, err := e.images(call.Name)
		if err != nil {
			return nil, err
		}
		if len(images)+len(observed) > 8 {
			return nil, ErrBudget
		}
		images = append(images, observed...)
		emitTool(e.cfg, call, "finished", toolFinishedDetail(call.Arguments, text))
		messages = append(messages, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: text})
	}
	if len(images) > 0 {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "Visual observations from the preceding tools.", Images: images})
	}
	return messages, nil
}

func emitTool(cfg Config, call provider.ToolCall, status, detail string) {
	if cfg.OnEvent == nil {
		return
	}
	cfg.OnEvent(Event{Kind: "tool", ID: call.ID, Text: call.Name + " " + status, Detail: BoundedEventDetail(detail)})
}

func toolArgsDetail(arguments json.RawMessage) string {
	return BoundedEventDetail("args: " + string(arguments))
}

func toolFinishedDetail(arguments json.RawMessage, result string) string {
	return BoundedEventDetail(toolArgsDetail(arguments) + "\n" + BoundedEventDetail("result: "+result))
}

func (e toolExecutor) hook(ctx context.Context, event string, value any) error {
	if e.cfg.RunHook == nil {
		return nil
	}
	return e.cfg.RunHook(ctx, event, value)
}
func (e toolExecutor) images(name string) ([]provider.Image, error) {
	for _, tool := range e.cfg.ExtraTools {
		if tool.Definition.Name != name || tool.Images == nil {
			continue
		}
		images := tool.Images()
		if len(images) > 8 {
			return nil, ErrBudget
		}
		bytes := 0
		for _, im := range images {
			bytes += len(im.Data)
			if bytes > 8<<20 || len(im.URL) > 8192 {
				return nil, ErrBudget
			}
		}
		return images, nil
	}
	return nil, nil
}
