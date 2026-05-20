package compare

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/config"
)

const compareHeaderXTest = "X-Test"

func TestHeaderComparatorDetectsHeaderDiff(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:    ModeHeader,
		CompareHeaders: []string{compareHeaderXTest},
	})

	primary := backend.Result{Header: http.Header{compareHeaderXTest: []string{"a"}}}
	shadow := backend.Result{Header: http.Header{compareHeaderXTest: []string{"b"}}}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Diff || !result.HeaderDiff {
		t.Fatalf("expected header diff to be detected")
	}

	if result.Mode != ModeHeader {
		t.Fatalf("expected visible compare mode %q, got %q", ModeHeader, result.Mode)
	}
}

func TestNginxAliasReportsHeaderMode(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:    ModeNginx,
		CompareHeaders: []string{compareHeaderXTest},
	})

	primary := backend.Result{Header: http.Header{compareHeaderXTest: []string{"a"}}}
	shadow := backend.Result{Header: http.Header{compareHeaderXTest: []string{"a"}}}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Mode != ModeHeader {
		t.Fatalf("expected legacy nginx alias to report %q, got %q", ModeHeader, result.Mode)
	}
}

func TestJSONComparatorStrictDetectsOrderDiff(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:    ModeJSON,
		CompareHeaders: []string{},
		JSONStrict:     true,
	})

	primary := backend.Result{Body: []byte(`{"a":1,"b":2}`)}
	shadow := backend.Result{Body: []byte(`{"b":2,"a":1}`)}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.BodyDiff {
		t.Fatalf("expected strict json diff for different ordering")
	}
}

func TestJSONComparatorNonStrictIgnoresOrder(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:    ModeJSON,
		CompareHeaders: []string{},
		JSONStrict:     false,
	})

	primary := backend.Result{Body: []byte(`{"a":1,"b":2}`)}
	shadow := backend.Result{Body: []byte(`{"b":2,"a":1}`)}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.BodyDiff {
		t.Fatalf("expected non-strict json comparison to ignore order")
	}
}

func TestJSONComparatorReportsPathDiffs(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:    ModeJSON,
		CompareHeaders: []string{},
		JSONStrict:     false,
	})

	primary := backend.Result{Body: []byte(`{"a":1,"b":{"c":2},"d":[1,2]}`)}
	shadow := backend.Result{Body: []byte(`{"a":2,"b":{"c":2},"d":[1,3],"e":true}`)}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.BodyDiff {
		t.Fatalf("expected json body diff")
	}

	assertPathPresent(t, result.JSONDiffs, "$.a")
	assertPathPresent(t, result.JSONDiffs, "$.d[1]")
	assertPathPresent(t, result.JSONDiffs, "$.e")
}

func TestHTMLComparatorSimilarity(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:             ModeHTML,
		CompareHeaders:          []string{},
		HTMLSimilarityThreshold: 0.8,
	})

	primary := backend.Result{Body: []byte("<html><body>Hello world</body></html>")}
	shadow := backend.Result{Body: []byte("<html><body>Hello world!</body></html>")}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.BodyDiff {
		t.Fatalf("expected html similarity to be above threshold")
	}
}

func TestHTMLComparatorDetectsDifference(t *testing.T) {
	comparator := newComparatorForTest(t, config.Config{
		CompareMode:             ModeHTML,
		CompareHeaders:          []string{},
		HTMLSimilarityThreshold: 0.8,
	})

	primary := backend.Result{Body: []byte("<html><body>Hello world</body></html>")}
	shadow := backend.Result{Body: []byte("<html><body>Goodbye</body></html>")}

	result, err := comparator.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.BodyDiff {
		t.Fatalf("expected html similarity to be below threshold")
	}
}

func newComparatorForTest(t *testing.T, cfg config.Config) Comparator {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))

	comparator, err := NewComparator(cfg, logger)
	if err != nil {
		t.Fatalf("unexpected error creating comparator: %v", err)
	}

	return comparator
}

func assertPathPresent(t *testing.T, diffs []JSONPathDiff, path string) {
	t.Helper()

	for _, diff := range diffs {
		if diff.Path == path {
			return
		}
	}

	t.Fatalf("expected diff path %q to be present", path)
}
