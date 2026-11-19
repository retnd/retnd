package agentmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The server's own pager (#1529). A route that carries next_cursor pages
// itself and takes a limit, and server.go leaves it alone. Every other list
// route answers whole, and when that is over the cap the server cuts the
// largest array into whole slices instead of refusing the call. Nothing is cut
// silently: the result carries a cursor for the rest, and a cursor is good
// only for the answer it came from.
//
// The cursor is "retnd-page:<offset>:<fingerprint>". The fingerprint is the
// first twelve hex digits of the SHA-256 of the engine's full answer, so a list
// that moved between two calls is refused rather than walked with an offset
// that now points somewhere else.
const pageCursorPrefix = "retnd-page:"

func pageFingerprint(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:6])
}

// parsePageCursor reads a cursor the server issued. A value with the prefix
// that does not parse is not one it issued.
func parsePageCursor(v string) (offset int, fingerprint string, ok bool) {
	rest, found := strings.CutPrefix(v, pageCursorPrefix)
	if !found {
		return 0, "", false
	}
	off, fp, found := strings.Cut(rest, ":")
	n, err := strconv.Atoi(off)
	if !found || err != nil || n < 0 || len(fp) != 12 {
		return 0, "", false
	}
	return n, fp, true
}

// slicePage returns the answer with its largest array cut to the whole items
// from offset that fit in max bytes, the offset the next slice starts at and
// whether there is a next slice. ok is false when the answer has no array, the
// offset is past the end, or not even one item fits.
func slicePage(body map[string]any, offset, max int) (page map[string]any, next int, more, ok bool) {
	key, items := "", []any(nil)
	biggest := -1
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		arr, isArr := body[k].([]any)
		if !isArr {
			continue
		}
		if size := encodedSize(arr); size > biggest {
			key, items, biggest = k, arr, size
		}
	}
	if key == "" || offset > len(items) {
		return nil, 0, false, false
	}
	with := func(n int) map[string]any {
		out := make(map[string]any, len(body))
		for k, v := range body {
			out[k] = v
		}
		out[key] = slices.Clone(items[offset : offset+n])
		return out
	}
	remaining := len(items) - offset
	if remaining == 0 {
		return with(0), offset, false, true
	}
	n := sort.Search(remaining, func(i int) bool { return encodedSize(with(i+1)) > max })
	if n == 0 {
		return nil, 0, false, false
	}
	return with(n), offset + n, offset+n < len(items), true
}

func encodedSize(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}
