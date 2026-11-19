// Package agentmcp serves the agent catalogue over the Model Context
// Protocol (EPIC U, U1.4, FR-94 and FR-96). It is the one MCP server both
// transports run: `retnd agent serve --stdio` hands it a Caller that speaks
// to a serving engine through core/internal/apiclient, and the streamable
// HTTP endpoint in apps/common/webhost hands it a Caller that re-enters the
// /api/v1 router with the same bearer token. Neither transport adds a
// process, a socket, a mount, a shell or an engine entry point; both do
// nothing but call /api/v1.
//
// # One result, whichever door it came through
//
// A tool call answers with one object (Result): ok, code, message,
// correlation_id, command, exit_code, data, untrusted and, for a paged
// answer, next_cursor. The code, message and correlation id are the API's;
// the command is the one core/cliecho composes for the same route; the exit
// code is the one `retnd` would have ended with, from ExitCode. A refusal is
// an isError tool result, never a protocol error, so the agent reads the
// sentence an operator would have read.
//
// # What an agent reads as text and what it reads as data
//
// The text content of a result is a static line built from the tool name and
// an API error code. Anything that originates outside the deployment (a file
// name, validator output, an activity message) is named by the catalogue row's
// Untrusted list and travels only in the untrusted object, keyed by the JSON
// pointer of the place it came from, with a null left where it was. Nothing
// from outside the deployment reaches a prompt either: the prompts here are
// fixed text that name tools.
//
// # Bounded
//
// A result larger than MaxResultBytes is not truncated. The server asks the
// route for a smaller page (halving its limit) and hands back the route's own
// next_cursor, which the agent sends back as `before`. A route with no cursor
// that answers over the cap is refused AGENT_RESULT_TOO_LARGE, and nothing is
// cut.
package agentmcp
