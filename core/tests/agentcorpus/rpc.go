package agentcorpus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("json-rpc %d: %s", e.Code, e.Message) }

// Conn is one MCP session over one transport. Call returns the raw `result`
// (or the error object), so a test scans exactly the bytes a client reads.
type Conn interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, *RPCError, error)
	// Transport names the surface for a row: "stdio" or "http".
	Transport() string
}

// ---- stdio --------------------------------------------------------------

// Stdio is `retnd agent serve --stdio` as a child process. The child reaches
// the stand-in API through RETND_API_URL, the variable every CLI verb that
// attaches to an engine reads, and takes its token from RETND_AGENT_TOKEN,
// which the spec lists beside --token-file; no token is ever on its command
// line, which is the rule the boundary drill holds.
type Stdio struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	mu     sync.Mutex
	id     int
	stderr *syncWriter
}

type syncWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// WriteConfig writes the smallest deployment a verb will load, the same one
// core/cmd/retnd's own tests use, and returns its path.
func WriteConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"remote", "local"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n      - id: postgres-primary\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + filepath.Join(dir, "remote") + "\n" +
		"        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n        stale_after: 24h\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var (
	buildOnce sync.Once
	buildDir  string
	buildBin  string
	buildErr  error
)

// BuildRetnd builds core/cmd/retnd once per test binary and returns the
// path. Built, not `go run`, so the exit status a test reads is the
// program's own. TestMain calls CleanBuild when the run is over.
func BuildRetnd(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "agentcorpus-retnd")
		if buildErr != nil {
			return
		}
		buildBin = filepath.Join(buildDir, "retnd")
		if out, err := exec.Command("go", "build", "-o", buildBin, "github.com/retnd/retnd/core/cmd/retnd").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building retnd: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildBin
}

// CleanBuild removes the directory BuildRetnd made.
func CleanBuild() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

// Token is a fresh agent token in the shape the spec's D10 decides: the rat_
// prefix is what makes a bearer an agent token.
func Token() string { return "rat_" + nonce + "_" + fmt.Sprint(time.Now().UnixNano()) }

// StartStdio starts the stdio transport against apiURL as token and performs
// the MCP handshake. The child is killed when the test ends.
func StartStdio(t *testing.T, bin, config, apiURL, token string) *Stdio {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "agent", "serve", "--stdio", "--token-file", tokenFile, "--config", config)
	cmd.Env = append(os.Environ(), "RETND_API_URL="+apiURL)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &Stdio{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20), stderr: &syncWriter{}}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting retnd agent serve --stdio: %v", err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	handshake(t, s)
	return s
}

// StderrText is what the child wrote to standard error so far: a surface of
// its own, since a log line is something an operator's agent host may relay.
func (s *Stdio) StderrText() string { return s.stderr.String() }

func (s *Stdio) Transport() string { return "stdio" }

func (s *Stdio) Call(ctx context.Context, method string, params any) (json.RawMessage, *RPCError, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id++
	id := s.id
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.in.Write(append(line, '\n')); err != nil {
		return nil, nil, fmt.Errorf("writing %s: %w", method, err)
	}
	type reply struct {
		raw []byte
		err error
	}
	for {
		ch := make(chan reply, 1)
		go func() {
			raw, err := s.out.ReadBytes('\n')
			ch <- reply{raw, err}
		}()
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("waiting for %s: %w", method, ctx.Err())
		case r := <-ch:
			if r.err != nil {
				return nil, nil, fmt.Errorf("reading the reply to %s: %w", method, r.err)
			}
			res, rpcErr, matched, err := match(r.raw, id)
			if err != nil {
				return nil, nil, err
			}
			if matched {
				return res, rpcErr, nil
			}
		}
	}
}

// Notify sends a notification, which has no id and no reply.
func (s *Stdio) Notify(method string, params any) error {
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.in.Write(append(line, '\n'))
	return err
}

// Wait blocks until the child exits or the timeout passes. The revoke drill
// uses it to ask whether a stream was closed.
func (s *Stdio) Wait(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("the process did not exit within %s", timeout)
	}
}

func match(raw []byte, id int) (json.RawMessage, *RPCError, bool, error) {
	var msg struct {
		ID     *json.RawMessage `json:"id"`
		Result json.RawMessage  `json:"result"`
		Error  *RPCError        `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &msg); err != nil {
		return nil, nil, false, fmt.Errorf("a line that is not JSON-RPC on the protocol stream: %q: %w", raw, err)
	}
	if msg.ID == nil || strings.TrimSpace(string(*msg.ID)) != fmt.Sprint(id) {
		return nil, nil, false, nil
	}
	return msg.Result, msg.Error, true, nil
}

func handshake(t *testing.T, c interface {
	Conn
	Notify(string, any) error
}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, rpcErr, err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "adversarial-corpus", "version": "0"},
	})
	if err != nil || rpcErr != nil {
		t.Fatalf("initialize over %s: %v %v", c.Transport(), err, rpcErr)
	}
	if err := c.Notify("notifications/initialized", map[string]any{}); err != nil {
		t.Fatalf("initialized over %s: %v", c.Transport(), err)
	}
}

// ---- http ---------------------------------------------------------------

// HTTP is a client of POST /api/v1/agent/mcp. Streamed (text/event-stream)
// replies are read to the matching message, which is what a client does.
type HTTP struct {
	Client  *http.Client
	URL     string
	Token   string
	session string
	mu      sync.Mutex
	id      int
}

func (h *HTTP) Transport() string { return "http" }

// Notify posts a notification; the endpoint answers 202 with no body.
func (h *HTTP) Notify(method string, params any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	resp, err := h.post(context.Background(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (h *HTTP) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+h.Token)
	h.mu.Lock()
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	h.mu.Unlock()
	c := h.Client
	if c == nil {
		c = http.DefaultClient
	}
	return c.Do(req)
}

func (h *HTTP) Call(ctx context.Context, method string, params any) (json.RawMessage, *RPCError, error) {
	h.mu.Lock()
	h.id++
	id := h.id
	h.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, nil, err
	}
	resp, err := h.post(ctx, body)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		h.session = sid
		h.mu.Unlock()
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &RPCError{Code: resp.StatusCode, Message: e.Code + ": " + e.Message, Data: raw}, nil
	}
	sc := bufio.NewReaderSize(resp.Body, 1<<20)
	for {
		line, err := sc.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		trimmed = bytes.TrimPrefix(trimmed, []byte("data:"))
		trimmed = bytes.TrimSpace(trimmed)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			res, rpcErr, matched, merr := match(trimmed, id)
			if merr != nil {
				return nil, nil, merr
			}
			if matched {
				return res, rpcErr, nil
			}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("the reply to %s ended with no matching message: %w", method, err)
		}
	}
}
