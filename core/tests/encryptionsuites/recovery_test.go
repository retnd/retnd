package encryptionsuites_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// The clean-machine recovery row of #1319, per suite: a machine holding the
// exported recovery bundle and the object and nothing else (no state, no
// configuration, no key directory) recovers the artifact with the stock age
// command line tool at the version scripts/ci/pins.env pins, and the other
// suite's bundle recovers nothing. The tool is the pinned one even when the
// host has another age on its PATH, because "age 1.3 or newer opens an
// age-pq/v1 copy" is a claim about a version.
//
// Written by reading; not run.

// pinnedAge returns a path to the age binary at the pinned version. An age on
// PATH is used only when its version line is the pin; otherwise the pinned
// release is installed from the module proxy into a temporary GOBIN. A
// missing pin is a failure, not a skip: the row claims the pinned tool.
func pinnedAge(t *testing.T) string {
	t.Helper()
	pins := filepath.Join("..", "..", "..", "scripts", "ci", "pins.env")
	f, err := os.Open(pins)
	if err != nil {
		t.Fatalf("reading the pins: %v", err)
	}
	defer f.Close()
	var version string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "AGE_VERSION="); ok {
			version = strings.Trim(v, `"'`)
		}
	}
	if version == "" {
		t.Fatalf("%s pins no AGE_VERSION, so there is no pinned age to recover with", pins)
	}
	want := strings.TrimPrefix(version, "v")
	if bin, err := exec.LookPath("age"); err == nil {
		if out, err := exec.Command(bin, "--version").Output(); err == nil && strings.TrimPrefix(strings.TrimSpace(string(out)), "v") == want {
			return bin
		}
	}
	gobin := t.TempDir()
	install := exec.Command("go", "install", "filippo.io/age/cmd/age@v"+want)
	install.Env = append(os.Environ(), "GOBIN="+gobin, "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("installing the pinned age v%s: %v\n%s", want, err, out)
	}
	bin := filepath.Join(gobin, "age")
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || strings.TrimPrefix(strings.TrimSpace(string(out)), "v") != want {
		t.Fatalf("the installed age says %q (err=%v), the pin is v%s", out, err, want)
	}
	return bin
}

func TestACleanMachineRecoversEachSuitesCopyWithThePinnedAgeAndTheBundle_1319(t *testing.T) {
	bin := pinnedAge(t)
	r := newRig(t)
	plain := fixture(41)

	bundles := map[string][]byte{}
	for _, id := range shipped {
		var b bytes.Buffer
		if _, err := r.store.Export(r.ctx, r.keys[id].ID, &b, mintedAt.Add(2*time.Hour)); err != nil {
			t.Fatalf("exporting the %s key's bundle: %v", id, err)
		}
		bundles[id] = b.Bytes()
	}

	for _, id := range shipped {
		t.Run(id, func(t *testing.T) {
			obj, _ := r.seal(id, "artifact.dump", plain)

			clean := t.TempDir()
			bundlePath := filepath.Join(clean, "bundle.json")
			objectPath := filepath.Join(clean, "copy.age")
			if err := os.WriteFile(bundlePath, bundles[id], 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(objectPath, obj, 0o600); err != nil {
				t.Fatal(err)
			}
			if entries, _ := os.ReadDir(clean); len(entries) != 2 {
				t.Fatalf("the clean machine holds %d entries, want the bundle and the object only", len(entries))
			}

			readBundle := func(path string) keycustody.Bundle {
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				b, err := keycustody.ReadBundle(f)
				if err != nil {
					t.Fatalf("ReadBundle: %v", err)
				}
				return b
			}
			b := readBundle(bundlePath)
			if b.FormatID != id {
				t.Fatalf("the bundle names suite %q, the copy is %s", b.FormatID, id)
			}
			// The recovery lines a person follows come from the suite and
			// say which tool to use; the age-pq/v1 ones say which version.
			steps := strings.Join(b.Instructions, "\n")
			if !strings.Contains(steps, "age -d -i") {
				t.Errorf("the %s bundle's instructions do not give the age command:\n%s", id, steps)
			}
			if id == agePQV1 && !strings.Contains(steps, "1.3") {
				t.Errorf("the age-pq/v1 bundle's instructions do not say age 1.3 or newer:\n%s", steps)
			}

			identity := filepath.Join(clean, "key.txt")
			if err := os.WriteFile(identity, []byte(b.Material+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tmp := filepath.Join(clean, ".recovering")
			if out, err := exec.Command(bin, "-d", "-i", identity, "-o", tmp, objectPath).CombinedOutput(); err != nil {
				t.Fatalf("age -d on a %s copy: %v\n%s", id, err, out)
			}
			got, err := os.ReadFile(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if sum(got) != sum(plain) {
				t.Fatalf("the recovered bytes hash to %s, the artifact is %s", sum(got), sum(plain))
			}
			if err := os.Rename(tmp, filepath.Join(clean, "artifact.dump")); err != nil {
				t.Fatal(err)
			}

			// The other suite's bundle, from the same deployment, recovers
			// nothing with the same tool and leaves nothing at the output.
			other := ageV1
			if id == ageV1 {
				other = agePQV1
			}
			otherPath := filepath.Join(clean, "other-bundle.json")
			if err := os.WriteFile(otherPath, bundles[other], 0o600); err != nil {
				t.Fatal(err)
			}
			wrongID := filepath.Join(clean, "wrong-key.txt")
			if err := os.WriteFile(wrongID, []byte(readBundle(otherPath).Material+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			wrongOut := filepath.Join(clean, ".wrong")
			if out, err := exec.Command(bin, "-d", "-i", wrongID, "-o", wrongOut, objectPath).CombinedOutput(); err == nil {
				t.Fatalf("the %s bundle opened a %s copy with the stock tool:\n%s", other, id, out)
			}
			if fi, err := os.Stat(wrongOut); err == nil && fi.Size() > 0 {
				t.Fatalf("a failed age -d left %d bytes at its output", fi.Size())
			}
		})
	}
}
