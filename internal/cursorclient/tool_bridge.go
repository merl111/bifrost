package cursorclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Tool exposes an already-authorized Bifrost tool through Cursor's MCP protocol.
// The callback retains its own rooted paths, validation and permission checks.
type Tool struct {
	Name, Description string
	Schema            json.RawMessage
	Call              func(context.Context, json.RawMessage) (string, error)
}

func bridgeDefinitions(tools []Tool) []*Value {
	var defs []*Value
	for _, tool := range tools {
		def, _ := New("McpToolDefinition")
		def.Set("name", "bifrost_"+tool.Name)
		def.Set("provider_identifier", "bifrost")
		def.Set("tool_name", tool.Name)
		def.Set("description", tool.Description)
		def.Set("input_schema_json", string(tool.Schema))
		defs = append(defs, def)
	}
	return defs
}

func (e *ToolEnv) callBridge(ctx context.Context, req, args *Value) *Value {
	server := args.String("server_identifier")
	if server == "" {
		server = args.String("provider_identifier")
	}
	if server != "" && server != "bifrost" {
		return nil
	}
	name := args.String("tool_name")
	if name == "" {
		name = strings.TrimPrefix(args.String("name"), "bifrost_")
	}
	for _, tool := range e.Tools {
		if tool.Name != name {
			continue
		}
		values, _ := args.Get("args").(map[string]any)
		parameters := make(map[string]any, len(values))
		var err error
		for key, value := range values {
			v, _ := value.(*Value)
			parameters[key], err = protoJSON(v, 0)
			if err != nil {
				break
			}
		}
		var raw []byte
		if err == nil {
			raw, err = json.Marshal(parameters)
		}
		if len(raw) > 1<<20 {
			err = errors.New("tool arguments exceed 1 MiB")
		}
		var output string
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			if tool.Call == nil {
				err = errors.New("tool is unavailable")
			} else {
				output, err = tool.Call(ctx, raw)
			}
		}
		if err != nil {
			output = err.Error()
		}
		if len(output) > 1<<20 {
			output = strings.ToValidUTF8(output[:1<<20], "") + "\n[tool output truncated]"
		}
		return oneofReply(req, "mcp_result", "McpResult", "success", "McpSuccess", func(v *Value) {
			text, _ := New("McpTextContent")
			text.Set("text", output)
			item, _ := New("McpToolResultContentItem")
			item.Set("text", text)
			v.Set("content", []*Value{item})
			v.Set("is_error", err != nil)
		})
	}
	return nil
}

func init() {
	// Standard google.protobuf JSON types are absent from the extracted vendor
	// descriptors. Decode them explicitly instead of treating MCP args as enums.
	schemaSpecs["Value"] = schemaSpec{desc: "Value|1 null_value 5 kind|2 number_value 1 kind|3 string_value 9 kind|4 bool_value 8 kind|5 struct_value #0 kind|6 list_value #1 kind", refs: []string{"Struct", "ListValue"}}
	schemaSpecs["Struct"] = schemaSpec{desc: "Struct|1 fields 9,#0", refs: []string{"Value"}}
	schemaSpecs["ListValue"] = schemaSpec{desc: "ListValue|1 values #0*", refs: []string{"Value"}}
}

func protoJSON(v *Value, depth int) (any, error) {
	if v == nil || depth > 64 {
		return nil, errors.New("invalid or excessively nested tool argument")
	}
	switch v.Oneof("kind") {
	case "null_value":
		return nil, nil
	case "string_value":
		return v.String("string_value"), nil
	case "number_value":
		return v.Get("number_value"), nil
	case "bool_value":
		return v.Bool("bool_value"), nil
	case "struct_value":
		fields, _ := v.Message("struct_value").Get("fields").(map[string]any)
		result := make(map[string]any, len(fields))
		for k, child := range fields {
			value, _ := child.(*Value)
			x, err := protoJSON(value, depth+1)
			if err != nil {
				return nil, err
			}
			result[k] = x
		}
		return result, nil
	case "list_value":
		result := make([]any, 0)
		for _, child := range v.Message("list_value").Messages("values") {
			x, err := protoJSON(child, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, x)
		}
		return result, nil
	}
	return nil, errors.New("missing tool argument value")
}
