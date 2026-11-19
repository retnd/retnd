package packaging

import (
	"path"
	"path/filepath"
	"testing"
)

// #1246: custom validators are written by the engine, and the workflow tree is
// mounted read-only, so every runtime that carries the tree also has to carry
// the two validator directories writable over it, from the same host tree.
// Without them a profile runs hooks and refuses every validator upload.
func TestEveryStackThatMountsTheHookTreeCarriesTheValidatorDirectories(t *testing.T) {
	t.Parallel()

	c := MustLoad()
	stacks := []struct{ name, compose, env string }{
		{"canonical", filepath.Join(RepoRoot, "container", "compose.yaml"), filepath.Join(RepoRoot, "container", ".env.example")},
		{"casaos", filepath.Join(PlatformDir("casaos"), "compose", "retnd.yml"), ""},
		{"portainer", filepath.Join(PlatformDir("portainer"), "compose", "retnd.yml"), filepath.Join(PlatformDir("portainer"), "compose", "retnd.env")},
		{"proxmox", filepath.Join(PlatformDir("proxmox"), "compose", "retnd.yml"), filepath.Join(PlatformDir("proxmox"), "compose", "retnd.env")},
		{"openmediavault", filepath.Join(PlatformDir("openmediavault"), "compose", "retnd.yml"), filepath.Join(PlatformDir("openmediavault"), "compose", "retnd.env")},
	}
	for _, s := range stacks {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()

			var env map[string]string
			if s.env != "" {
				var err error
				if env, err = ReadEnvFile(s.env); err != nil {
					t.Fatalf("read %s: %v", s.env, err)
				}
			}
			svcs, err := ReadCompose(s.compose, env)
			if err != nil {
				t.Fatalf("read %s: %v", s.compose, err)
			}
			carried := false
			for _, svc := range svcs {
				byRole := map[string]Mount{}
				for _, m := range svc.Mounts {
					byRole[m.Role] = m
				}
				tree, ok := byRole["workflows"]
				if !ok {
					continue
				}
				carried = true
				for _, role := range ValidatorRoles {
					want, _ := c.ContainerPaths.ByRole(role)
					m, ok := byRole[role]
					if !ok {
						t.Errorf("service %s mounts the hook tree and nothing at %s, so every validator upload is refused", svc.Name, want)
						continue
					}
					if m.ReadOnly {
						t.Errorf("service %s mounts %s read-only; the engine writes validator scripts there", svc.Name, want)
					}
					if wantHost := path.Join(tree.HostPath, path.Base(want)); path.Clean(m.HostPath) != wantHost {
						t.Errorf("service %s mounts %s from %s, want %s inside the same host tree the hook scripts come from", svc.Name, want, m.HostPath, wantHost)
					}
				}
			}
			if !carried {
				t.Fatalf("%s mounts no hook tree, so this test is not looking at what it is about", s.name)
			}
		})
	}
}
