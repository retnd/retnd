// Package recoveryperf_test measures what recovering a large artifact costs
// in memory, in the two processes that can do it (EPIC Q, #976 and #983).
//
// Nothing in this repository had ever streamed a byte out of /api/v1 before
// recovery, so the memory shape of the download path was new machinery, and
// the issue asks for it to be measured rather than assumed. This is that
// measurement, against the real binaries:
//
//   - `retnd recover --to -` is run and the process's peak resident set size
//     is read from its rusage after it exits;
//   - `retnd-web serve` is started, a recovery is minted and downloaded over
//     real HTTP, and the engine's peak resident set size is read the same way
//     after it is stopped, beside a control run of the same process that
//     served nothing, so what is attributable to the stream is the difference
//     and not the process's own baseline.
//
// It is a harness and not a gate: skipped unless RECOVERY_PERF_BYTES is set,
// so an ordinary `go test ./...` never writes gigabytes. The stated ceilings
// below are asserted when it does run, and docs/perf/epic-q-recovery.md
// records the numbers and the host from the last real run.
//
// Nothing here writes a credential anywhere. The enrollment token is read off
// a pipe and held in memory; the password is generated per run and never
// leaves memory.
package recoveryperf_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/apps/common/email/emailtest"
)

// The stated ceilings. They are generous against what a fixed 256 KiB buffer
// needs and tight against what buffering a 4 GiB artifact would cost, which is
// the only distinction they exist to draw.
const (
	// cliPeakRSSCeiling is the most `retnd recover --to -` may reach.
	cliPeakRSSCeiling = 256 << 20

	// webStreamRSSCeiling is the most the web process may grow by while
	// serving one recovery, over the same process serving none.
	webStreamRSSCeiling = 128 << 20

	csrfCookie = "retnd_csrf"
	csrfHeader = "X-CSRF-Token"
)

type processResult struct {
	PeakRSSBytes int64   `json:"peak_rss_bytes"`
	WallSeconds  float64 `json:"wall_seconds"`
	Bytes        int64   `json:"bytes"`
	SHA256       string  `json:"sha256"`
}

type record struct {
	Bytes      int64         `json:"artifact_bytes"`
	Host       host          `json:"host"`
	CLI        processResult `json:"cli_recover"`
	WebIdle    processResult `json:"web_control_no_download"`
	Web        processResult `json:"web_recover"`
	WebGrowth  int64         `json:"web_growth_over_control_bytes"`
	CLIControl processResult `json:"cli_control_status"`
}

type host struct {
	OS, Arch string
	NumCPU   int
	Model    string
	MemTotal string
	Go       string
}

