// Package reals3 is the harness for the tests that run against REAL AWS S3
// instead of MinIO or a fake (EPIC Q).
//
// Everything here is opt-in. A test calls Require, which skips unless
// RETND_TEST_S3_REAL=1 and credentials are in the environment, so CI and a
// plain `go test` never touch AWS.
//
// # Credentials
//
// They arrive as the standard AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (and
// optionally AWS_SESSION_TOKEN) environment variables, put there by whatever
// runs the tests. Nothing here writes them to disk. The product's own code
// reads them through its credentials.env source, so Require re-publishes them
// as shared-credentials text under CredentialsEnv, in this process only.
//
// # Buckets
//
// Only buckets named retnd-test-<runid>-<n> are ever created, with Block
// Public Access on, no versioning and a retnd-test=true tag. Each is emptied
// and deleted by t.Cleanup (which runs on failure too). Sweep is the second
// line of defence for a killed run: it removes the buckets of one run id and
// refuses anything that does not carry both the name prefix and the tag.
package reals3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/retnd/retnd/core/internal/transport"
)

const (
	// EnableVar turns the real-S3 tests on.
	EnableVar = "RETND_TEST_S3_REAL"
	// RunIDVar optionally pins the run id (8 hex), so a driver can sweep it.
	RunIDVar = "RETND_TEST_S3_RUNID"
	// ExpectAccountVar, when set, makes Require refuse any other account.
	ExpectAccountVar = "RETND_TEST_S3_EXPECT_ACCOUNT"
	// SweepVar names a run id for TestSweep to clean up.
	SweepVar = "RETND_TEST_S3_SWEEP"
	// CredentialsEnv is the variable the product's credentials.env source is
	// pointed at.
	CredentialsEnv = "RETND_REAL_S3_CREDS"

	// Region is where every bucket is made. us-east-1 needs no location
	// constraint.
	Region = "us-east-1"

	namePrefix = "retnd-test-"
)

var (
	runOnce sync.Once
	runID   string
	counter atomic.Int64
)

// RunID is this process's 8-hex run id.
func RunID() string {
	runOnce.Do(func() {
		if v := os.Getenv(RunIDVar); v != "" {
			runID = v
			return
		}
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		runID = hex.EncodeToString(b)
	})
	return runID
}

// Env is what Require hands back.
type Env struct {
	Client *s3.Client
}

// Require skips the test unless the real-S3 run is switched on and credentials
// are present, and otherwise returns a client plus a CredentialsEnv the
// product can read.
func Require(t testing.TB) *Env {
	t.Helper()
	if os.Getenv(EnableVar) != "1" {
		t.Skipf("real S3 tests are off; set %s=1 and AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY to run them", EnableVar)
	}
	id, secret, token := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")
	if id == "" || secret == "" {
		t.Skip("real S3 tests need AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY in the environment")
	}
	if !validRunID(RunID()) {
		t.Fatalf("%s must be 8 lowercase hex characters, got %q", RunIDVar, RunID())
	}
	ini := "[default]\naws_access_key_id = " + id + "\naws_secret_access_key = " + secret + "\n"
	if token != "" {
		ini += "aws_session_token = " + token + "\n"
	}
	t.Setenv(CredentialsEnv, ini)

	cfg := aws.Config{
		Region:      Region,
		Credentials: credentials.NewStaticCredentialsProvider(id, secret, token),
	}
	if want := os.Getenv(ExpectAccountVar); want != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			t.Fatalf("checking the AWS account: %v", err)
		}
		if aws.ToString(out.Account) != want {
			t.Fatalf("these credentials belong to account %s, not the %s this run is allowed to touch", aws.ToString(out.Account), want)
		}
	}
	return &Env{Client: s3.NewFromConfig(cfg)}
}

func validRunID(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Bucket is one real bucket this run created.
type Bucket struct {
	Name string
	e    *Env
}

// NewBucket creates retnd-test-<runid>-<n> and arranges for it to be emptied
// and deleted when the test ends.
func (e *Env) NewBucket(t testing.TB) *Bucket {
	t.Helper()
	name := fmt.Sprintf("%s%s-%d", namePrefix, RunID(), counter.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Registered first: a bucket that half-exists is still removed.
	t.Cleanup(func() { e.removeBucket(t, name) })
	if _, err := e.Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
		t.Fatalf("creating bucket %s: %v", name, err)
	}
	if _, err := e.Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(name),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls: aws.Bool(true), IgnorePublicAcls: aws.Bool(true),
			BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		t.Fatalf("blocking public access on %s: %v", name, err)
	}
	if _, err := e.Client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  aws.String(name),
		Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("retnd-test"), Value: aws.String("true")}}},
	}); err != nil {
		t.Fatalf("tagging %s: %v", name, err)
	}
	return &Bucket{Name: name, e: e}
}

