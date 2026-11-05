package logging

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type jsonSegment struct {
	key     string
	index   bool
	pointer bool
}

// ValidateJSONSelector accepts non-root JSON pointers and singular JSONPaths.
// Multi-match operators are deliberately unsupported for log field selection.
func ValidateJSONSelector(selector string) error {
	_, err := parseJSONSelector(selector)
	return err
}

func parseJSONSelector(selector string) ([]jsonSegment, error) {
	if strings.HasPrefix(selector, "/") {
		tokens, err := ParseJSONPointer(selector)
		if err != nil {
			return nil, err
		}

		result := make([]jsonSegment, len(tokens))
		for i, token := range tokens {
			result[i] = jsonSegment{key: token, pointer: true}
		}

		return result, nil
	}

	if !strings.HasPrefix(selector, "$") || len(selector) == 1 {
		return nil, fmt.Errorf("expected a non-root JSONPath or JSON pointer")
	}

	var result []jsonSegment

	remaining := selector[1:]
	for remaining != "" {
		segment, rest, err := parseJSONPathSegment(remaining)
		if err != nil {
			return nil, err
		}

		result = append(result, segment)
		remaining = rest
	}

	return result, nil
}

func parseJSONPathSegment(input string) (jsonSegment, string, error) {
	if input[0] == '.' {
		end := 1
		for end < len(input) && jsonPathNameByte(input[end], end == 1) {
			end++
		}

		if end == 1 {
			return jsonSegment{}, "", fmt.Errorf("expected a JSONPath member name")
		}

		return jsonSegment{key: input[1:end]}, input[end:], nil
	}

	if input[0] != '[' {
		return jsonSegment{}, "", fmt.Errorf("unsupported JSONPath operator")
	}

	input = strings.TrimLeft(input[1:], " \t\r\n")
	if len(input) > 0 && (input[0] == '\'' || input[0] == '"') {
		return parseJSONPathQuotedMember(input)
	}

	return parseJSONPathIndex(input)
}

func parseJSONPathIndex(input string) (jsonSegment, string, error) {
	end := strings.IndexByte(input, ']')
	if end < 0 {
		return jsonSegment{}, "", fmt.Errorf("unterminated JSONPath index")
	}

	key := strings.TrimSpace(input[:end])

	index, err := strconv.Atoi(key)
	if err != nil || index < 0 || strconv.Itoa(index) != key {
		return jsonSegment{}, "", fmt.Errorf("expected a non-negative JSONPath array index")
	}

	return jsonSegment{key: key, index: true}, input[end+1:], nil
}

func jsonPathNameByte(value byte, first bool) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '_' || (!first && value >= '0' && value <= '9')
}

func parseJSONPathQuotedMember(input string) (jsonSegment, string, error) {
	quote := input[0]
	normalized := []byte{'"'}

	for i := 1; i < len(input); i++ {
		current := input[i]
		if current == quote {
			return finishJSONPathMember(normalized, input[i+1:])
		}

		if current == '\\' {
			i++
			if i == len(input) {
				break
			}

			if input[i] == '\'' && quote == '\'' {
				normalized = append(normalized, '\'')
			} else {
				normalized = append(normalized, '\\', input[i])
			}

			continue
		}

		if current == '"' {
			normalized = append(normalized, '\\')
		}

		normalized = append(normalized, current)
	}

	return jsonSegment{}, "", fmt.Errorf("unterminated JSONPath member")
}

func finishJSONPathMember(normalized []byte, rest string) (jsonSegment, string, error) {
	var key string
	if err := json.Unmarshal(append(normalized, '"'), &key); err != nil {
		return jsonSegment{}, "", fmt.Errorf("invalid quoted JSONPath member")
	}

	rest = strings.TrimLeft(rest, " \t\r\n")
	if !strings.HasPrefix(rest, "]") {
		return jsonSegment{}, "", fmt.Errorf("expected closing JSONPath bracket")
	}

	return jsonSegment{key: key}, rest[1:], nil
}
