package cursorclient

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxMessageBytes = 8 << 20

// Value is one decoded protobuf message. Unmodified messages encode back to
// the exact bytes they were parsed from, including unknown fields.
type Value struct {
	Type    string
	fields  map[string]any
	unknown []byte
	raw     []byte
	dirty   bool
	opaque  bool
}

type fieldSpec struct {
	no       int
	name     string
	scalar   int
	ref      string
	mapKey   int
	mapVal   int
	mapRef   string
	optional bool
	repeated bool
	message  bool
	enum     bool
	mapField bool
	oneof    string
}

type parsedSchema struct {
	fields []fieldSpec
	byName map[string]fieldSpec
	byNo   map[int]fieldSpec
}

var (
	schemaOnce sync.Once
	schemaBy   map[string]parsedSchema
	schemaErr  error
)

func schemas() (map[string]parsedSchema, error) {
	schemaOnce.Do(func() {
		schemaBy = make(map[string]parsedSchema, len(schemaSpecs))
		for name, spec := range schemaSpecs {
			parsed, err := parseDescriptor(spec.desc, spec.refs)
			if err != nil {
				schemaErr = fmt.Errorf("cursor schema %s: %w", name, err)
				return
			}
			schemaBy[name] = parsed
		}
	})
	return schemaBy, schemaErr
}

func parseDescriptor(desc string, refs []string) (parsedSchema, error) {
	parts := strings.Split(desc, "|")
	out := parsedSchema{byName: map[string]fieldSpec{}, byNo: map[int]fieldSpec{}}
	for _, part := range parts[1:] {
		toks := strings.Fields(part)
		if len(toks) < 3 {
			return parsedSchema{}, fmt.Errorf("short field %q", part)
		}
		no, err := strconv.Atoi(toks[0])
		if err != nil || no <= 0 {
			return parsedSchema{}, fmt.Errorf("field number %q", toks[0])
		}
		f := fieldSpec{no: no, name: toks[1]}
		if len(toks) >= 4 {
			f.oneof = toks[3]
		}
		if err := fillType(&f, toks[2], refs); err != nil {
			return parsedSchema{}, err
		}
		if _, ok := out.byNo[f.no]; ok {
			return parsedSchema{}, fmt.Errorf("duplicate field %d", f.no)
		}
		out.fields = append(out.fields, f)
		out.byName[f.name] = f
		out.byNo[f.no] = f
	}
	return out, nil
}

func fillType(f *fieldSpec, tok string, refs []string) error {
	if strings.HasSuffix(tok, "?") {
		f.optional = true
		tok = strings.TrimSuffix(tok, "?")
	}
	if strings.HasSuffix(tok, "*") {
		f.repeated = true
		tok = strings.TrimSuffix(tok, "*")
	}
	if strings.Contains(tok, ",") {
		key, val, _ := strings.Cut(tok, ",")
		n, err := strconv.Atoi(key)
		if err != nil {
			return fmt.Errorf("map key %q", key)
		}
		f.mapField = true
		f.mapKey = n
		f.repeated = true
		if strings.HasPrefix(val, "#") {
			ref, err := refName(val, refs)
			if err != nil {
				return err
			}
			f.mapRef = canonicalRef(ref)
			f.message = isMessage(f.mapRef)
			if !f.message {
				f.enum = true
			}
			return nil
		}
		n, err = strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("map value %q", val)
		}
		f.mapVal = n
		return nil
	}
	if strings.HasPrefix(tok, "#") {
		ref, err := refName(tok, refs)
		if err != nil {
			return err
		}
		f.ref = canonicalRef(ref)
		if isMessage(f.ref) {
			f.message = true
		} else {
			f.enum = true
		}
		return nil
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return fmt.Errorf("type %q", tok)
	}
	f.scalar = n
	return nil
}

func refName(tok string, refs []string) (string, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(tok, "#"))
	if err != nil || n < 0 || n >= len(refs) {
		return "", fmt.Errorf("schema ref %q", tok)
	}
	return refs[n], nil
}