func TestRecoveryMemoryOfALargeArtifact(t *testing.T) {
	raw := os.Getenv("RECOVERY_PERF_BYTES")
	if raw == "" {
		t.Skip("recovery memory harness: set RECOVERY_PERF_BYTES=4294967296 (and optionally RECOVERY_PERF_OUT) to run it")
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || size <= 0 {
		t.Fatalf("RECOVERY_PERF_BYTES=%q", raw)
	}

	root := repoRoot(t)
	work := t.TempDir()
	cli := build(t, filepath.Join(root, "core"), "./cmd/retnd", filepath.Join(work, "bin", "retnd"))
	web := build(t, filepath.Join(root, "apps", "generic"), "./cmd/retnd-web", filepath.Join(work, "bin", "retnd-web"))

	// One backup set, one artifact of the requested size, ingested by a real
	// cycle so the journal holds its SHA-256 and a local placement.
	remote := filepath.Join(work, "remote")
	local := filepath.Join(work, "local")
	state := filepath.Join(work, "state")
	for _, d := range []string{remote, local, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wantSum := writePayload(t, filepath.Join(remote, "big.dump"), size)
	cfg := filepath.Join(work, "config.yaml")
	writeConfig(t, cfg, remote, local, filepath.Join(state, "state.db"))
	if out, err := exec.Command(cli, "run", "--config", cfg).CombinedOutput(); err != nil {
		t.Fatalf("ingest: %v\n%s", err, tail(out))
	}
	const id = "perf/set/big.dump"

	rec := record{Bytes: size, Host: describeHost()}

	// Control: a command that opens the same service and reads the same
	// journal and does nothing else, so the CLI's own baseline is known.
	rec.CLIControl = runCLI(t, cli, nil, "status", "--config", cfg)

	// The CLI recovering to standard output.
	h := sha256.New()
	rec.CLI = runCLI(t, cli, h, "recover", "--config", cfg, id, "--to", "-")
	rec.CLI.SHA256 = hex.EncodeToString(h.Sum(nil))
	if rec.CLI.SHA256 != wantSum {
		t.Fatalf("the CLI recovered different bytes than were ingested: %s vs %s", rec.CLI.SHA256, wantSum)
	}
	if rec.CLI.Bytes != size {
		t.Fatalf("the CLI wrote %d bytes, want %d", rec.CLI.Bytes, size)
	}
	if rec.CLI.PeakRSSBytes > cliPeakRSSCeiling {
		t.Errorf("retnd recover peaked at %d MiB, over the %d MiB ceiling: that is buffering, not streaming", rec.CLI.PeakRSSBytes>>20, cliPeakRSSCeiling>>20)
	}

	// The web process: first a control that serves nothing, then one that
	// serves the recovery.
	rec.WebIdle = serveAndMaybeDownload(t, web, cfg, work, "", 0, nil)
	h = sha256.New()
	rec.Web = serveAndMaybeDownload(t, web, cfg, work, id, size, h)
	rec.Web.SHA256 = hex.EncodeToString(h.Sum(nil))
	if rec.Web.SHA256 != wantSum || rec.Web.Bytes != size {
		t.Fatalf("the web download returned %d bytes with sha256 %s; want %d / %s", rec.Web.Bytes, rec.Web.SHA256, size, wantSum)
	}
	rec.WebGrowth = rec.Web.PeakRSSBytes - rec.WebIdle.PeakRSSBytes
	if rec.WebGrowth > webStreamRSSCeiling {
		t.Errorf("serving one recovery grew the web process by %d MiB, over the %d MiB ceiling", rec.WebGrowth>>20, webStreamRSSCeiling>>20)
	}

	// Nothing may be left in staging once both are done.
	left, _ := filepath.Glob(filepath.Join(state, "recovery", "*"))
	if len(left) != 0 {
		t.Errorf("staging not empty after both recoveries: %v", left)
	}

	blob, _ := json.MarshalIndent(rec, "", "  ")
	t.Logf("%s", blob)
	if out := os.Getenv("RECOVERY_PERF_OUT"); out != "" {
		if err := os.WriteFile(out, append(blob, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runCLI runs the CLI, copying its standard output into sink (when given),
// and reports its peak RSS from the rusage the kernel kept.
func runCLI(t *testing.T, bin string, sink hash.Hash, args ...string) processResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var n int64
	if sink != nil {
		pr, pw, _ := os.Pipe()
		cmd.Stdout = pw
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 1<<20)
			for {
				m, err := pr.Read(buf)
				if m > 0 {
					sink.Write(buf[:m])
					n += int64(m)
				}
				if err != nil {
					return
				}
			}
		}()
		start := time.Now()
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = pw.Close()
		err := cmd.Wait()
		<-done
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return processResult{PeakRSSBytes: maxRSS(cmd), WallSeconds: time.Since(start).Seconds(), Bytes: n}
	}
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return processResult{PeakRSSBytes: maxRSS(cmd), WallSeconds: time.Since(start).Seconds()}
}

func maxRSS(cmd *exec.Cmd) int64 {
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		// Linux reports kilobytes.
		return int64(ru.Maxrss) * 1024
	}
	return 0
}

// serveAndMaybeDownload runs the web process, optionally recovers one
// artifact through it over HTTP, stops it, and reports its peak RSS.
func serveAndMaybeDownload(t *testing.T, bin, cfg, work, artifact string, size int64, sink hash.Hash) processResult {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	base := "http://" + addr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve", "--config", cfg, "--listen", addr,
		"--auth-store", filepath.Join(work, fmt.Sprintf("auth-%d.json", time.Now().UnixNano())))
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tokenCh := make(chan string, 1)
	go scanToken(stdout, tokenCh)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Transport: &http.Transport{DisableCompression: true}}
	waitReady(t, client, base)
	var token string
	select {
	case token = <-tokenCh:
	case <-time.After(15 * time.Second):
		t.Fatal("no enrollment bootstrap token")
	}
	enroll(t, client, base, token)
	time.Sleep(2 * time.Second)

	var got int64
	if artifact != "" {
		parts := strings.SplitN(artifact, "/", 3)
		body := `{"target":{"kind":"download"}}`
		req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/backups/"+parts[0]+"/"+parts[1]+"/"+parts[2]+"/recovery", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrfHeader, cookieValue(t, client, base, csrfCookie))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		mintBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("mint = %d %s", resp.StatusCode, mintBody)
		}
		var grant struct {
			DownloadURL string `json:"download_url"`
		}
		if err := json.Unmarshal(mintBody, &grant); err != nil || grant.DownloadURL == "" {
			t.Fatalf("mint body %s", mintBody)
		}
		dl, err := client.Get(base + grant.DownloadURL)
		if err != nil {
			t.Fatal(err)
		}
		if dl.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(dl.Body)
			t.Fatalf("download = %d %s", dl.StatusCode, b)
		}
		w := io.MultiWriter(sink, countWriter{&got})
		if _, err := io.Copy(w, dl.Body); err != nil {
			t.Fatalf("download body: %v", err)
		}
		dl.Body.Close()
		_ = size
	}
	elapsed := time.Since(start).Seconds()
	cancel()
	_ = cmd.Wait()
	return processResult{PeakRSSBytes: maxRSS(cmd), WallSeconds: elapsed, Bytes: got}
}