// Put stores one small object.
func (b *Bucket) Put(t testing.TB, key string, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := b.e.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.Name), Key: aws.String(key), Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatalf("putting %q into %s: %v", key, b.Name, err)
	}
}

// TryPut is Put for goroutines, which must not call t.Fatalf.
func (b *Bucket) TryPut(key string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := b.e.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.Name), Key: aws.String(key), Body: bytes.NewReader(body),
	})
	return err
}

// Delete removes one object.
func (b *Bucket) Delete(t testing.TB, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := b.e.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.Name), Key: aws.String(key)}); err != nil {
		t.Fatalf("deleting %q from %s: %v", key, b.Name, err)
	}
}

// Keys is every key in the bucket, straight from the SDK (the ground truth a
// product listing is compared with).
func (b *Bucket) Keys(t testing.TB) []string {
	t.Helper()
	keys, err := b.e.listKeys(context.Background(), b.Name)
	if err != nil {
		t.Fatalf("listing %s: %v", b.Name, err)
	}
	return keys
}

// Medium is the transport.Medium that addresses this bucket the way the
// product does: no endpoint override, credentials from CredentialsEnv.
func (b *Bucket) Medium(id, prefix string) transport.Medium {
	return transport.Medium{
		ID: id, Type: transport.MediumTypeS3, Region: Region, Bucket: b.Name, Prefix: prefix,
		Credentials: transport.MediumCredentials{Env: CredentialsEnv},
	}
}

// ConfigCredentialsEnv is the CredentialsEnv text, for building a child
// process environment in memory.
func ConfigCredentialsEnv() string { return os.Getenv(CredentialsEnv) }

func (e *Env) listKeys(ctx context.Context, bucket string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(e.Client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return keys, err
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// removeBucket empties and deletes a bucket this harness named. It refuses
// any name outside the retnd-test- prefix.
func (e *Env) removeBucket(t testing.TB, name string) {
	if !strings.HasPrefix(name, namePrefix) {
		t.Errorf("refusing to remove %q: not a retnd-test- bucket", name)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := e.emptyAndDelete(ctx, name); err != nil {
		t.Errorf("TEARDOWN FAILED for bucket %s, remove it by hand: %v", name, err)
	}
}

func (e *Env) emptyAndDelete(ctx context.Context, name string) error {
	keys, err := e.listKeys(ctx, name)
	if err != nil {
		var nsb *types.NoSuchBucket
		if errors.As(err, &nsb) {
			return nil // creation never happened
		}
		return err
	}
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		ids := make([]types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			ids[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		if _, err := e.Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(name), Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		}); err != nil {
			return err
		}
		keys = keys[n:]
	}
	_, err = e.Client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
	return err
}

// Sweep removes every bucket of one run id that carries the retnd-test=true
// tag, and returns the names it removed.
func (e *Env) Sweep(ctx context.Context, run string) ([]string, error) {
	if !validRunID(run) {
		return nil, fmt.Errorf("run id %q is not 8 hex characters", run)
	}
	out, err := e.Client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, b := range out.Buckets {
		name := aws.ToString(b.Name)
		if !strings.HasPrefix(name, namePrefix+run+"-") {
			continue
		}
		tags, err := e.Client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(name)})
		if err != nil || !hasTestTag(tags.TagSet) {
			return removed, fmt.Errorf("bucket %s has the prefix but not the retnd-test=true tag; leaving it alone", name)
		}
		if err := e.emptyAndDelete(ctx, name); err != nil {
			return removed, fmt.Errorf("removing %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// ListTestBuckets lists every bucket in the account whose name starts with
// retnd-test- (all runs), for the proof that none of ours remain.
func (e *Env) ListTestBuckets(ctx context.Context) ([]string, error) {
	out, err := e.Client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range out.Buckets {
		if n := aws.ToString(b.Name); strings.HasPrefix(n, namePrefix) {
			names = append(names, n)
		}
	}
	return names, nil
}

func hasTestTag(tags []types.Tag) bool {
	for _, tg := range tags {
		if aws.ToString(tg.Key) == "retnd-test" && aws.ToString(tg.Value) == "true" {
			return true
		}
	}
	return false
}
