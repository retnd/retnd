package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
)

func TestUsageIntroducesThisBinaryByName(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	defer r.Close()

	stderr := os.Stderr
	os.Stderr = w
	usage()
	os.Stderr = stderr
	if err := w.Close(); err != nil {
		t.Fatalf("close usage pipe: %v", err)
	}

	buf := make([]byte, 1<<16)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	text := string(buf[:n])

	wantFirst := "usage: " + cliecho.WebBinary + " <command> [flags]"
	if first, _, _ := strings.Cut(text, "\n"); first != wantFirst {
		t.Errorf("usage begins %q, want %q", first, wantFirst)
	}
	for _, want := range []string{
		cliecho.Binary + " status",
		cliecho.WebBinary + " auth",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage never names %q", want)
		}
	}
}

func TestTheWebHostNamesItselfFromTheConstant(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary; -short is for runs that cannot afford a compile")
	}

	bin := filepath.Join(t.TempDir(), cliecho.WebBinary)
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	run := exec.Command(bin, "no-such-command")
	out, _ := run.CombinedOutput()
	got := string(out)
	if !strings.Contains(got, cliecho.WebBinary+": unknown command") {
		t.Errorf("the diagnostic does not name cliecho.WebBinary (%q):\n%s", cliecho.WebBinary, got)
	}
	if !strings.Contains(got, "usage: "+cliecho.WebBinary+" <command>") {
		t.Errorf("the usage line does not name cliecho.WebBinary (%q):\n%s", cliecho.WebBinary, got)
	}
}
