package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// EPIC N, N1.1 (#989, FR-59). One new top-level key, retained_copy_encryption,
// a per-set override in Retention's three-field pattern whose whole surface is
// mode and key_provider, and the re-resolve after a mutation of the global.

func TestRetainedCopyEncryption_ANilBlockResolvesEverySetToOff(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	if c.RetainedCopyEncryptionBlock != nil {
		t.Fatalf("Validate must not write the defaults back into the operator's block, got %+v", c.RetainedCopyEncryptionBlock)
	}
	if got := c.RetainedCopyEncryption.Mode; got != RetainedCopyEncryptionOff {
		t.Errorf("deployment mode = %q, want %q on a config that never mentions the key", got, RetainedCopyEncryptionOff)
	}
	if !c.RetainedCopyEncryption.KeyProvider.IsZero() {
		t.Errorf("deployment key provider = %+v, want none", c.RetainedCopyEncryption.KeyProvider)
	}
	bs := c.Sources[0].BackupSets[0]
	if bs.RetainedCopyEncryption.Mode != RetainedCopyEncryptionOff || bs.RetainedCopyEncryption.Enabled() {
		t.Errorf("set resolved to %+v, want off", bs.RetainedCopyEncryption)
	}
	if bs.RetainedCopyEncryptionIsOverride() {
		t.Errorf("a set that wrote nothing reads as an override")
	}
}

// The defaults are complete with nothing from the operator: no cipher, no
// nonce length, no chunk size, no KDF cost. Those are not fields, so the
// schema itself is the proof, and this pins it against a field growing one.
func TestRetainedCopyEncryption_TheSchemaHasNoCryptoKnob(t *testing.T) {
	for _, forbidden := range []string{"cipher", "nonce", "chunk", "kdf", "algorithm", "format", "rotation"} {
		for _, body := range []string{
			"retained_copy_encryption:\n  mode: required\n  key_provider:\n    managed: true\n  " + forbidden + ": x\n",
		} {
			path := writeTempConfig(t, minimalYAML(body, ""))
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field "+forbidden+" not found") {
				t.Errorf("a top-level %s knob was not refused at Load: %v", forbidden, err)
			}
		}
		path := writeTempConfig(t, minimalYAML("", "        retained_copy_encryption:\n          mode: required\n          "+forbidden+": x\n"))
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field "+forbidden+" not found") {
			t.Errorf("a per-set %s knob was not refused at Load: %v", forbidden, err)
		}
	}
}

func TestRetainedCopyEncryption_AConfigThatMentionsNothingSurvivesAReMarshalByteIdentically(t *testing.T) {
	c := validConfig()
	before, err := yaml.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "retained_copy_encryption") {
		t.Fatalf("a config that names no encryption must not marshal the key:\n%s", before)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	after, err := yaml.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("Validate changed the marshalled file:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func managed() *RetainedCopyKeyProvider {
	return &RetainedCopyKeyProvider{Managed: true}
}

func external(env string) *RetainedCopyKeyProvider {
	return &RetainedCopyKeyProvider{External: &SecretSource{Env: env}}
}

func TestRetainedCopyEncryption_PerSetModesEachResolveThroughValidate(t *testing.T) {
	cases := []struct {
		name     string
		global   *RetainedCopyEncryptionBlock
		override *RetainedCopyEncryptionOverride
		mode     string
		provider string
		isOver   bool
	}{
		{name: "inherit by absence from an off deployment", mode: RetainedCopyEncryptionOff},
		{
			name:   "inherit by absence from a required deployment",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()},
			mode:   RetainedCopyEncryptionRequired, provider: KeyProviderManaged,
		},
		{
			name:     "explicit inherit follows a required global",
			global:   &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()},
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionInherit},
			mode:     RetainedCopyEncryptionRequired, provider: KeyProviderManaged, isOver: true,
		},
		{
			name:     "off beats a required global",
			global:   &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()},
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionOff},
			mode:     RetainedCopyEncryptionOff, isOver: true,
		},
		{
			name:     "required on the set borrows the deployment's provider",
			global:   &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionOff, KeyProvider: managed()},
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionRequired},
			mode:     RetainedCopyEncryptionRequired, provider: KeyProviderManaged, isOver: true,
		},
		{
			name:     "required with the set's own external provider",
			global:   &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()},
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionRequired, KeyProvider: external("RETND_COPY_KEY")},
			mode:     RetainedCopyEncryptionRequired, provider: KeyProviderExternal, isOver: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			c.RetainedCopyEncryptionBlock = tc.global
			c.Sources[0].BackupSets[0].RetainedCopyEncryptionConfig = tc.override
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			bs := c.Sources[0].BackupSets[0]
			got := bs.RetainedCopyEncryption
			if got.Mode != tc.mode {
				t.Errorf("mode = %q, want %q", got.Mode, tc.mode)
			}
			if got.KeyProvider.Kind() != tc.provider {
				t.Errorf("provider = %q, want %q", got.KeyProvider.Kind(), tc.provider)
			}
			if got.Enabled() != (tc.mode == RetainedCopyEncryptionRequired) {
				t.Errorf("Enabled() = %v for mode %q", got.Enabled(), got.Mode)
			}
			if bs.RetainedCopyEncryptionIsOverride() != tc.isOver {
				t.Errorf("IsOverride = %v, want %v", bs.RetainedCopyEncryptionIsOverride(), tc.isOver)
			}
			if tc.override != nil && *bs.RetainedCopyEncryptionConfig != *tc.override {
				t.Errorf("Validate wrote into the operator's override: %+v", bs.RetainedCopyEncryptionConfig)
			}
		})
	}
}

