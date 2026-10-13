package rclone

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/transport"
)

// This file guards the S3 surface at its two edges: what configuration can
// reach rclone's s3 backend, and what code can reach an AWS SDK.
//
// Neither is a behaviour anybody would notice going wrong. rclone's s3
// backend takes well over a hundred options, and the ones that matter here
// (assume-role, the SSE-C family, download_url, presigned requests, v2
// signing, versioned views, ACL headers) change WHERE a backup goes or WHO
// writes it, silently and successfully. What keeps them out is that
// s3Options is an allowlist, and an allowlist is only an allowlist while
// something checks it, so the first test measures the complete producible
// key set in both directions: a key that appeared fails, and so does one
// that vanished, because losing `endpoint` sends a backup to AWS instead
// of the configured service and losing `no_check_bucket` lets this adapter
// provision a bucket somebody mistyped.
//
// The second test is FR-28's "no AWS SDK enters this repository" read the
// only way it can honestly be read. Registering rclone's s3 backend DOES
// put aws-sdk-go-v2 in go.mod, and no test can make that untrue; what
// would be a mistake is a SECOND S3 implementation written against the SDK
// directly, outside the FR-3 boundary and outside rclone's upgrade
// contract. So it scans import lines this repository OWNS rather than
// checking go.mod, which cannot tell the two apart, and it fails on a file
// it could not parse rather than skipping it, because an unparsed file is
// an unchecked one. Its file count is a positive control: a walk that
// visited nothing reports no offenders and looks exactly like a clean
// repository.

// TestS3OptionsAreExactlyThisAllowlist pins the COMPLETE set of rclone s3
// options this adapter can ever produce.
//
// rclone's s3 backend takes well over a hundred options. What keeps this
// product's blast radius small is not that nothing sets the dangerous ones
// today, it is that there is no path from configuration to them at all:
// s3Options is an allowlist, the way sftpConfig is for a Source. Assume
// role, the SSE-C family, download_url, presigned requests, v2 signing,
// versioned views and ACL headers are all unreachable by construction, and
// every one of them is a way to change WHERE a backup goes or WHO writes
// it.
//
// The test asserts both directions on purpose. "No unexpected key" alone
// would pass if s3Options stopped setting something load-bearing (an
// endpoint, say, which would send a backup to AWS instead of the
// configured service); "every expected key" alone would pass if it grew a
// new one. Only both together say what this file means.
func TestS3OptionsAreExactlyThisAllowlist(t *testing.T) {
	for _, name := range ambientAWSCredentialEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("MEDIUM_CREDS", "[default]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = sekrit\naws_session_token = tok\n")

	// Every field a Medium can carry is populated, so the widest set of
	// keys this function can produce is the one being measured.
	medium := transport.Medium{
		ID:           "offsite_s3",
		Type:         transport.MediumTypeS3,
		Region:       "us-west-2",
		Endpoint:     "https://s3.example.invalid",
		Bucket:       "backups",
		Prefix:       "retnd",
		StorageClass: "STANDARD_IA",
		Credentials:  transport.MediumCredentials{Env: "MEDIUM_CREDS"},
	}

	want := map[string]string{
		"env_auth":          "false",
		"access_key_id":     "AKIAEXAMPLE",
		"secret_access_key": "sekrit",
		"session_token":     "tok",
		"provider":          "Other",
		"endpoint":          "https://s3.example.invalid",
		"region":            "us-west-2",
		"storage_class":     "STANDARD_IA",
		"no_check_bucket":   "true",
	}

	cfg, err := s3Options(medium)
	if err != nil {
		t.Fatalf("s3Options: %v", err)
	}
	for key, value := range cfg {
		expected, ok := want[key]
		if !ok {
			t.Errorf("s3Options set %q, which is not on the allowlist. If this option really is needed, add it here WITH "+
				"its reason: every rclone s3 option is a way for configuration to reach the backend, and most of them "+
				"can change where a backup goes or who writes it", key)
			continue
		}
		if value != expected {
			t.Errorf("s3Options set %q = %q, want %q", key, value, expected)
		}
	}
	for key := range want {
		if _, ok := cfg[key]; !ok {
			t.Errorf("s3Options no longer sets %q. Each of these does a job: env_auth decides whether the AWS credential "+
				"chain is consulted at all, no_check_bucket is what stops this adapter provisioning a bucket somebody "+
				"mistyped, and endpoint is the difference between the configured service and AWS", key)
		}
	}

	t.Run("the file source produces the other two credential keys and no more", func(t *testing.T) {
		path := writeCreds(t, goodCredsBody)
		fileMedium := medium
		fileMedium.Credentials = transport.MediumCredentials{File: path}
		cfg, err := s3Options(fileMedium)
		if err != nil {
			t.Fatalf("s3Options: %v", err)
		}
		fileWant := map[string]string{
			"env_auth":                "true",
			"shared_credentials_file": path,
			"provider":                "Other",
			"endpoint":                "https://s3.example.invalid",
			"region":                  "us-west-2",
			"storage_class":           "STANDARD_IA",
			"no_check_bucket":         "true",
		}
		for key := range cfg {
			if _, ok := fileWant[key]; !ok {
				t.Errorf("the file source set %q, which is not on its allowlist", key)
			}
		}
		for key, value := range fileWant {
			if got, ok := cfg[key]; !ok || got != value {
				t.Errorf("the file source set %q = %q (present=%v), want %q", key, got, ok, value)
			}
		}
	})

	t.Run("no endpoint means AWS and no endpoint key", func(t *testing.T) {
		awsMedium := medium
		awsMedium.Endpoint = ""
		cfg, err := s3Options(awsMedium)
		if err != nil {
			t.Fatalf("s3Options: %v", err)
		}
		if got, _ := cfg.Get("provider"); got != "AWS" {
			t.Errorf("provider = %q, want AWS", got)
		}
		if _, ok := cfg.Get("endpoint"); ok {
			t.Error("an endpoint key was set for a medium that configured none")
		}
	})
}