func canonicalRef(name string) string {
	if name == "_LsDirectoryTreeNode" {
		return "LsDirectoryTreeNode"
	}
	return name
}

func isMessage(name string) bool {
	spec, ok := schemaSpecs[name]
	return ok && spec.desc != ""
}

// New returns an empty message of a known schema type.
func New(typeName string) (*Value, error) {
	if _, err := schemas(); err != nil {
		return nil, err
	}
	if !isMessage(typeName) {
		return nil, fmt.Errorf("unknown cursor message %s", typeName)
	}
	return &Value{Type: typeName, fields: map[string]any{}, dirty: true}, nil
}

// Decode parses protobuf bytes with the named schema. Unknown field numbers
// are kept and written back unchanged.
func Decode(typeName string, b []byte) (*Value, error) {
	if len(b) > maxMessageBytes {
		return nil, errors.New("cursor message exceeds size limit")
	}
	parsed, err := schemas()
	if err != nil {
		return nil, err
	}
	if !isMessage(typeName) {
		raw := append([]byte(nil), b...)
		return &Value{Type: typeName, raw: raw, opaque: true}, nil
	}
	v := &Value{Type: typeName, fields: map[string]any{}, raw: append([]byte(nil), b...)}
	spec := parsed[typeName]
	buf := b
	for len(buf) > 0 {
		tag, n, err := readVarint(buf)
		if err != nil {
			return nil, err
		}
		buf = buf[n:]
		num := int(tag >> 3)
		wire := int(tag & 7)
		rawStart := len(b) - len(buf) - n
		payload, used, err := readWire(buf, wire)
		if err != nil {
			return nil, err
		}
		buf = buf[used:]
		field, ok := spec.byNo[num]
		if !ok {
			v.unknown = append(v.unknown, b[rawStart:len(b)-len(buf)]...)
			continue
		}
		if err := v.consume(field, wire, payload); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", typeName, field.name, err)
		}
	}
	return v, nil
}

