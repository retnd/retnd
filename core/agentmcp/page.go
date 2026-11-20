package agentmcp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// The server's own pager (#1529). A route that carries next_cursor pages
// itself and takes a limit, and server.go leaves it alone. Every other list
// route answers whole, and when that is over the cap the server cuts one array
// of the answer into whole slices instead of refusing the call. Nothing is cut
// silently: the result carries a cursor for the rest.
//
// Only one array is sliced, the largest on the first call, and the cursor names
// it so the rest of the walk follows the same one. The other arrays travel whole
// on every page, so an answer whose other arrays alone are over the cap is still
// AGENT_RESULT_TOO_LARGE.
//
// The cursor is "retnd-page:<offset>:<tag>:<array>". The tag is an HMAC, under
// a key this process made at start, of the array's name and the identity of its
// items (an id when the item has one, the whole item otherwise) in order, taken
// from the projected answer. It is keyed so it tells nobody anything about a
// field the projection hides, and it covers identity, not volatile fields such
// as a progress counter, so a live list keeps its cursor while its rows change.
// An answer whose items were added, dropped or reordered refuses the cursor
// (INVALID_REQUEST), and a restart of the server does too: ask again without it.
const pageCursorPrefix = "retnd-page:"

var pageKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("agentmcp: no randomness for the page key: " + err.Error())
	}
	return k
}()

// identityKeys are the fields that name an item, first one present wins.
var identityKeys = []string{"id", "operation_id", "artifact_id", "run_id", "plan_id", "artifact", "name", "set", "medium", "backup_set_id"}

func itemIdentity(item any) string {
	if m, ok := item.(map[string]any); ok {
		for _, k := range identityKeys {
			if s, ok := m[k].(string); ok && s != "" {
				return k + "=" + s
			}
		}
	}
	raw, _ := json.Marshal(item)
	return string(raw)
}

// pageTag is the keyed fingerprint of one array of a projected answer.
func pageTag(array string, items []any) string {
	mac := hmac.New(sha256.New, pageKey)
	mac.Write([]byte(array))
	mac.Write([]byte{0})
	for _, it := range items {
		mac.Write([]byte(itemIdentity(it)))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

type pageCursor struct {
	offset int
	tag    string
	array  string
}

func (c pageCursor) String() string {
	return pageCursorPrefix + strconv.Itoa(c.offset) + ":" + c.tag + ":" + c.array
}

// parsePageCursor reads a cursor the server issued. A value with the prefix
// that does not parse is not one it issued.
func parsePageCursor(v string) (pageCursor, bool) {
	rest, found := strings.CutPrefix(v, pageCursorPrefix)
	if !found {
		return pageCursor{}, false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return pageCursor{}, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 0 || len(parts[1]) != 32 || parts[2] == "" {
		return pageCursor{}, false
	}
	return pageCursor{offset: n, tag: parts[1], array: parts[2]}, true
}

// largestArray names the array of an answer that takes the most bytes.
func largestArray(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, biggest := "", -1
	for _, k := range keys {
		if arr, ok := body[k].([]any); ok {
			if size := encodedSize(arr); size > biggest {
				best, biggest = k, size
			}
		}
	}
	return best
}

// slicePage returns the answer with the named array cut to the whole items
// from offset that fit in max bytes, the offset the next slice starts at and
// whether there is one. ok is false when the array is not there, the offset is
// past its end, or not even one item fits.
func slicePage(body map[string]any, array string, offset, max int) (page map[string]any, next int, more, ok bool) {
	items, isArr := body[array].([]any)
	if !isArr || offset >= len(items) {
		return nil, 0, false, false
	}
	with := func(n int) map[string]any {
		out := make(map[string]any, len(body))
		for k, v := range body {
			out[k] = v
		}
		out[array] = items[offset : offset+n]
		return out
	}
	n := sort.Search(len(items)-offset, func(i int) bool { return encodedSize(with(i+1)) > max })
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

// deepCopy is a copy nothing else shares, because splitUntrusted empties the
// places it moves.
func deepCopy(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}

// globalPointers moves the item index in each untrusted pointer under the
// array by offset, so "/operations/0/result" on the page that starts at item
// 40 reads "/operations/40/result", the item's place in the whole list.
func globalPointers(untrusted map[string]any, array string, offset int) map[string]any {
	if offset == 0 || len(untrusted) == 0 {
		return untrusted
	}
	prefix := "/" + array + "/"
	out := make(map[string]any, len(untrusted))
	for p, v := range untrusted {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			idx, tail, _ := strings.Cut(rest, "/")
			if n, err := strconv.Atoi(idx); err == nil {
				p = prefix + strconv.Itoa(n+offset)
				if tail != "" {
					p += "/" + tail
				}
			}
		}
		out[p] = v
	}
	return out
}
