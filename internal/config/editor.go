package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// EditField is one editable setting exposed to the configuration editor.
// Value is a plain scalar for string, bool, integer, and floating point
// fields. Structured values use compact JSON. Kind is one of string, bool,
// int, float, or json.
type EditField struct {
	Key         string
	Value       string
	Kind        string
	Description string
}

const (
	EditKindString = "string"
	EditKindBool   = "bool"
	EditKindInt    = "int"
	EditKindFloat  = "float"
	EditKindJSON   = "json"

	// MissingRevision is the stable revision returned for a config that does
	// not exist. It lets callers use the same compare-and-swap operation for
	// creation and replacement.
	MissingRevision = "<missing>"
)

// ErrRevisionConflict reports an optimistic-save race.
var ErrRevisionConflict = errors.New("config revision conflict")

// EditFields returns a deterministic schema/value snapshot for cfg. Structs
// expose both their complete JSON value and their scalar leaves. Maps and
// slices are editable as complete JSON values; provider, project, and SSH map
// entries additionally expose complete-entry and existing-entry leaf fields.
// schema_version is intentionally excluded because it is controlled by the
// settings schema rather than the user.
func EditFields(cfg Settings) []EditField {
	var out []EditField
	walkEditFields(reflect.ValueOf(cfg), "", &out, true)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func walkEditFields(v reflect.Value, prefix string, out *[]EditField, mapEntry bool) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || (prefix == "" && field.Name == "SchemaVersion") {
				continue
			}
			name := jsonFieldName(field)
			if name == "" || name == "-" {
				continue
			}
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.Struct {
				appendEditField(fv, key, out)
				walkEditFields(fv, key, out, false)
				continue
			}
			appendEditField(fv, key, out)
			if fv.Kind() != reflect.Map || !mapEntry || !mapEntriesAllowed(prefix, key) || fv.Type().Key().Kind() != reflect.String {
				continue
			}
			keys := fv.MapKeys()
			sort.Slice(keys, func(a, b int) bool { return keys[a].String() < keys[b].String() })
			for _, mapKey := range keys {
				entryKey := key + "." + mapKey.String()
				entry := fv.MapIndex(mapKey)
				appendEditField(entry, entryKey, out)
				if derefKind(entry) == reflect.Struct {
					walkEditFields(entry, entryKey, out, false)
				}
			}
		}
	case reflect.Map, reflect.Slice, reflect.Array:
		// These are represented by the field that owns them. There is no
		// index-based schema because the complete JSON value is the edit unit.
		_ = mapEntry
	}
}

func mapEntriesAllowed(prefix, key string) bool {
	return prefix == "" && (key == "providers" || key == "projects" || key == "ssh")
}

func appendEditField(v reflect.Value, key string, out *[]EditField) {
	isPointer := v.Kind() == reflect.Pointer
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			*out = append(*out, EditField{Key: key, Value: "null", Kind: EditKindJSON, Description: fieldDescription(key)})
			return
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		*out = append(*out, EditField{Key: key, Value: "null", Kind: EditKindJSON, Description: fieldDescription(key)})
		return
	}
	kind := editKind(v)
	if isPointer {
		kind = EditKindJSON
	}
	value := ""
	switch kind {
	case EditKindString:
		value = v.String()
	case EditKindBool:
		value = strconv.FormatBool(v.Bool())
	case EditKindInt:
		value = strconv.FormatInt(v.Int(), 10)
	case EditKindFloat:
		value = strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())
	default:
		b, err := json.Marshal(v.Interface())
		if err != nil {
			value = "null"
		} else {
			value = string(b)
		}
	}
	*out = append(*out, EditField{Key: key, Value: value, Kind: kind, Description: fieldDescription(key)})
}

func editKind(v reflect.Value) string {
	if !v.IsValid() {
		return EditKindJSON
	}
	switch v.Kind() {
	case reflect.String:
		return EditKindString
	case reflect.Bool:
		return EditKindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return EditKindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return EditKindInt
	case reflect.Float32, reflect.Float64:
		return EditKindFloat
	default:
		return EditKindJSON
	}
}

