package spk

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #1109: the package identity is retnd, completely. The former name is
// built from pieces so this file carries no occurrence of it.
var (
	former1109 = strings.Join([]string{"Back", "upd"}, "")
	// The same definition of an occurrence the repository scanner uses:
	// upper-case, title-case and lower-case not followed by a lower-case
	// letter, so BackupDetailPage is left alone.
	occurrence1109 = regexp.MustCompile(
		strings.ToUpper(former1109) + "|[Bb]" + strings.ToLower(former1109)[1:] + "(?:[^a-z]|$)|" +
			former1109[:6] + "D(?:[^a-z]|$)")
)

func TestPackageIdentityIsRetnd(t *testing.T) {
	if PackageName != "retnd" {
		t.Errorf("PackageName = %q, want retnd", PackageName)
	}
	if DisplayName != "retnd" {
		t.Errorf("DisplayName = %q, want retnd", DisplayName)
	}
	if want := "SYNO.SDS._ThirdParty.App.retnd"; DSMAppName != want {
		t.Errorf("DSMAppName = %q, want %q", DSMAppName, want)
	}
}

func TestShippedAssetsNameThePackageRetnd(t *testing.T) {
	cfg, err := os.ReadFile(filepath.Join("assets", "ui", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"SYNO.SDS._ThirdParty.App.retnd"`,
		`"/webman/3rdparty/retnd/index.html"`,
	} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("assets/ui/config does not contain %s", want)
		}
	}
	res, err := os.ReadFile(filepath.Join("assets", "conf", "resource"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"rw": ["retnd"]`) {
		t.Errorf("assets/conf/resource does not grant the retnd share: %s", res)
	}
}

func TestNoFileInThePackageCarriesTheFormerName(t *testing.T) {
	seen := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen++
		for i, line := range strings.Split(string(body), "\n") {
			if occurrence1109.MatchString(line) {
				t.Errorf("%s:%d names the former project", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 10 {
		t.Fatalf("walked only %d files; the scan is not looking at the package", seen)
	}
}
