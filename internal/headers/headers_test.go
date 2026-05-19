package headers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const authStatusHeader = "Auth-Status"

const (
	headerXCloseMe = "X-Close-Me"
	headerXKeepMe  = "X-Keep-Me"
)

func TestCloneIndependence(t *testing.T) {
	original := http.Header{
		"X-Test": []string{"a", "b"},
	}
	clone := Clone(original)
	clone.Set("X-Test", "c")

	if got := original.Values("X-Test"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected original header to be unchanged, got %v", got)
	}
}

func TestCompareDetailedDetectsDiff(t *testing.T) {
	primary := http.Header{}
	shadow := http.Header{}

	primary.Set(authStatusHeader, "OK")
	shadow.Set(authStatusHeader, "FAIL")

	_, _, diffs, diff := CompareDetailed(primary, shadow, []string{authStatusHeader}, false)
	if !diff {
		t.Fatalf("expected diff to be detected")
	}

	if len(diffs) != 1 {
		t.Fatalf("expected one diff, got %d", len(diffs))
	}
}

func TestWriteSelected(t *testing.T) {
	rec := httptest.NewRecorder()
	src := http.Header{}

	src.Set(authStatusHeader, "OK")
	src.Set("X-Unrelated", "skip")

	WriteSelected(rec, src, []string{authStatusHeader})

	if got := rec.Header().Get(authStatusHeader); got != "OK" {
		t.Fatalf("expected Auth-Status header to be copied, got %q", got)
	}

	if got := rec.Header().Get("X-Unrelated"); got != "" {
		t.Fatalf("expected X-Unrelated to be empty, got %q", got)
	}
}

func TestRemoveHopByHop(t *testing.T) {
	header := http.Header{}
	header.Set(headerConnection, "X-Close-Me, keep-alive")
	header.Set(headerXCloseMe, "drop")
	header.Set(headerKeepAlive, "timeout=5")
	header.Set(headerProxyConnection, "keep-alive")
	header.Set(headerTE, "trailers")
	header.Set(headerXKeepMe, "ok")

	RemoveHopByHop(header)

	for _, key := range []string{headerConnection, headerXCloseMe, headerKeepAlive, headerProxyConnection, headerTE} {
		if got := header.Get(key); got != "" {
			t.Fatalf("expected %s to be removed, got %q", key, got)
		}
	}

	if got := header.Get(headerXKeepMe); got != "ok" {
		t.Fatalf("expected X-Keep-Me to be preserved, got %q", got)
	}
}