// A per-set inherit and an explicit off stay two different statements after
// Validate: one follows the deployment, the other has made up its own mind.
func TestRetainedCopyEncryption_InheritAndExplicitOffStayDistinguishable(t *testing.T) {
	c := validConfig()
	c.RetainedCopyEncryptionBlock = &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()}
	set := c.Sources[0].BackupSets[0]
	inherits, explicitInherit, off := set, set, set
	inherits.Name = "inherits"
	explicitInherit.Name = "explicit-inherit"
	explicitInherit.LocalPath = "/backups/production/explicit"
	explicitInherit.RetainedCopyEncryptionConfig = &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionInherit}
	off.Name = "off"
	off.LocalPath = "/backups/production/off"
	off.RetainedCopyEncryptionConfig = &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionOff}
	c.Sources[0].BackupSets = []BackupSet{inherits, explicitInherit, off}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	sets := c.Sources[0].BackupSets
	if sets[0].RetainedCopyEncryption.Mode != RetainedCopyEncryptionRequired || sets[0].RetainedCopyEncryptionIsOverride() {
		t.Errorf("inherit by absence: %+v override=%v", sets[0].RetainedCopyEncryption, sets[0].RetainedCopyEncryptionIsOverride())
	}
	if sets[1].RetainedCopyEncryption.Mode != RetainedCopyEncryptionRequired || !sets[1].RetainedCopyEncryptionIsOverride() {
		t.Errorf("explicit inherit: %+v override=%v", sets[1].RetainedCopyEncryption, sets[1].RetainedCopyEncryptionIsOverride())
	}
	if sets[2].RetainedCopyEncryption.Mode != RetainedCopyEncryptionOff || !sets[2].RetainedCopyEncryptionIsOverride() {
		t.Errorf("explicit off: %+v override=%v", sets[2].RetainedCopyEncryption, sets[2].RetainedCopyEncryptionIsOverride())
	}
}

