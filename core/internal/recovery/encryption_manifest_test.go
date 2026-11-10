package recovery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// EPIC N, #992 (N1.14): the sidecar carries the non-secret encryption
// metadata of an encrypted copy, so a catalog rebuild over a deployment with
// no state can tell which copies are encrypted and which key each one needs.
// That is the whole reason the metadata is here and not only in the journal:
// a procedure that needed a working deployment to learn which key a copy
// wants would not be a recovery procedure.

// canary stands in for key material. It is long and unmistakable so a
// substring search cannot hit it by accident.
const canary = "AGE-SECRET-KEY-1CANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARY"

func encryptedManifest() Manifest {
	m := testManifest()
	verified := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	m.Placements = []ManifestPlacement{
		{Medium: "local", Location: "/backups/backup-2026-08-20.dump", Status: "GONE"},
		{
			Medium: "offsite_s3", Location: "retnd/production/postgres-primary/backup-2026-08-20.dump",
			Checksum: "deadbeef", ChecksumAlgorithm: "sha256", VerificationClass: "content", Status: "ACTIVE",
			Encryption: &ManifestEncryption{
				FormatID: "age/v1", FormatVersion: 1, KeyID: "k0123456789abcdef",
				PlaintextDigest: "deadbeef", PlaintextDigestAlgorithm: "sha256", PlaintextSize: 1024,
				StoredDigest: "cafef00d", StoredDigestAlgorithm: "sha256", StoredSize: 1330,
				LastDecryptVerified: &verified,
			},
		},
	}
	return m
}

func TestTheSidecarRoundTripsTheEncryptionMetadata_992(t *testing.T) {
	m := encryptedManifest()
	data, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	got, err := DecodeManifest(data)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if len(got.Placements) != 2 || got.Placements[0].Encryption != nil {
		t.Fatalf("the plaintext local copy must carry no encryption block: %+v", got.Placements)
	}
	enc := got.Placements[1].Encryption
	if enc == nil {
		t.Fatal("the encrypted copy lost its encryption block in the round trip")
	}
	want := *m.Placements[1].Encryption
	if enc.FormatID != want.FormatID || enc.FormatVersion != want.FormatVersion || enc.KeyID != want.KeyID ||
		enc.PlaintextDigest != want.PlaintextDigest || enc.PlaintextSize != want.PlaintextSize ||
		enc.StoredDigest != want.StoredDigest || enc.StoredSize != want.StoredSize {
		t.Errorf("round trip changed the metadata: got %+v want %+v", *enc, want)
	}
	if enc.LastDecryptVerified == nil || !enc.LastDecryptVerified.Equal(*want.LastDecryptVerified) {
		t.Errorf("last decrypt-verification time = %v, want %v", enc.LastDecryptVerified, want.LastDecryptVerified)
	}
}

// A plaintext deployment's sidecar is byte for byte what it was: the block is
// omitempty on a pointer, so a manifest with no encrypted copy has no new key
// in it at all.
func TestAPlaintextSidecarGainsNoEncryptionKeys_992(t *testing.T) {
	m := testManifest()
	m.Placements = []ManifestPlacement{{Medium: "offsite_s3", Location: "k", Status: "ACTIVE"}}
	data, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "encryption") || strings.Contains(string(data), "key_id") {
		t.Errorf("a plaintext copy's sidecar mentions encryption:\n%s", data)
	}
}

// The sidecar sits in the backup root and, once a medium sidecar is uploaded,
// in the bucket: it carries the key id and never material. The canary is
// planted into a field the way a leak would, and the check that reads the
// encoded bytes must see it; then the same check over the real manifest must
// find nothing. The control is what makes the second half mean something.
func TestTheSidecarCarriesTheKeyIdAndNoMaterial_992(t *testing.T) {
	leaks := func(m Manifest) bool {
		data, err := EncodeManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(data), canary)
	}

	planted := encryptedManifest()
	planted.Placements[1].Encryption.KeyID = canary
	if !leaks(planted) {
		t.Fatal("control: a canary planted in the key id field was not found in the encoded sidecar, so this check cannot see a leak")
	}

	real := encryptedManifest()
	if leaks(real) {
		t.Fatal("the real sidecar contains the canary")
	}
	data, _ := EncodeManifest(real)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key_id": "k0123456789abcdef"`) {
		t.Errorf("the sidecar must carry the key id (not material) so a rebuild can name it:\n%s", data)
	}
}

// Validate refuses an encryption block that cannot be honest: a copy that says
// it is encrypted with no key id or no format names nothing a rebuild can act
// on, and a stored size that does not exceed the plaintext's describes
// something other than ciphertext.
func TestAnEncryptionBlockThatNamesNoKeyIsRefused_992(t *testing.T) {
	for name, mutate := range map[string]func(*ManifestEncryption){
		"no key id":              func(e *ManifestEncryption) { e.KeyID = "" },
		"no format":              func(e *ManifestEncryption) { e.FormatID = "" },
		"format version zero":    func(e *ManifestEncryption) { e.FormatVersion = 0 },
		"stored size not larger": func(e *ManifestEncryption) { e.StoredSize = e.PlaintextSize },
		"stored digest equals the plaintext digest": func(e *ManifestEncryption) { e.StoredDigest = e.PlaintextDigest },
	} {
		t.Run(name, func(t *testing.T) {
			m := encryptedManifest()
			mutate(m.Placements[1].Encryption)
			if err := m.Validate(); err == nil {
				t.Errorf("a manifest whose encryption block has %s was accepted", name)
			}
		})
	}
}
