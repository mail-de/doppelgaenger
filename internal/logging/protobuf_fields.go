package logging

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	protobufInt64      = "int64"
	protobufUint64     = "uint64"
	protobufSint64     = "sint64"
	protobufString     = "string"
	protobufBool       = "bool"
	maxProtobufMessage = 64 * 1024
	maxSelectedValue   = 4096
	maxSelectedTotal   = 16 * 1024
)

// ProtobufField selects a singular scalar using a protobuf field-number path.
type ProtobufField struct {
	Name string `mapstructure:"name"`
	Path []int  `mapstructure:"path"`
	Type string `mapstructure:"type"`
}

// ValidateProtobufFields rejects ambiguous aliases and unbounded selectors.
func ValidateProtobufFields(fields []ProtobufField) error {
	if len(fields) > 32 {
		return fmt.Errorf("at most 32 protobuf fields are allowed")
	}

	seen := map[string]bool{}

	for _, f := range fields {
		key := FieldKey(f.Name)
		if !ValidFieldAlias(f.Name) || seen[key] {
			return fmt.Errorf("invalid or duplicate protobuf alias %q", f.Name)
		}

		seen[key] = true

		if len(f.Path) == 0 || len(f.Path) > 8 {
			return fmt.Errorf("protobuf path must contain 1 to 8 field numbers")
		}

		for _, n := range f.Path {
			if n < 1 || n > int(protowire.MaxValidNumber) {
				return fmt.Errorf("invalid protobuf field number %d", n)
			}
		}

		switch f.Type {
		case protobufString, protobufBool, protobufInt64, protobufUint64, protobufSint64:
		default:
			return fmt.Errorf("unsupported protobuf scalar type %q", f.Type)
		}
	}

	return nil
}

// ValidFieldAlias reports whether a bounded ASCII log alias is valid.
func ValidFieldAlias(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}

	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_-.", r) {
			return false
		}
	}

	return true
}

// ProtobufCapture copies only configured scalars from the first observed message.
// A mutex permits snapshots while a streaming request is still being forwarded.
type ProtobufCapture struct {
	mu     sync.Mutex
	group  string
	fields []ProtobufField
	seen   bool
	attrs  []slog.Attr
	status string
}

// NewProtobufCapture returns nil for an empty allowlist.
func NewProtobufCapture(group string, fields []ProtobufField) *ProtobufCapture {
	if len(fields) == 0 {
		return nil
	}

	return &ProtobufCapture{group: group, fields: fields, status: "not_seen"}
}

// Observe captures at most one bounded message without retaining its payload.
func (c *ProtobufCapture) Observe(payload []byte) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.seen {
		return
	}

	c.seen = true

	c.status = "no_fields"
	if len(payload) > maxProtobufMessage {
		c.status = "message_too_large"
		return
	}

	if !validWireMessage(payload) {
		c.status = "malformed"
		return
	}

	total := 0

	for _, f := range c.fields {
		value, ok := protobufValue(payload, f.Path, f.Type)
		if !ok {
			continue
		}

		size := len(value.String())
		if total+size > maxSelectedTotal {
			continue
		}

		total += size

		c.attrs = append(c.attrs, slog.Attr{Key: FieldKey(f.Name), Value: value})
	}

	if len(c.attrs) > 0 {
		c.status = "captured"
	}
}

// Attributes returns an immutable snapshot and a bounded capture status.
func (c *ProtobufCapture) Attributes() []slog.Attr {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	attrs := append([]slog.Attr(nil), c.attrs...)

	return []slog.Attr{{Key: c.group, Value: slog.GroupValue(attrs...)}, slog.String(c.group+"_status", c.status)}
}

func validWireMessage(b []byte) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || !num.IsValid() || typ == protowire.StartGroupType || typ == protowire.EndGroupType {
			return false
		}

		b = b[n:]

		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return false
		}

		b = b[n:]
	}

	return true
}

func protobufValue(b []byte, path []int, kind string) (slog.Value, bool) {
	var (
		selected     []byte
		selectedType protowire.Type
	)

	found := false

	if !validWireMessage(b) {
		return slog.Value{}, false
	}

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		b = b[n:]

		n = protowire.ConsumeFieldValue(num, typ, b)
		if int(num) == path[0] {
			if found {
				return slog.Value{}, false
			}

			found = true
			selected = b[:n]
			selectedType = typ
		}

		b = b[n:]
	}

	if !found {
		return slog.Value{}, false
	}

	if len(path) > 1 {
		if selectedType != protowire.BytesType {
			return slog.Value{}, false
		}

		nested, _ := protowire.ConsumeBytes(selected)

		return protobufValue(nested, path[1:], kind)
	}

	return protobufScalar(selected, selectedType, kind)
}

func protobufScalar(b []byte, typ protowire.Type, kind string) (slog.Value, bool) {
	if kind == protobufString {
		if typ != protowire.BytesType {
			return slog.Value{}, false
		}

		value, _ := protowire.ConsumeBytes(b)
		if len(value) == 0 || len(value) > maxSelectedValue || !utf8.Valid(value) {
			return slog.Value{}, false
		}

		return slog.StringValue(string(value)), true
	}

	if typ != protowire.VarintType {
		return slog.Value{}, false
	}

	value, _ := protowire.ConsumeVarint(b)

	switch kind {
	case protobufBool:
		return slog.BoolValue(value != 0), true
	case protobufInt64:
		return slog.Int64Value(int64(value)), true
	case protobufUint64:
		return slog.Uint64Value(value), true
	case protobufSint64:
		return slog.Int64Value(protowire.DecodeZigZag(value)), true
	default:
		return slog.Value{}, false
	}
}