func derefKind(v reflect.Value) reflect.Kind {
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Invalid
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return reflect.Invalid
	}
	return v.Kind()
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}
	return strings.Split(tag, ",")[0]
}

func describeKey(key string) string {
	parts := strings.Split(key, ".")
	for i, part := range parts {
		part = strings.ReplaceAll(part, "_", " ")
		if len(part) > 0 {
			part = strings.ToUpper(part[:1]) + part[1:]
		}
		parts[i] = part
	}
	return strings.Join(parts, " / ")
}

func fieldDescription(key string) string {
	descriptions := map[string]string{
		"subagents":               "Default provider and model for spawned child tasks. Empty values inherit the parent or selected profile model.",
		"subagents.provider":      "Saved provider profile for spawned subagents; empty inherits the parent. Explicit spawn choices take precedence.",
		"subagents.model":         "Default subagent model ID; empty uses the selected provider model or parent model.",
		"execution":               "Command approval policy. Separate from the allow_exec capability.",
		"execution.mode":          "allow: allow all enabled commands; ask: approve each command; rules: exact argv rules, otherwise ask. Empty preserves allow behavior.",
		"execution.allow":         "JSON array of exact argv arrays automatically approved in rules mode. Shell scripts must match the entire argv.",
		"active_provider":         "Provider profile selected by default; empty uses the runtime/provider environment.",
		"providers":               "JSON object mapping profile names to provider objects. Each object requires kind and model.",
		"projects":                "JSON object mapping project names to provider, skills, MCP, and environment defaults.",
		"ssh":                     "JSON object mapping names to SSH worker targets; values require target, absolute root, and state_dir.",
		"hooks":                   "JSON array of trusted hook objects with name, event, argv command, timeout, and output limit.",
		"defaults":                "Complete defaults object as strict JSON; scalar leaves are also editable below.",
		"defaults.project":        "Default registered project name; empty leaves project selection to the command or workspace.",
		"defaults.provider":       "Default provider profile name; it must exist in providers.",
		"defaults.skills":         "JSON array of skill names; replaces the default skill list.",
		"defaults.mcp_config":     "MCP configuration reference or path; empty disables the default reference.",
		"defaults.allow_write":    "Allow file mutations by default; false is the default.",
		"defaults.allow_exec":     "Allow host command execution by default; false is the default.",
		"defaults.browser":        "Allow browser tools by default; false is the default.",
		"defaults.max_steps":      "Maximum model/tool steps per run; must be 1..100 (default 8).",
		"defaults.max_agents":     "Maximum delegated agents; must be 0..32 (default 3).",
		"ui":                      "Complete UI object as strict JSON; appearance changes apply on save and runtime changes need a restart.",
		"ui.theme":                "UI theme: midnight, light, or plain (default midnight).",
		"ui.icons":                "Provider icon mode: unicode, nerd, or ascii (default unicode).",
		"ui.editor":               "JSON argv array for the external editor; no shell parsing is performed.",
		"ui.layout":               "Layout preset: studio, conversation, review, or focus. Setting it clears pane overrides.",
		"ui.pane_proportions":     "JSON array of three pane percentages; layout presets clear these overrides.",
		"ui.terminal_visible":     "JSON boolean or null controlling terminal visibility; layout presets clear this override.",
		"ui.terminal_percent":     "Terminal pane percentage; layout presets clear this override.",
		"ui.composer_max_rows":    "Composer height limit: 0 uses the default 8 rows; otherwise 1..20.",
		"ui.newline_key":          "Composer newline binding: empty uses Shift+Enter; allowed values are shift+enter, alt+enter, ctrl+j.",
		"decision":                "Complete decision-routing object as strict JSON; disabled by default.",
		"decision.enabled":        "Enable decision routing; enabling requires a valid decision policy.",
		"decision.candidates":     "JSON object mapping provider profiles to candidate descriptions; enabled policies need 2..16 candidates.",
		"decision.api_key_env":    "Decision provider environment variable name; never paste a literal API key.",
		"decision.kind":           "Decision backend kind: typesafe or openrouter.",
		"decision.base_url":       "Decision provider HTTP(S) endpoint; HTTP is limited to loopback.",
		"decision.model":          "Decision model identifier; required when routing is enabled.",
		"decision.min_confidence": "Decision threshold between 0 and 1.",
		"marketplace":             "Complete marketplace object as strict JSON.",
		"marketplace.catalog_url": "HTTPS catalog URL for skills; empty disables marketplace discovery.",
	}
	if description, ok := descriptions[key]; ok {
		return description
	}
	if strings.HasPrefix(key, "providers.") {
		for _, suffix := range []string{".kind", ".base_url", ".model", ".api_key_env", ".account", ".executable"} {
			if strings.HasSuffix(key, suffix) {
				profile := strings.TrimSuffix(strings.TrimPrefix(key, "providers."), suffix)
				prefix := "Provider " + profile + " "
				switch suffix {
				case ".kind":
					return prefix + "backend kind: responses, anthropic, gemini, chat-completions, chatgpt, cursor, claude, or openrouter."
				case ".base_url":
					return prefix + "HTTP(S) endpoint; HTTP is limited to loopback and must not contain credentials."
				case ".model":
					return prefix + "model identifier; required and limited to 256 bytes."
				case ".api_key_env":
					return prefix + "environment variable name such as OPENAI_API_KEY; never paste a literal API key."
				case ".account":
					return prefix + "named subscription account; account login owns its credentials."
				case ".executable":
					return prefix + "argv executable path for cursor or claude providers."
				}
			}
		}
		return "Provider profile as strict JSON; requires kind and model and never stores literal credentials."
	}
	if strings.HasPrefix(key, "projects.") {
		for _, suffix := range []string{".provider", ".skills", ".mcp_config", ".environment"} {
			if strings.HasSuffix(key, suffix) {
				return "Project " + strings.TrimSuffix(strings.TrimPrefix(key, "projects."), suffix) + " " + strings.TrimPrefix(suffix, ".") + "; provider references must name an existing provider."
			}
		}
		return "Project defaults as strict JSON; provider references must name an existing provider."
	}
	if strings.HasPrefix(key, "ssh.") {
		for _, suffix := range []string{".target", ".port", ".identity_file", ".executable", ".root", ".state_dir"} {
			if strings.HasSuffix(key, suffix) {
				return "SSH " + strings.TrimSuffix(strings.TrimPrefix(key, "ssh."), suffix) + " " + strings.TrimPrefix(suffix, ".") + "; guest root and state_dir must be absolute paths."
			}
		}
		return "SSH worker setting as strict JSON; target and guest root/state_dir are validated before save."
	}
	return describeKey(key) + "."
}

