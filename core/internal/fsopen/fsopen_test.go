//go:build unix

package fsopen

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// opened is what a call to OpenRegular returned, collected off a goroutine so
// a call that blocks is a failing test and not a hung one.
type opened struct {
	f   *os.File
	err error
}

func openWithin(t *testing.T, path string, d time.Duration) (opened, bool) {
	t.Helper()
	ch := make(chan opened, 1)
	go func() {
		f, err := OpenRegular(path)
		ch <- opened{f, err}
	}()
	select {
	case o := <-ch:
		return o, true
	case <-time.After(d):
		// Release the goroutine: a writer arriving lets the open return.
		go func() {
			if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
				w.Close()
			}
		}()
		if o := <-ch; o.f != nil {
			o.f.Close()
		}
		return opened{}, false
	}
}

func TestOpenRegularReadsARegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "copy.dump")
	if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRegular(p)
	if err != nil {
		t.Fatalf("OpenRegular: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 5)
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "bytes" {
		t.Errorf("read = %q, %v", buf[:n], err)
	}
}

func TestOpenRegularRefusesAFifoWithoutBlocking_C11(t *testing.T) {
	p := filepath.Join(t.TempDir(), "copy.dump")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("no fifos here: %v", err)
	}
	o, returned := openWithin(t, p, 2*time.Second)
	if !returned {
		t.Fatal("OpenRegular blocked on a fifo: a copy path nobody should be able to wedge a tick with")
	}
	if o.err == nil {
		o.f.Close()
		t.Fatal("OpenRegular opened a fifo")
	}
}

func TestOpenRegularRefusesWhatIsNotARegularFile_C11(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"directory": dir, "symlink": link, "missing": filepath.Join(dir, "nope")} {
		f, err := OpenRegular(p)
		if err == nil {
			f.Close()
			t.Errorf("OpenRegular(%s) succeeded", name)
		}
	}
}
