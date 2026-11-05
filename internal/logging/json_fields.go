package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ParseJSONPointer parses a non-root RFC 6901 pointer, including escaped keys.
func ParseJSONPointer(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("expected a non-root JSON pointer")
	}

	for i := 0; i < len(pointer); i++ {
		if pointer[i] != '~' {
			continue
		}

		if i+1 == len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
			return nil, fmt.Errorf("invalid JSON pointer escape")
		}

		i++
	}

	tokens := strings.Split(pointer[1:], "/")
	for i, token := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}

	return tokens, nil
}

// SelectedJSONFields captures explicitly selected scalar values without logging
// complete objects, invalid bodies, or parse errors containing request data.
func SelectedJSONFields(group string, fields map[string]string, body []byte) slog.Attr {
	if len(fields) == 0 || len(body) == 0 {
		return slog.Attr{}
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return slog.Attr{}
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return slog.Attr{}
	}

	attrs := make([]slog.Attr, 0, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		tokens, err := parseJSONSelector(fields[name])
		if err != nil {
			continue
		}

		value := resolveJSONPointer(document, tokens)
		if attr := jsonScalarAttr(FieldKey(name), value); !attr.Equal(slog.Attr{}) {
			attrs = append(attrs, attr)
		}
	}

	return slog.GroupAttrs(group, attrs...)
}

func resolveJSONPointer(value any, tokens []jsonSegment) any {
	for _, token := range tokens {
		switch node := value.(type) {
		case map[string]any:
			if token.index {
				return nil
			}

			value = node[token.key]
		case []any:
			if !token.index && !token.pointer {
				return nil
			}

			index, err := strconv.Atoi(token.key)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != token.key {
				return nil
			}

			value = node[index]
		default:
			return nil
		}
	}

	return value
}

func jsonScalarAttr(key string, value any) slog.Attr {
	switch scalar := value.(type) {
	case string:
		if scalar != "" {
			return slog.String(key, scalar)
		}
	case bool, json.Number:
		return slog.Any(key, scalar)
	}

	return slog.Attr{}
}