// The retention override flags were once a silent no-op because the global
// moved and nothing re-resolved. Mutate the global, re-resolve, and every
// inheriting set has to have moved with it while an explicit off stays put.
func TestRetainedCopyEncryption_MutatingTheGlobalMovesEveryInheritingSetOnReResolve(t *testing.T) {
	c := validConfig()
	set := c.Sources[0].BackupSets[0]
	second, third := set, set
	second.Name = "explicit-inherit"
	second.LocalPath = "/backups/production/second"
	second.RetainedCopyEncryptionConfig = &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionInherit}
	third.Name = "off"
	third.LocalPath = "/backups/production/third"
	third.RetainedCopyEncryptionConfig = &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionOff}
	c.Sources[0].BackupSets = []BackupSet{set, second, third}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, bs := range c.Sources[0].BackupSets {
		if bs.RetainedCopyEncryption.Mode != RetainedCopyEncryptionOff {
			t.Fatalf("%s resolved to %q before anything was enabled", bs.Name, bs.RetainedCopyEncryption.Mode)
		}
	}

	c.RetainedCopyEncryptionBlock = &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()}

	// Without a re-resolve nothing moves. That is the trap this test is
	// about, and it is asserted so the next reader knows it is real.
	for _, bs := range c.Sources[0].BackupSets {
		if bs.RetainedCopyEncryption.Mode != RetainedCopyEncryptionOff {
			t.Fatalf("%s moved without a Validate pass; the resolved field is a snapshot on purpose", bs.Name)
		}
	}

	if err := c.ResolveRetainedCopyEncryption(); err != nil {
		t.Fatalf("ResolveRetainedCopyEncryption: %v", err)
	}
	if c.RetainedCopyEncryption.Mode != RetainedCopyEncryptionRequired {
		t.Errorf("deployment mode = %q after the mutation", c.RetainedCopyEncryption.Mode)
	}
	want := map[string]string{"postgres-primary": RetainedCopyEncryptionRequired, "explicit-inherit": RetainedCopyEncryptionRequired, "off": RetainedCopyEncryptionOff}
	for _, bs := range c.Sources[0].BackupSets {
		if bs.RetainedCopyEncryption.Mode != want[bs.Name] {
			t.Errorf("%s = %q after the global moved, want %q", bs.Name, bs.RetainedCopyEncryption.Mode, want[bs.Name])
		}
		if bs.RetainedCopyEncryption.Mode == RetainedCopyEncryptionRequired && bs.RetainedCopyEncryption.KeyProvider.Kind() != KeyProviderManaged {
			t.Errorf("%s moved to required without the deployment's provider: %+v", bs.Name, bs.RetainedCopyEncryption.KeyProvider)
		}
	}
}

