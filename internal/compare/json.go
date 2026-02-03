package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"unicode"

	jsoniter "github.com/json-iterator/go"

	"httpproxy/internal/backend"
	"httpproxy/internal/config"
)

type jsonComparator struct {
	baseComparator
	logger *slog.Logger
	strict bool
	api    jsoniter.API
}

func newJSONComparator(cfg config.Config, logger *slog.Logger) *jsonComparator {
	return &jsonComparator{
		baseComparator: baseComparator{cfg: cfg},
		logger:         logger,
		strict:         cfg.JSONStrict,
		api:            jsoniter.ConfigCompatibleWithStandardLibrary,
	}
}

func (c *jsonComparator) Compare(primary, shadow backend.BackendResult) (Result, error) {
	pKV, sKV, diffs, headerDiff := c.compareHeaders(primary.Header, shadow.Header)
	result := Result{
		Mode:          ModeJSON,
		HeaderDiff:    headerDiff,
		HeaderPrimary: pKV,
		HeaderShadow:  sKV,
		HeaderDiffs:   diffs,
	}

	bodyDiff, jsonDiffs, err := c.compareBodies(primary.Body, shadow.Body)
	result.BodyDiff = bodyDiff
	result.JSONDiffs = jsonDiffs
	result.Diff = result.HeaderDiff || result.BodyDiff
	if err != nil {
		result.BodyDiff = true
		result.Diff = true
	}

	return result, err
}

func (c *jsonComparator) compareBodies(primary, shadow []byte) (bool, []JSONPathDiff, error) {
	if len(primary) == 0 && len(shadow) == 0 {
		return false, nil, nil
	}
	if len(primary) == 0 || len(shadow) == 0 {
		return true, []JSONPathDiff{diffForEmpty(primary, shadow)}, nil
	}

	if c.strict {
		compactPrimary, err := compactJSON(primary)
		if err != nil {
			return true, nil, fmt.Errorf("invalid primary json: %w", err)
		}
		compactShadow, err := compactJSON(shadow)
		if err != nil {
			return true, nil, fmt.Errorf("invalid shadow json: %w", err)
		}
		if bytes.Equal(compactPrimary, compactShadow) {
			return false, nil, nil
		}
		return true, []JSONPathDiff{{Path: "$", Primary: string(compactPrimary), Shadow: string(compactShadow)}}, nil
	}

	var primaryValue interface{}
	if err := c.api.Unmarshal(primary, &primaryValue); err != nil {
		return true, nil, fmt.Errorf("invalid primary json: %w", err)
	}
	var shadowValue interface{}
	if err := c.api.Unmarshal(shadow, &shadowValue); err != nil {
		return true, nil, fmt.Errorf("invalid shadow json: %w", err)
	}

	jsonDiffs := make([]JSONPathDiff, 0)
	compareJSONValues("$", primaryValue, shadowValue, &jsonDiffs)
	return len(jsonDiffs) > 0, jsonDiffs, nil
}

func compactJSON(body []byte) ([]byte, error) {
	if !jsoniter.Valid(body) {
		return nil, fmt.Errorf("json is not valid")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compareJSONValues(path string, primary, shadow interface{}, diffs *[]JSONPathDiff) {
	if primary == nil && shadow == nil {
		return
	}
	if primary == nil || shadow == nil {
		*diffs = append(*diffs, JSONPathDiff{Path: path, Primary: stringifyJSONValue(primary), Shadow: stringifyJSONValue(shadow)})
		return
	}

	switch primaryTyped := primary.(type) {
	case map[string]interface{}:
		shadowTyped, ok := shadow.(map[string]interface{})
		if !ok {
			*diffs = append(*diffs, JSONPathDiff{Path: path, Primary: stringifyJSONValue(primary), Shadow: stringifyJSONValue(shadow)})
			return
		}
		keys := unionKeys(primaryTyped, shadowTyped)
		for _, key := range keys {
			compareJSONValues(appendJSONKey(path, key), primaryTyped[key], shadowTyped[key], diffs)
		}
	case []interface{}:
		shadowTyped, ok := shadow.([]interface{})
		if !ok {
			*diffs = append(*diffs, JSONPathDiff{Path: path, Primary: stringifyJSONValue(primary), Shadow: stringifyJSONValue(shadow)})
			return
		}
		maxLen := len(primaryTyped)
		if len(shadowTyped) > maxLen {
			maxLen = len(shadowTyped)
		}
		for i := 0; i < maxLen; i++ {
			var pValue interface{}
			var sValue interface{}
			if i < len(primaryTyped) {
				pValue = primaryTyped[i]
			}
			if i < len(shadowTyped) {
				sValue = shadowTyped[i]
			}
			compareJSONValues(fmt.Sprintf("%s[%d]", path, i), pValue, sValue, diffs)
		}
	default:
		if !reflect.DeepEqual(primary, shadow) {
			*diffs = append(*diffs, JSONPathDiff{Path: path, Primary: stringifyJSONValue(primary), Shadow: stringifyJSONValue(shadow)})
		}
	}
}

func unionKeys(primary, shadow map[string]interface{}) []string {
	keys := make([]string, 0, len(primary)+len(shadow))
	seen := make(map[string]struct{}, len(primary)+len(shadow))
	for key := range primary {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range shadow {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func appendJSONKey(path, key string) string {
	if path == "" {
		path = "$"
	}
	if isSimpleJSONKey(key) {
		return path + "." + key
	}
	escaped := strings.ReplaceAll(key, "\"", "\\\"")
	return path + "[\"" + escaped + "\"]"
}

func isSimpleJSONKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r == '_' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func stringifyJSONValue(value interface{}) string {
	if value == nil {
		return "null"
	}
	raw, err := jsoniter.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

func diffForEmpty(primary, shadow []byte) JSONPathDiff {
	primaryText := strings.TrimSpace(string(primary))
	shadowText := strings.TrimSpace(string(shadow))
	if primaryText == "" {
		primaryText = "<empty>"
	}
	if shadowText == "" {
		shadowText = "<empty>"
	}
	return JSONPathDiff{Path: "$", Primary: primaryText, Shadow: shadowText}
}

func normalizeText(value string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, value))
}