func (v *Value) consume(f fieldSpec, wire int, payload []byte) error {
	if f.mapField {
		if wire != 2 {
			return errors.New("map entry is not length-delimited")
		}
		key, val, err := decodeMapEntry(f, payload)
		if err != nil {
			return err
		}
		m, _ := v.fields[f.name].(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		m[key] = val
		v.fields[f.name] = m
		return nil
	}
	if f.repeated && !f.message && !f.enum && scalarWire(f.scalar) == 0 && wire == 2 {
		packed, err := unpackVarints(payload)
		if err != nil {
			return err
		}
		for _, item := range packed {
			if err := v.add(f, item); err != nil {
				return err
			}
		}
		return nil
	}
	item, err := decodeScalarish(f, wire, payload)
	if err != nil {
		return err
	}
	return v.add(f, item)
}

func (v *Value) add(f fieldSpec, item any) error {
	if f.repeated {
		v.fields[f.name] = append(asSlice(v.fields[f.name]), item)
		return nil
	}
	if f.oneof != "" {
		for name, spec := range mustSchema(v.Type).byName {
			if spec.oneof == f.oneof && name != f.name {
				delete(v.fields, name)
			}
		}
	}
	v.fields[f.name] = item
	return nil
}

func decodeScalarish(f fieldSpec, wire int, payload []byte) (any, error) {
	switch {
	case f.message:
		if wire != 2 {
			return nil, errors.New("message field has wrong wire type")
		}
		return Decode(f.ref, payload)
	case f.enum:
		if wire != 0 {
			return nil, errors.New("enum field has wrong wire type")
		}
		n, _, err := readVarint(payload)
		return int64(n), err
	default:
		return decodeScalar(f.scalar, wire, payload)
	}
}

func decodeMapEntry(f fieldSpec, payload []byte) (string, any, error) {
	var key string
	var val any
	buf := payload
	for len(buf) > 0 {
		tag, n, err := readVarint(buf)
		if err != nil {
			return "", nil, err
		}
		buf = buf[n:]
		wire := int(tag & 7)
		body, used, err := readWire(buf, wire)
		if err != nil {
			return "", nil, err
		}
		buf = buf[used:]
		switch tag >> 3 {
		case 1:
			item, err := decodeScalar(f.mapKey, wire, body)
			if err != nil {
				return "", nil, err
			}
			key = fmt.Sprint(item)
		case 2:
			child := fieldSpec{scalar: f.mapVal, ref: f.mapRef, message: f.message, enum: f.enum}
			item, err := decodeScalarish(child, wire, body)
			if err != nil {
				return "", nil, err
			}
			val = item
		}
	}
	return key, val, nil
}

func decodeScalar(scalar, wire int, payload []byte) (any, error) {
	switch scalar {
	case 1:
		if wire != 1 || len(payload) != 8 {
			return nil, errors.New("bad double")
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(payload)), nil
	case 2:
		if wire != 5 || len(payload) != 4 {
			return nil, errors.New("bad float")
		}
		return math.Float32frombits(binary.LittleEndian.Uint32(payload)), nil
	case 8:
		n, _, err := readVarint(payload)
		if err != nil || wire != 0 {
			return nil, errors.New("bad bool")
		}
		return n != 0, nil
	case 9:
		if wire != 2 {
			return nil, errors.New("bad string")
		}
		return string(payload), nil
	case 12:
		if wire != 2 {
			return nil, errors.New("bad bytes")
		}
		return append([]byte(nil), payload...), nil
	case 3, 4, 5, 13:
		if wire != 0 {
			return nil, errors.New("bad varint")
		}
		n, _, err := readVarint(payload)
		return int64(n), err
	case 17, 18:
		if wire != 0 {
			return nil, errors.New("bad sint")
		}
		n, _, err := readVarint(payload)
		return zigzagDecode(n), err
	default:
		return nil, fmt.Errorf("unsupported scalar %d", scalar)
	}
}

// Set assigns a field and marks the message for re-encoding.
func (v *Value) Set(name string, val any) {
	if v.fields == nil {
		v.fields = map[string]any{}
	}
	spec := mustSchema(v.Type).byName[name]
	if spec.oneof != "" {
		for other, f := range mustSchema(v.Type).byName {
			if f.oneof == spec.oneof && other != name {
				delete(v.fields, other)
			}
		}
	}
	v.fields[name] = val
	v.dirty = true
}

func (v *Value) Get(name string) any {
	if v == nil {
		return nil
	}
	return v.fields[name]
}

func (v *Value) String(name string) string {
	s, _ := v.Get(name).(string)
	return s
}

func (v *Value) Int(name string) int64 {
	switch n := v.Get(name).(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint32:
		return int64(n)
	default:
		return 0
	}
}

func (v *Value) Bool(name string) bool {
	b, _ := v.Get(name).(bool)
	return b
}

func (v *Value) Bytes(name string) []byte {
	b, _ := v.Get(name).([]byte)
	return b
}

func (v *Value) Message(name string) *Value {
	child, _ := v.Get(name).(*Value)
	return child
}

func (v *Value) Messages(name string) []*Value {
	items := asSlice(v.Get(name))
	out := make([]*Value, 0, len(items))
	for _, item := range items {
		if child, ok := item.(*Value); ok {
			out = append(out, child)
		}
	}
	return out
}

func (v *Value) Strings(name string) []string {
	items := asSlice(v.Get(name))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Oneof returns the set field name for a oneof group.
func (v *Value) Oneof(group string) string {
	if v == nil {
		return ""
	}
	for name, spec := range mustSchema(v.Type).byName {
		if spec.oneof == group {
			if _, ok := v.fields[name]; ok {
				return name
			}
		}
	}
	return ""
}

// Encode writes protobuf bytes. An unmodified decoded message returns its
// original payload.
func (v *Value) Encode() ([]byte, error) {
	if v == nil {
		return nil, errors.New("nil cursor message")
	}
	if v.opaque || (!v.dirty && v.raw != nil) {
		return append([]byte(nil), v.raw...), nil
	}
	parsed, err := schemas()
	if err != nil {
		return nil, err
	}
	spec, ok := parsed[v.Type]
	if !ok {
		if v.raw != nil {
			return append([]byte(nil), v.raw...), nil
		}
		return nil, fmt.Errorf("unknown cursor message %s", v.Type)
	}
	var nums []int
	for _, f := range spec.fields {
		if _, ok := v.fields[f.name]; ok {
			nums = append(nums, f.no)
		}
	}
	sort.Ints(nums)
	var out []byte
	for _, no := range nums {
		f := spec.byNo[no]
		encoded, err := encodeField(f, v.fields[f.name])
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", v.Type, f.name, err)
		}
		out = append(out, encoded...)
	}
	out = append(out, v.unknown...)
	if len(out) > maxMessageBytes {
		return nil, errors.New("cursor message exceeds size limit")
	}
	return out, nil
}

func encodeField(f fieldSpec, val any) ([]byte, error) {
	if f.mapField {
		m, _ := val.(map[string]any)
		var out []byte
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entry, err := encodeMapEntry(f, k, m[k])
			if err != nil {
				return nil, err
			}
			out = append(out, tag(f.no, 2)...)
			out = append(out, lenPrefix(entry)...)
		}
		return out, nil
	}
	if f.repeated {
		items := asSlice(val)
		if scalarWire(f.scalar) == 0 && !f.message && !f.enum && len(items) > 0 {
			var packed []byte
			for _, item := range items {
				body, _, err := encodeScalar(f.scalar, item)
				if err != nil {
					return nil, err
				}
				packed = append(packed, body...)
			}
			var out []byte
			out = append(out, tag(f.no, 2)...)
			out = append(out, lenPrefix(packed)...)
			return out, nil
		}
		var out []byte
		for _, item := range items {
			body, wire, err := encodeValue(f, item)
			if err != nil {
				return nil, err
			}
			out = append(out, tag(f.no, wire)...)
			if wire == 2 {
				out = append(out, lenPrefix(body)...)
			} else {
				out = append(out, body...)
			}
		}
		return out, nil
	}
	body, wire, err := encodeValue(f, val)
	if err != nil {
		return nil, err
	}
	out := tag(f.no, wire)
	if wire == 2 {
		out = append(out, lenPrefix(body)...)
		return out, nil
	}
	return append(out, body...), nil
}

