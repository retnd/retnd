package keycustody_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// TestInstallerArchivesThisDirectoryByItsRealName_1295 reads the installer
// source and requires the directory name to appear inside archive_plan and
// ENGINE_OWNED_CONFIG_DIRS, so renaming DirName on either side goes red.
func TestInstallerArchivesThisDirectoryByItsRealName_1295(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install", "install_docker_host.py"))
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.QuoteMeta(keycustody.DirName)
	for label, pattern := range map[string]string{
		"archive_plan":             `(?s)def archive_plan\(args\):.*?\n    \]\n`,
		"ENGINE_OWNED_CONFIG_DIRS": `ENGINE_OWNED_CONFIG_DIRS = \([^)]*\)`,
	} {
		block := regexp.MustCompile(pattern).Find(src)
		if block == nil {
			t.Fatalf("%s not found in the installer, so the pin cannot be checked", label)
		}
		if !regexp.MustCompile(`"` + name + `"`).Match(block) {
			t.Errorf("%s does not name %q (keycustody.DirName): the installer and the engine disagree about where the managed keys live", label, keycustody.DirName)
		}
	}
}
