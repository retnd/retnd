// Package agenttools is the catalogue of what an agent may do with a
// deployment: one table over the /api/v1 routes, keyed by the same route
// patterns as core/cliecho, where every route is either a tool with a scope
// and a static description or a named refusal with the reason no agent
// reaches it (EPIC U, U1.3, FR-93 and FR-98).
//
// There is no third route list. cliecho already holds every route to a
// command or a reason, and this is the same idea for the third client: a
// route the router registers with neither a row here nor an entry there fails
// a guard that names it. A tool row names its cliecho entry by its route, so
// the command a tool echoes is composed in Go, the one the browser already
// shows, and never by a client.
//
// The package holds four things. Tools, Lookup and Routes read the table.
// Denied reads the list of routes that can return key material, a recipient, a
// bundle, a credential, an SSH key, a workflow secret or a recovery grant, and
// it is the list the bearer authenticator in apps/common/webhost enforces on
// the wire, because a token works against raw /api/v1 and a deny list held
// only by a transport would hold nothing back. Project is the one projection
// every tool result passes through at the tool boundary. Document is the
// object retnd agent tools and GET /api/v1/agent/tools both print.
package agenttools