func encodeValue(f fieldSpec, val any) ([]byte, int, error) {
	switch {
	case f.message:
		child, ok := val.(*Value)
		if !ok || child == nil {
			return nil, 0, errors.New("expected nested message")
		}
		b, err := child.Encode()
		return b, 2, err
	case f.enum:
		n, err := asInt(val)
		if err != nil {
			return nil, 0, err
		}
		return appendVarint(nil, uint64(n)), 0, nil
	default:
		return encodeScalar(f.scalar, val)
	}
}

func encodeMapEntry(f fieldSpec, key string, val any) ([]byte, error) {
	keyBody, keyWire, err := encodeScalar(f.mapKey, key)
	if err != nil {
		return nil, err
	}
	child := fieldSpec{scalar: f.mapVal, ref: f.mapRef, message: f.message, enum: f.enum}
	valBody, valWire, err := encodeValue(child, val)
	if err != nil {
		return nil, err
	}
	var out []byte
	out = append(out, tag(1, keyWire)...)
	if keyWire == 2 {
		out = append(out, lenPrefix(keyBody)...)
	} else {
		out = append(out, keyBody...)
	}
	out = append(out, tag(2, valWire)...)
	if valWire == 2 {
		out = append(out, lenPrefix(valBody)...)
	} else {
		out = append(out, valBody...)
	}
	return out, nil
}

