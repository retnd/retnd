package miniointegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/reals3"
)

// #986 AC, black-box, against REAL AWS S3 (EPIC Q, #1073).
//
// recovery_blackbox_test.go proves `retnd recover` from an S3 medium against
// MinIO behind a recording proxy. Neither can say whether the product's s3
// path works against the real service: real endpoint resolution (no endpoint
// override at all), real SigV4 for the region, real key encoding, real ETags.
// These cases run the same built binary against a real bucket.
//
// They skip unless RETND_TEST_S3_REAL=1 and AWS credentials are in the
// environment (core/tests/reals3), so CI and a normal run never reach AWS.
// The credentials reach the binary only through its credentials.env source,
// as an environment variable of that one child process; nothing is written to
// disk. The archive-class (Glacier) case is deliberately NOT repeated here:
// a restore request is billed per request on real S3.

type realBlackboxWorld struct {
	*blackboxWorld
	bucket *reals3.Bucket
}

// newRealBlackboxWorld is newBlackboxWorld with a real bucket and no proxy:
// the medium has no endpoint, so the binary talks to AWS itself.
func newRealBlackboxWorld(t *testing.T, env *reals3.Env, prefix string) *realBlackboxWorld {
	t.Helper()
	ctx := context.Background()

	w := &blackboxWorld{dir: t.TempDir()}
	w.content = bytes.Repeat([]byte("the only copy of this backup is in a real AWS bucket. "), 2048)
	w.digest = sha256HexOf(w.content)
	w.artifact = model.ArtifactID{Set: model.BackupSetID{Source: bbSource, Set: bbSet}, Name: bbArtName}
	w.artifactID = w.artifact.String()

	bucket := env.NewBucket(t)
	medium := bucket.Medium(bbMedium, prefix)
	key, err := transport.MediumKey(prefix, w.artifact)
	if err != nil {
		t.Fatalf("computing the object key: %v", err)
	}
	w.key = key
	src := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(src, w.content, 0o600); err != nil {
		t.Fatalf("writing the upload source: %v", err)
	}
	// Uploaded by the product's own adapter, so the object is what a real
	// transfer would have left (including a multipart-form ETag if it chose one).
	if _, err := rclone.New().UploadFromLocal(ctx, medium, src, key, transport.UploadOptions{}); err != nil {
		t.Fatalf("putting the artifact in the bucket: %v", err)
	}

	dbPath := filepath.Join(w.dir, "state.db")
	journal, err := state.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	seedArtifactOnMedium(t, journal, w.artifact, key, w.content, bbMedium, time.Now().UTC())
	if err := journal.Close(); err != nil {
		t.Fatalf("closing the seeded journal: %v", err)
	}

	remoteDir := filepath.Join(w.dir, "remote")
	localDir := filepath.Join(w.dir, "local")
	for _, d := range []string{remoteDir, localDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}
	pfx := ""
	if prefix != "" {
		pfx = "    prefix: " + prefix + "\n"
	}
	cfg := "poll_interval: 15m\n" +
		"state:\n  database: " + dbPath + "\n" +
		"sources:\n" +
		"  - id: " + bbSource + "\n" +
		"    backup_sets:\n" +
		"      - id: " + bbSet + "\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + remoteDir + "\n" +
		"        local_path: " + localDir + "\n" +
		"        completion:\n          strategy: stable\n          stable_for: 10m\n" +
		"        stale_after: 30h\n" +
		"storage_mediums:\n" +
		"  - id: " + bbMedium + "\n" +
		"    type: s3\n" +
		"    region: " + reals3.Region + "\n" +
		"    bucket: " + bucket.Name + "\n" +
		pfx +
		"    credentials:\n      env: " + reals3.CredentialsEnv + "\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	w.configPath = filepath.Join(w.dir, "config.yaml")
	if err := os.WriteFile(w.configPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	return &realBlackboxWorld{blackboxWorld: w, bucket: bucket}
}

func (w *realBlackboxWorld) recover(t *testing.T, bin string, extra ...string) runResult {
	t.Helper()
	args := append([]string{"recover", "--config", w.configPath, w.artifactID, "--to", "restored/backup.dump", "--acknowledge-cost", "--json"}, extra...)
	return runRetndWithEnv(t, bin, []string{reals3.CredentialsEnv + "=" + reals3.ConfigCredentialsEnv()}, args...)
}

func TestBlackBox_RealS3_RecoverDeliversTheRecordedDigest(t *testing.T) {
	env := reals3.Require(t)
	bin := builtRetnd(t)
	for _, prefix := range []string{"", "retnd/offsite"} {
		name := "bucket-rooted"
		if prefix != "" {
			name = "prefix " + prefix
		}
		t.Run(name, func(t *testing.T) {
			w := newRealBlackboxWorld(t, env, prefix)

			res := w.recover(t, bin)
			if res.Exit != 0 {
				t.Fatalf("recover exited %d, want 0\nstdout: %s\nstderr: %s", res.Exit, res.Stdout, res.Stderr)
			}
			var out struct {
				Copy   string `json:"copy"`
				Path   string `json:"path"`
				Bytes  int64  `json:"bytes"`
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
				t.Fatalf("recover --json printed something that is not JSON: %q (%v)", res.Stdout, err)
			}
			if out.Copy != bbMedium {
				t.Errorf("recovered from %q, want the medium %q (it holds the only copy)", out.Copy, bbMedium)
			}
			if out.SHA256 != w.digest {
				t.Errorf("the verb reports sha256 %s, the journal recorded %s", out.SHA256, w.digest)
			}
			got, err := os.ReadFile(out.Path)
			if err != nil {
				t.Fatalf("the recovered file is not at the path the verb printed (%q): %v", out.Path, err)
			}
			if sha256HexOf(got) != w.digest {
				t.Errorf("the recovered bytes hash to %s, the recorded digest is %s", sha256HexOf(got), w.digest)
			}
			if out.Bytes != int64(len(w.content)) || int64(len(got)) != int64(len(w.content)) {
				t.Errorf("recovered %d bytes (file holds %d), want %d", out.Bytes, len(got), len(w.content))
			}
			// The bytes can only have come from the bucket: nothing else
			// holds a copy. The object must be where the key layout says.
			if !containsKey(w.bucket.Keys(t), w.key) {
				t.Errorf("the bucket does not hold %q: %v", w.key, w.bucket.Keys(t))
			}
			assertRecoveryStageEvents(t, res.Stderr, w.artifactID)
		})
	}
}

// The mirror of the case above: an object of the right size at the right key
// but with other bytes. The verb must refuse it against the recorded digest
// and place nothing, because "it came from S3" is not "it is the backup".
func TestBlackBox_RealS3_RecoverRefusesBytesThatMissTheRecordedDigest(t *testing.T) {
	env := reals3.Require(t)
	bin := builtRetnd(t)
	w := newRealBlackboxWorld(t, env, "")
	bad := bytes.Repeat([]byte("X"), len(w.content))
	w.bucket.Put(t, w.key, bad)

	res := w.recover(t, bin)
	if res.Exit == 0 {
		t.Fatalf("recover exited 0 on bytes that do not match the recorded digest\nstdout: %s", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "restored")); err == nil {
		t.Errorf("bytes were placed although they did not match the recorded digest")
	}
	if strings.Contains(res.Stdout+res.Stderr, w.digest) && strings.Contains(res.Stdout, `"path"`) {
		t.Errorf("the verb reported a recovered path: %s", res.Stdout)
	}
	t.Logf("exit %d, stdout: %s", res.Exit, res.Stdout)
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}