// SetValue parses and validates one setting. It updates cfg only after the
// complete candidate passes strict decoding and Settings.Validate.
func SetValue(cfg *Settings, key, value string) error {
	if cfg == nil {
		return errors.New("settings must not be nil")
	}
	if key == "" || key == "schema_version" || strings.HasPrefix(key, "schema_version.") {
		return fmt.Errorf("unknown setting %q", key)
	}
	next, err := cloneSettings(*cfg)
	if err != nil {
		return err
	}
	if err := setReflect(reflect.ValueOf(&next).Elem(), strings.Split(key, "."), value, key); err != nil {
		return err
	}
	if key == "ui.layout" {
		next.UI.PaneProportions = [3]int{}
		next.UI.TerminalVisible = nil
		next.UI.TerminalPercent = 0
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*cfg = next
	return nil
}

func cloneSettings(cfg Settings) (Settings, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return Settings{}, fmt.Errorf("clone settings: %w", err)
	}
	var out Settings
	if err := decodeStrict(strings.NewReader(string(b)), &out); err != nil {
		return Settings{}, fmt.Errorf("clone settings: %w", err)
	}
	return out, nil
}

func setReflect(v reflect.Value, parts []string, raw, fullKey string) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	if len(parts) == 0 {
		return errors.New("empty setting key")
	}
	switch v.Kind() {
	case reflect.Struct:
		name := parts[0]
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" || (v.Type() == reflect.TypeOf(Settings{}) && field.Name == "SchemaVersion") || jsonFieldName(field) != name {
				continue
			}
			if len(parts) == 1 {
				return assignReflect(v.Field(i), raw, fullKey)
			}
			if v.Field(i).Kind() != reflect.Struct && v.Field(i).Kind() != reflect.Map {
				return fmt.Errorf("unknown setting %q", fullKey)
			}
			return setReflect(v.Field(i), parts[1:], raw, fullKey)
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("unknown setting %q", fullKey)
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		mapKey, suffix := resolveMapPath(v, parts)
		keyValue := reflect.ValueOf(mapKey).Convert(v.Type().Key())
		entryType := v.Type().Elem()
		entry := reflect.New(entryType).Elem()
		if current := v.MapIndex(keyValue); current.IsValid() {
			entry.Set(current)
		}
		if len(suffix) == 0 {
			if err := assignReflect(entry, raw, fullKey); err != nil {
				return err
			}
		} else {
			if entry.Kind() != reflect.Struct && entry.Kind() != reflect.Map {
				return fmt.Errorf("unknown setting %q", fullKey)
			}
			if err := setReflect(entry, suffix, raw, fullKey); err != nil {
				return err
			}
		}
		v.SetMapIndex(keyValue, entry)
		return nil
	}
	return fmt.Errorf("unknown setting %q", fullKey)
}

