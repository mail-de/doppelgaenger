package logging

import (
	"log/slog"
	"strings"
)

// FieldKey normalizes a selected field name for structured logs.
func FieldKey(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}

		return '_'
	}, strings.ToLower(name))
}

// SelectedFields snapshots explicitly selected values in a separate log group.
// Missing and empty values are omitted; repeated values retain their order.
func SelectedFields(group string, names []string, values func(string) []string) slog.Attr {
	attrs := make([]slog.Attr, 0, len(names))
	for _, name := range names {
		var selected []string

		for _, value := range values(name) {
			if value != "" {
				selected = append(selected, value)
			}
		}

		if len(selected) == 0 {
			continue
		}

		key := FieldKey(name)
		if len(selected) == 1 {
			attrs = append(attrs, slog.String(key, selected[0]))
		} else {
			attrs = append(attrs, slog.Any(key, selected))
		}
	}

	return slog.GroupAttrs(group, attrs...)
}
