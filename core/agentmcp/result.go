package agentmcp

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/cliecho"
)

// The exit codes `retnd` ends with for a refusal from the engine. They are
// the numbers of core/cmd/retnd's exitEngineGateShut and
// exitInsecureTransport, repeated here because that package is main and the
// HTTP transport cannot import it; core/cmd/retnd's parity test holds the two
// equal for every code the catalogue can produce.
const (
	ExitFailure           = 1
	ExitUsage             = 2
	ExitEngineGateShut    = 50
	ExitInsecureTransport = 52
)

// ExitCode is the exit status the CLI uses for an engine refusal with this
// code: 50 for a shut destructive gate, 52 for a plain-http act that needed an
// acknowledgement, 1 for everything else.
func ExitCode(code string) int {
	switch code {
	case "DESTRUCTIVE_OPERATIONS_DISABLED":
		return ExitEngineGateShut
	case "INSECURE_TRANSPORT_ACK_REQUIRED":
		return ExitInsecureTransport
	}
	return ExitFailure
}

// Codes the server itself produces, besides the API's.
const (
	CodeOK              = "OK"
	CodeInvalidRequest  = "INVALID_REQUEST"
	CodeUnreachable     = "ENGINE_UNREACHABLE"
	CodeResultTooLarge  = "AGENT_RESULT_TOO_LARGE"
	codeUnknownRefusal  = "UNKNOWN"
	staticOKMessage     = "ok"
	maxQueryKeyLength   = 64
	maxQueryValueLength = 512
)

// Result is what every tool call answers with, as structured content.
type Result struct {
	OK            bool   `json:"ok"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id"`
	// Command is the line an operator would have typed for this call, or
	// empty when the route has no verb.
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	// Data is the route's answer after the projection, with the places named
	// Untrusted left null.
	Data any `json:"data,omitempty"`
	// Untrusted holds the values that came from outside the deployment, keyed
	// by the JSON pointer of the place each one left in Data.
	Untrusted map[string]any `json:"untrusted,omitempty"`
	// NextCursor is where a paged answer ended: send it back as before.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Refusal is an answer from the engine that refuses: the API's status, code,
// message and correlation id. A Caller returns it as the error.
type Refusal struct {
	Status        int
	Code          string
	Message       string
	CorrelationID string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

var codeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// refusalResult turns an error from a Caller into the result an agent reads.
// An engine refusal keeps its code, message and correlation id; anything else
// (nothing answered, a body the contract does not describe) is the sentence
// the CLI would have printed, exit 1.
func refusalResult(err error, command string) Result {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		code := refusal.Code
		if !codeShape.MatchString(code) {
			code = codeUnknownRefusal
		}
		return Result{Code: code, Message: refusal.Message, CorrelationID: refusal.CorrelationID, Command: command, ExitCode: ExitCode(code)}
	}
	return Result{Code: CodeUnreachable, Message: err.Error(), Command: command, ExitCode: ExitFailure}
}

// invalidArguments is the result for a call whose arguments the route cannot
// be built from. It is the CLI's usage error, exit 2.
func invalidArguments(message string) Result {
	return Result{Code: CodeInvalidRequest, Message: message, ExitCode: ExitUsage}
}

// routeParams lists the {name} placeholders of a route pattern, in order.
func routeParams(route string) []string {
	var out []string
	for rest := route; ; {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			return out
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return out
		}
		out = append(out, rest[open+1:open+end])
		rest = rest[open+end+1:]
	}
}

// callRoute is the contract's path for a catalogue row. The catalogue keys a
// row by the router's own pattern, as cliecho does, and the router serves one
// route through a catch-all (GET /backup-sets/*), whose contract path spells
// the two segments it carries. The command is echoed from the router's
// pattern and the call is made on the contract's.
func callRoute(t agenttools.Row) string {
	if t.Route == "/backup-sets/*" {
		return "/backup-sets/{source}/{set}"
	}
	return t.Route
}

// echo is the command the CLI would have for this call.
func echo(t agenttools.Row, params []string, args []string, query url.Values) string {
	named := make(map[string]string, len(params))
	for i, p := range params {
		named[p] = args[i]
	}
	return cliecho.Echo(cliecho.Action{Method: t.Method, Route: t.Route, Params: named, Query: query}).Shell()
}

// splitUntrusted moves the values of the named fields out of v into a map
// keyed by JSON pointer, leaving null where each one was. A name is the last
// segment of a path such as "events[].artifact_name"; it matches a key of
// that name at any depth.
func splitUntrusted(v any, names []string) (any, map[string]any) {
	if len(names) == 0 {
		return v, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSuffix(n, "[]")
		if i := strings.LastIndexByte(n, '.'); i >= 0 {
			n = n[i+1:]
		}
		want[n] = true
	}
	out := map[string]any{}
	var walk func(v any, pointer string) any
	walk = func(v any, pointer string) any {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				p := pointer + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
				if want[k] && child != nil {
					out[p] = child
					x[k] = nil
					continue
				}
				x[k] = walk(child, p)
			}
		case []any:
			for i, child := range x {
				x[i] = walk(child, pointer+"/"+strconv.Itoa(i))
			}
		}
		return v
	}
	v = walk(v, "")
	if len(out) == 0 {
		return v, nil
	}
	return v, out
}