func resolveMapPath(v reflect.Value, parts []string) (string, []string) {
	// Prefer the longest existing key, so profiles such as "work.local" can
	// still address their nested model with providers.work.local.model.
	for n := len(parts); n >= 1; n-- {
		candidate := strings.Join(parts[:n], ".")
		if v.MapIndex(reflect.ValueOf(candidate).Convert(v.Type().Key())).IsValid() {
			return candidate, parts[n:]
		}
	}
	return strings.Join(parts, "."), nil
}

func assignReflect(dst reflect.Value, raw, key string) error {
	if !dst.CanSet() {
		return fmt.Errorf("setting %q is not writable", key)
	}
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(raw)
		return nil
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("setting %s must be true or false: %w", key, err)
		}
		dst.SetBool(v)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("setting %s must be an integer: %w", key, err)
		}
		dst.SetInt(v)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(raw, 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("setting %s must be an unsigned integer: %w", key, err)
		}
		dst.SetUint(v)
		return nil
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(raw, dst.Type().Bits())
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("setting %s must be a finite number", key)
		}
		dst.SetFloat(v)
		return nil
	default:
		return decodeStrict(strings.NewReader(raw), dst.Addr().Interface())
	}
}

// LoadSettingsRevision loads validated settings and the exact file revision.
// A missing file returns DefaultSettings and MissingRevision so callers can
// create it with SaveSettingsRevision.
func LoadSettingsRevision(path string) (Settings, string, error) {
	b, err := readSettingsFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSettings(), MissingRevision, nil
	}
	if err != nil {
		return Settings{}, "", err
	}
	var cfg Settings
	if err := decodeStrict(bytes.NewReader(b), &cfg); err != nil {
		return Settings{}, "", err
	}
	if err := cfg.Validate(); err != nil {
		return Settings{}, "", err
	}
	return cfg, settingsRevision(b), nil
}

// SaveSettingsRevision atomically saves cfg only if expected still describes
// the file. The file and lock are checked while holding the same lock.
func SaveSettingsRevision(path string, cfg Settings, expected string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Accept an empty expected revision as the conventional must-not-exist
	// precondition while exposing MissingRevision to callers that want an
	// explicit value.
	if expected == "" {
		expected = MissingRevision
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := acquireSettingsLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := settingsFileRevision(path)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("%w: expected %q, got %q", ErrRevisionConflict, expected, current)
	}
	return writeSettings(path, cfg)
}

func settingsRevision(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
