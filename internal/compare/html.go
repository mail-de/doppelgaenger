package compare

import (
	"bytes"
	"log/slog"
	"strings"

	"golang.org/x/net/html"

	"doppelgaenger/internal/backend"
)

type htmlComparator struct {
	baseComparator
	logger *slog.Logger
}

func (c *htmlComparator) Compare(primary, shadow backend.Result) (Result, error) {
	pKV, sKV, diffs, headerDiff := c.compareHeaders(primary.Header, shadow.Header)
	result := Result{
		Mode:          ModeHTML,
		HeaderDiff:    headerDiff,
		HeaderPrimary: pKV,
		HeaderShadow:  sKV,
		HeaderDiffs:   diffs,
	}

	similarity, err := compareHTMLSimilarity(primary.Body, shadow.Body)
	result.HTMLSimilarity = similarity
	result.BodyDiff = similarity < c.cfg.HTMLSimilarityThreshold
	result.Diff = result.HeaderDiff || result.BodyDiff

	if err != nil {
		result.BodyDiff = true
		result.Diff = true
	}

	return result, err
}

func compareHTMLSimilarity(primary, shadow []byte) (float64, error) {
	primaryText, err := extractHTMLText(primary)
	if err != nil {
		return 0, err
	}

	shadowText, err := extractHTMLText(shadow)
	if err != nil {
		return 0, err
	}

	primaryTokens := tokenizeHTMLText(primaryText)
	shadowTokens := tokenizeHTMLText(shadowText)

	return jaccardSimilarity(primaryTokens, shadowTokens), nil
}

func extractHTMLText(body []byte) (string, error) {
	if len(body) == 0 {
		return "", nil
	}

	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	walkHTMLText(root, &builder)

	return builder.String(), nil
}

func walkHTMLText(node *html.Node, builder *strings.Builder) {
	if node.Type == html.ElementNode {
		name := strings.ToLower(node.Data)
		if name == "script" || name == "style" || name == "noscript" {
			return
		}
	}

	if node.Type == html.TextNode {
		appendHTMLText(builder, strings.TrimSpace(node.Data))
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTMLText(child, builder)
	}
}

func appendHTMLText(builder *strings.Builder, text string) {
	if text == "" {
		return
	}

	if builder.Len() > 0 {
		builder.WriteByte(' ')
	}

	builder.WriteString(text)
}

func tokenizeHTMLText(text string) map[string]struct{} {
	normalized := normalizeText(text)
	tokens := strings.Fields(normalized)

	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}

		set[token] = struct{}{}
	}

	return set
}

func jaccardSimilarity(primary, shadow map[string]struct{}) float64 {
	if len(primary) == 0 && len(shadow) == 0 {
		return 1
	}

	if len(primary) == 0 || len(shadow) == 0 {
		return 0
	}

	intersection := 0

	for token := range primary {
		if _, ok := shadow[token]; ok {
			intersection++
		}
	}

	union := len(primary) + len(shadow) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}
