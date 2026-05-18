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

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/config"
)

const emptyJSONText = "<empty>"

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

func (c *jsonComparator) Compare(primary, shadow backend.Result) (Result, error) {
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
		appendJSONDiff(path, primary, shadow, diffs)

		return
	}

	switch primaryTyped := primary.(type) {
	case map[string]interface{}:
		compareJSONObject(path, primaryTyped, shadow, diffs)
	case []interface{}:
		compareJSONArray(path, primaryTyped, shadow, diffs)
	default:
		if !reflect.DeepEqual(primary, shadow) {
			appendJSONDiff(path, primary, shadow, diffs)
		}
	}
}

func compareJSONObject(path string, primary map[string]interface{}, shadow interface{}, diffs *[]JSONPathDiff) {
	shadowTyped, ok := shadow.(map[string]interface{})
	if !ok {
		appendJSONDiff(path, primary, shadow, diffs)

		return
	}

	keys := unionKeys(primary, shadowTyped)
	for _, key := range keys {
		compareJSONValues(appendJSONKey(path, key), primary[key], shadowTyped[key], diffs)
	}
}

func compareJSONArray(path string, primary []interface{}, shadow interface{}, diffs *[]JSONPathDiff) {
	shadowTyped, ok := shadow.([]interface{})
	if !ok {
		appendJSONDiff(path, primary, shadow, diffs)

		return
	}

	for i := 0; i < maxLen(len(primary), len(shadowTyped)); i++ {
		compareJSONValues(fmt.Sprintf("%s[%d]", path, i), sliceValue(primary, i), sliceValue(shadowTyped, i), diffs)
	}
}

func appendJSONDiff(path string, primary, shadow interface{}, diffs *[]JSONPathDiff) {
	*diffs = append(*diffs, JSONPathDiff{Path: path, Primary: stringifyJSONValue(primary), Shadow: stringifyJSONValue(shadow)})
}

func maxLen(primary, shadow int) int {
	if shadow > primary {
		return shadow
	}

	return primary
}

func sliceValue(values []interface{}, index int) interface{} {
	if index < len(values) {
		return values[index]
	}

	return nil
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
		primaryText = emptyJSONText
	}

	if shadowText == "" {
		shadowText = emptyJSONText
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
