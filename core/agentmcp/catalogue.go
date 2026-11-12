package agentmcp

// Catalogue is the read tools of core/agenttools and its projection, the one
// place this package touches that table.
//
// STUB until U1.3 (#1346) lands its table: it offers no tools and projects
// nothing, so a server built from it answers initialize and lists nothing.
// The adapter replaces this body with a loop over agenttools.Tools() that
// keeps the rows whose scope is read.
func Catalogue() ([]Tool, func(any) any) {
	return nil, func(v any) any { return v }
}