type countWriter struct{ n *int64 }

func (c countWriter) Write(p []byte) (int, error) { *c.n += int64(len(p)); return len(p), nil }

// writePayload writes size bytes of incompressible, deterministic data and
// returns their SHA-256.
func writePayload(t *testing.T, path string, size int64) string {
	t.Helper()
	f, err := os.Create(path + ".part")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	buf := make([]byte, 4<<20)
	x := uint64(0x9E3779B97F4A7C15)
	for left := size; left > 0; {
		n := int64(len(buf))
		if left < n {
			n = left
		}
		for i := int64(0); i+8 <= n; i += 8 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			for j := 0; j < 8; j++ {
				buf[i+int64(j)] = byte(x >> (8 * j))
			}
		}
		for i := n &^ 7; i < n; i++ {
			buf[i] = byte(i)
		}
		h.Write(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".part", path); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeConfig(t *testing.T, path, remote, local, db string) {
	t.Helper()
	content := "poll_interval: 15m\n" +
		"state:\n  database: " + db + "\n" +
		"sources:\n  - id: perf\n    backup_sets:\n      - id: set\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + remote + "\n" +
		"        local_path: " + local + "\n" +
		"        include:\n          - \"*.dump\"\n" +
		"        completion:\n          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, dir, pkg, out string) string {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, tail(b))
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

func tail(b []byte) string {
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return string(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitReady(t *testing.T, c *http.Client, base string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.Get(base + "/health/ready")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("engine never became ready")
}

func cookieValue(t *testing.T, c *http.Client, base, name string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/health/live", nil)
	for _, ck := range c.Jar.Cookies(req.URL) {
		if ck.Name == name {
			return ck.Value
		}
	}
	t.Fatalf("no %s cookie", name)
	return ""
}

var bootstrapTokenRE = regexp.MustCompile(`Enrollment bootstrap token: (\S+)`)

func scanToken(r io.Reader, out chan<- string) {
	sc := bufio.NewScanner(r)
	sent := false
	for sc.Scan() {
		if sent {
			continue
		}
		if m := bootstrapTokenRE.FindStringSubmatch(sc.Text()); m != nil {
			out <- m[1]
			sent = true
		}
	}
}

func enroll(t *testing.T, c *http.Client, base, bootstrap string) {
	t.Helper()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	sink := emailtest.Start(t)
	body := fmt.Sprintf(`{"username":"perf","password":%q,"recoveryEmail":"perf@example.test","smtp":{"host":%q,"port":%d,"security":"none","username":"","from":"retnd@example.test"}}`,
		password, sink.Host(), sink.Port())
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/auth/enroll", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, cookieValue(t, c, base, csrfCookie))
	req.Header.Set("X-Bootstrap-Token", bootstrap)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("enroll = %d %s", resp.StatusCode, b)
	}
	sink.WaitForMessage(t, "retnd: verify your recovery email", 10*time.Second)
}

func describeHost() host {
	h := host{OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU(), Go: runtime.Version()}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				h.Model = strings.TrimSpace(l[strings.Index(l, ":")+1:])
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "MemTotal") {
				h.MemTotal = strings.TrimSpace(strings.TrimPrefix(l, "MemTotal:"))
				break
			}
		}
	}
	return h
}