// TestNoCloudProviderSDKIsImportedDirectly is FR-28's "no AWS SDK enters
// this repository" read the only way it can honestly be read.
//
// Registering rclone's s3 backend DOES pull aws-sdk-go-v2 into go.mod and
// go.sum as an indirect dependency, and no test can make that untrue. What
// would be a mistake is a SECOND S3 implementation written against the SDK
// directly, outside the FR-3 boundary and outside rclone's upgrade
// contract. That is what this refuses: a direct provider-SDK import from
// any Go file this repository owns, this package included.
//
// It is a source scan rather than a go.mod check for exactly that reason:
// go.mod cannot tell the two apart, and the one that matters is who writes
// the import line.
func TestNoCloudProviderSDKIsImportedDirectly(t *testing.T) {
	root := coreModuleRootForRclonePackage(t)
	repo := filepath.Dir(root)

	offenders, scanned, err := scanSDKImports(repo, sdkImportAllowed)
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	// A positive control, because a scan that visited nothing would report
	// no offenders and look exactly like a clean repository.
	if scanned < 200 {
		t.Fatalf("the scan parsed only %d Go files, which is far fewer than this repository has; "+
			"its verdict about SDK imports means nothing", scanned)
	}
	for _, offender := range offenders {
		t.Errorf("%s. rclone's s3 backend is the whole S3 implementation (FR-28); a direct SDK import is a second one, "+
			"outside the boundary that keeps upstream churn in one adapter", offender)
	}
}

var forbiddenSDKImports = []string{
	"github.com/aws/aws-sdk-go",
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/smithy-go",
	"cloud.google.com/go/storage",
	"github.com/Azure/azure-sdk-for-go",
	"github.com/minio/minio-go",
}

// sdkImportAllowed is empty until the fix lands.
var sdkImportAllowed = map[string]bool{}

// scanSDKImports walks repo and returns one line per forbidden import found
// in a file not named in allowed (keys are slash-separated paths relative to
// repo), plus the number of Go files it parsed.
func scanSDKImports(repo string, allowed map[string]bool) (offenders []string, scanned int, err error) {
	_ = allowed
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			// A file this repository cannot parse is not a file whose
			// imports have been checked, so it is reported rather than
			// skipped.
			offenders = append(offenders, path+": could not be parsed: "+perr.Error())
			return nil
		}
		scanned++
		for _, imported := range file.Imports {
			p, uerr := strconv.Unquote(imported.Path.Value)
			if uerr != nil {
				continue
			}
			for _, bad := range forbiddenSDKImports {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					rel, _ := filepath.Rel(repo, path)
					offenders = append(offenders, rel+" imports "+p)
				}
			}
		}
		return nil
	})
	return offenders, scanned, err
}

// TestSDKImportAllowListIsExact is the control for the allow-list: it names
// one file, so a sibling in the same directory importing the SDK is still
// refused, and an entry whose file no longer imports the SDK is itself a
// failure rather than a permission left lying around.
func TestSDKImportAllowListIsExact(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const imp = "package x\n\nimport _ \"github.com/aws/aws-sdk-go-v2/service/s3\"\n"
	write("core/tests/reals3/reals3.go", imp)
	write("core/tests/reals3/sibling.go", imp)
	write("core/internal/other/other.go", imp)

	allowed := map[string]bool{"core/tests/reals3/reals3.go": true}
	got, scanned, err := scanSDKImports(repo, allowed)
	if err != nil || scanned != 3 {
		t.Fatalf("scan = %v, %d files, %v", got, scanned, err)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "core/tests/reals3/reals3.go") {
		t.Errorf("the allow-listed file was reported: %s", joined)
	}
	for _, must := range []string{"core/tests/reals3/sibling.go", "core/internal/other/other.go"} {
		if !strings.Contains(joined, must) {
			t.Errorf("%s imports the SDK, is not allow-listed, and was not reported: %q", must, joined)
		}
	}
}

// TestSDKImportAllowListEntriesStillImportTheSDK keeps the real list minimal.
func TestSDKImportAllowListEntriesStillImportTheSDK(t *testing.T) {
	repo := filepath.Dir(coreModuleRootForRclonePackage(t))
	for rel := range sdkImportAllowed {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, filepath.FromSlash(rel)), nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("the allow-list names %s, which cannot be parsed: %v", rel, err)
			continue
		}
		found := false
		for _, imported := range file.Imports {
			p, _ := strconv.Unquote(imported.Path.Value)
			for _, bad := range forbiddenSDKImports {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the allow-list names %s, which no longer imports a provider SDK; drop it", rel)
		}
	}
}

// coreModuleRootForRclonePackage walks up to core/'s go.mod. It is this
// package's own copy of the walk internal/transport already has, because
// making that one exported would put a test helper on a production
// package's surface.
func coreModuleRootForRclonePackage(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("found no go.mod above %s", dir)
		}
		dir = parent
	}
}