func encodeScalar(scalar int, val any) ([]byte, int, error) {
	switch scalar {
	case 1:
		n, _ := val.(float64)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(n))
		return b[:], 1, nil
	case 2:
		n, _ := val.(float32)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(n))
		return b[:], 5, nil
	case 8:
		var n uint64
		if b, _ := val.(bool); b {
			n = 1
		}
		return appendVarint(nil, n), 0, nil
	case 9:
		s, ok := val.(string)
		if !ok && val != nil {
			return nil, 0, errors.New("expected string")
		}
		return []byte(s), 2, nil
	case 12:
		b, _ := val.([]byte)
		return append([]byte(nil), b...), 2, nil
	case 3, 4, 5, 13:
		n, err := asInt(val)
		if err != nil {
			return nil, 0, err
		}
		return appendVarint(nil, uint64(n)), 0, nil
	case 17, 18:
		n, err := asInt(val)
		if err != nil {
			return nil, 0, err
		}
		return appendVarint(nil, zigzagEncode(n)), 0, nil
	default:
		return nil, 0, fmt.Errorf("unsupported scalar %d", scalar)
	}
}

func scalarWire(scalar int) int {
	switch scalar {
	case 1:
		return 1
	case 2:
		return 5
	case 9, 12:
		return 2
	default:
		return 0
	}
}

func mustSchema(name string) parsedSchema {
	parsed, err := schemas()
	if err != nil {
		return parsedSchema{byName: map[string]fieldSpec{}}
	}
	return parsed[name]
}

func asSlice(v any) []any {
	switch items := v.(type) {
	case nil:
		return nil
	case []any:
		return items
	case []*Value:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out
	case []string:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out
	default:
		return []any{v}
	}
}

func asInt(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	default:
		return 0, errors.New("expected integer")
	}
}

func tag(field, wire int) []byte {
	return appendVarint(nil, uint64(field<<3|wire))
}

func lenPrefix(b []byte) []byte {
	return append(appendVarint(nil, uint64(len(b))), b...)
}

func appendVarint(dst []byte, n uint64) []byte {
	for n >= 0x80 {
		dst = append(dst, byte(n)|0x80)
		n >>= 7
	}
	return append(dst, byte(n))
}

func readVarint(b []byte) (uint64, int, error) {
	var n uint64
	for i := 0; i < len(b) && i < 10; i++ {
		n |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return n, i + 1, nil
		}
	}
	return 0, 0, errors.New("bad varint")
}

func readWire(b []byte, wire int) ([]byte, int, error) {
	switch wire {
	case 0:
		_, n, err := readVarint(b)
		if err != nil {
			return nil, 0, err
		}
		return b[:n], n, nil
	case 1:
		if len(b) < 8 {
			return nil, 0, errors.New("truncated fixed64")
		}
		return b[:8], 8, nil
	case 2:
		n, used, err := readVarint(b)
		if err != nil {
			return nil, 0, err
		}
		if n > maxMessageBytes || used+int(n) > len(b) {
			return nil, 0, errors.New("truncated length-delimited field")
		}
		return b[used : used+int(n)], used + int(n), nil
	case 5:
		if len(b) < 4 {
			return nil, 0, errors.New("truncated fixed32")
		}
		return b[:4], 4, nil
	default:
		return nil, 0, fmt.Errorf("unsupported wire type %d", wire)
	}
}

func unpackVarints(b []byte) ([]any, error) {
	var out []any
	for len(b) > 0 {
		n, used, err := readVarint(b)
		if err != nil {
			return nil, err
		}
		out = append(out, int64(n))
		b = b[used:]
	}
	return out, nil
}

func zigzagDecode(n uint64) int64 {
	return int64((n >> 1) ^ uint64((int64(n&1)<<63)>>63))
}

func zigzagEncode(n int64) uint64 {
	return uint64((n << 1) ^ (n >> 63))
}