func TestRetainedCopyEncryption_Refusals(t *testing.T) {
	cases := []struct {
		name     string
		global   *RetainedCopyEncryptionBlock
		override *RetainedCopyEncryptionOverride
		want     string
	}{
		{
			name:   "required with no provider names the key at fault",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired},
			want:   "retained_copy_encryption.key_provider: mode required needs a key provider",
		},
		{
			name:   "an unknown global mode",
			global: &RetainedCopyEncryptionBlock{Mode: "always"},
			want:   `retained_copy_encryption.mode: "always" is not one of [off required]`,
		},
		{
			name:   "inherit is not a deployment spelling",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionInherit},
			want:   `retained_copy_encryption.mode: "inherit" is not one of [off required]`,
		},
		{
			name:   "a provider that names nothing",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: &RetainedCopyKeyProvider{}},
			want:   "retained_copy_encryption.key_provider: name exactly one of managed: true or external",
		},
		{
			name:   "a provider that names both",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: &RetainedCopyKeyProvider{Managed: true, External: &SecretSource{Env: "K"}}},
			want:   "retained_copy_encryption.key_provider: name exactly one of managed: true or external",
		},
		{
			name:   "an external reference with two sources",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: &RetainedCopyKeyProvider{External: &SecretSource{Env: "K", File: "/run/secrets/k"}}},
			want:   "retained_copy_encryption.key_provider.external:",
		},
		{
			name:   "an external reference with no source",
			global: &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: &RetainedCopyKeyProvider{External: &SecretSource{}}},
			want:   "retained_copy_encryption.key_provider.external:",
		},
		{
			name:     "a per-set required with no provider anywhere",
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionRequired},
			want:     "sources[0].backup_sets[0].retained_copy_encryption.key_provider: mode required needs a key provider, on the set or on the deployment",
		},
		{
			name:     "a per-set block that names nothing",
			override: &RetainedCopyEncryptionOverride{},
			want:     "sources[0].backup_sets[0].retained_copy_encryption: names nothing",
		},
		{
			name:     "a per-set inherit carrying a provider",
			global:   &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()},
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionInherit, KeyProvider: managed()},
			want:     "sources[0].backup_sets[0].retained_copy_encryption.key_provider: a key provider needs mode required",
		},
		{
			name:     "a per-set off carrying a provider",
			override: &RetainedCopyEncryptionOverride{Mode: RetainedCopyEncryptionOff, KeyProvider: managed()},
			want:     "sources[0].backup_sets[0].retained_copy_encryption.key_provider: a key provider needs mode required",
		},
		{
			name:     "an unknown per-set mode",
			override: &RetainedCopyEncryptionOverride{Mode: "custom"},
			want:     `sources[0].backup_sets[0].retained_copy_encryption.mode: "custom" is not one of [inherit off required]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			c.RetainedCopyEncryptionBlock = tc.global
			c.Sources[0].BackupSets[0].RetainedCopyEncryptionConfig = tc.override
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate accepted it; want a refusal containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// A deployment that declares a provider under mode off is staging it, the
// way a storage medium no tier names yet is staged. It is accepted and the
// provider is resolved, so a later flip to required has it already.
func TestRetainedCopyEncryption_AStagedProviderUnderOffIsAccepted(t *testing.T) {
	c := validConfig()
	c.RetainedCopyEncryptionBlock = &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionOff, KeyProvider: external("RETND_COPY_KEY")}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.RetainedCopyEncryption.Mode != RetainedCopyEncryptionOff || c.RetainedCopyEncryption.KeyProvider.Kind() != KeyProviderExternal {
		t.Errorf("resolved %+v", c.RetainedCopyEncryption)
	}
	if ref := c.RetainedCopyEncryption.KeyProvider.ExternalRef(); ref.Env != "RETND_COPY_KEY" {
		t.Errorf("ExternalRef = %+v", ref)
	}
}

// Inline key material has no field to land in, so it is refused at Load
// under KnownFields(true), the same door 53-invalid-inline-medium-secret
// proves for a medium credential. The value here is a placeholder, not a
// key.
func TestRetainedCopyEncryption_InlineKeyMaterialIsRefusedAtLoad(t *testing.T) {
	for _, field := range []string{"identity", "key", "secret", "value"} {
		path := writeTempConfig(t, minimalYAML("retained_copy_encryption:\n  mode: required\n  key_provider:\n    "+field+": NOT-A-KEY-PLACEHOLDER\n", ""))
		_, err := Load(path)
		if err == nil {
			t.Fatalf("Load accepted inline material under key_provider.%s", field)
		}
		if !strings.Contains(err.Error(), "field "+field+" not found in type config.RetainedCopyKeyProvider") {
			t.Errorf("Load refused for another reason: %v", err)
		}
	}
	// Inside the external reference too: the three sources are the whole
	// vocabulary, secretref's TestRefFieldSetMatchesTheConfiguredOnes pins it.
	path := writeTempConfig(t, minimalYAML("retained_copy_encryption:\n  mode: required\n  key_provider:\n    external:\n      identity: NOT-A-KEY-PLACEHOLDER\n", ""))
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field identity not found in type config.SecretSource") {
		t.Errorf("inline material under external was not refused at Load: %v", err)
	}
}

// Nothing outside this package reads the raw pointers. The resolved field is
// the one answer; a consumer that read the pointer would decide under the
// policy that was written rather than the one that resolved.
func TestRetainedCopyEncryption_NoConsumerReadsThePointers(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := func(path string) bool {
		return strings.Contains(filepath.ToSlash(path), "core/internal/config/")
	}
	var offenders []string
	for _, top := range []string{"core", "apps", "distribution"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed(path) {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			s := string(b)
			if strings.Contains(s, ".RetainedCopyEncryptionConfig") || strings.Contains(s, ".RetainedCopyEncryptionBlock") {
				offenders = append(offenders, path)
			}
			return nil
		})
	}
	if len(offenders) > 0 {
		t.Errorf("these files read the raw encryption pointers instead of the resolved field: %v", offenders)
	}
}

// minimalYAML is the smallest config Load accepts, with a top-level block
// and a per-set block spliced in where the caller wants them.
func minimalYAML(topLevel, perSet string) string {
	return "poll_interval: 15m\nstate:\n  database: /var/lib/retnd/state.db\n" + topLevel +
		"sources:\n  - id: production\n    backup_sets:\n      - id: postgres-primary\n        remote:\n          type: local\n" +
		"        remote_path: /srv/exports/postgres\n        local_path: /backups/production/postgres\n" +
		"        completion:\n          strategy: stable\n          stable_for: 10m\n        stale_after: 30h\n" + perSet
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
