// Package headers contains helpers for copying and comparing HTTP headers.
package headers

import (
	"net/http"
	"strings"
)

// HeaderDiff represents a difference in headers between primary and shadow.
type HeaderDiff struct {
	Key     string `json:"key"`
	Primary string `json:"primary"`
	Shadow  string `json:"shadow"`
}

// Clone creates a deep copy of an http.Header.
func Clone(h http.Header) http.Header {
	cp := make(http.Header, len(h))
	for k, vv := range h {
		nv := make([]string, len(vv))
		copy(nv, vv)
		cp[k] = nv
	}

	return cp
}

// CompareDetailed compares specified headers between primary and shadow responses.
func CompareDetailed(primary http.Header, shadow http.Header, keys []string, includeEmpty bool) (p map[string]string, s map[string]string, diffs []HeaderDiff, diff bool) {
	p = make(map[string]string, len(keys))
	s = make(map[string]string, len(keys))
	diffs = make([]HeaderDiff, 0, len(keys))

	for _, key := range keys {
		ck := http.CanonicalHeaderKey(key)
		pv := strings.Join(primary.Values(ck), ",")
		sv := strings.Join(shadow.Values(ck), ",")

		if !includeEmpty && pv == "" && sv == "" {
			continue
		}

		p[ck] = pv
		s[ck] = sv

		if pv != sv {
			diff = true

			diffs = append(diffs, HeaderDiff{Key: ck, Primary: pv, Shadow: sv})
		}
	}

	return
}

// WriteSelected writes only the allowed headers from src to w.
func WriteSelected(w http.ResponseWriter, src http.Header, allow []string) {
	allowSet := make(map[string]struct{}, len(allow))
	for _, k := range allow {
		allowSet[http.CanonicalHeaderKey(k)] = struct{}{}
	}

	for k, vv := range src {
		ck := http.CanonicalHeaderKey(k)
		if _, ok := allowSet[ck]; !ok {
			continue
		}

		w.Header().Del(ck)

		for _, v := range vv {
			w.Header().Add(ck, v)
		}
	}
}
