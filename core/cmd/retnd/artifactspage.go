package main

import "fmt"

// pageWindow is `artifacts --limit N --offset M` (issue #1180): the terminal's
// version of the Backups page's "Rows per page" and Previous/Next. The API's
// list answers with every artifact, as the web page does before it cuts the
// list into pages, so the window is cut here, after every filter, and the
// total it reports is the filtered list's, not the journal's.
type pageWindow struct{ limit, offset int }

// parsePageFlags reads --limit and --offset; a negative number is refused.
func parsePageFlags(limit, offset int) (pageWindow, bool) {
	if limit < 0 || offset < 0 {
		return pageWindow{}, false
	}
	return pageWindow{limit: limit, offset: offset}, true
}

// set is true when either flag was given, which is when the extra line prints.
func (w pageWindow) set() bool { return w.limit > 0 || w.offset > 0 }

func windowOf[T any](w pageWindow, rows []T) []T {
	if w.offset >= len(rows) {
		return rows[:0]
	}
	rows = rows[w.offset:]
	if w.limit > 0 && w.limit < len(rows) {
		rows = rows[:w.limit]
	}
	return rows
}

// describe says which rows were printed and how to ask for the next ones.
func (w pageWindow) describe(shown, total int) string {
	if shown == 0 {
		return fmt.Sprintf("showing 0 of %d (--offset %d is past the end)", total, w.offset)
	}
	first, last := w.offset+1, w.offset+shown
	line := fmt.Sprintf("showing %d-%d of %d", first, last, total)
	if last < total {
		line += fmt.Sprintf(" (--offset %d for the next page)", last)
	}
	return line
}
