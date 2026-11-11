package config

import (
	"reflect"
	"strings"
	"testing"
)

// EPIC N's NC-05 (#989, closed on the release PR #1331): retained-copy
// encryption is one new top-level key, retained_copy_encryption, and
// key_encryption is neither extended nor moved. key_encryption protects
// secret files on disk; a field on it that reached retained copies would be
// a second door to the same decision, and nothing pinned its shape.
//
// Its planted violation, "key_encryption extended with a retained-copy
// field" in scripts/bdtools/encryption/selftest.py, adds one field to
// KeyEncryption, and this test is what goes red.
func TestKeyEncryptionIsNeitherExtendedNorMoved_1331(t *testing.T) {
	var fields []string
	typ := reflect.TypeFor[KeyEncryption]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		fields = append(fields, f.Name+" "+f.Type.String()+" "+f.Tag.Get("yaml"))
	}
	want := []string{"File string file", "Env string env", "Command []string command"}
	if strings.Join(fields, "; ") != strings.Join(want, "; ") {
		t.Errorf("KeyEncryption has fields [%s], want exactly [%s]; retained-copy encryption is its own key, retained_copy_encryption",
			strings.Join(fields, "; "), strings.Join(want, "; "))
	}

	tags := map[string]string{}
	cfg := reflect.TypeFor[Config]()
	for i := range cfg.NumField() {
		f := cfg.Field(i)
		tags[strings.Split(f.Tag.Get("yaml"), ",")[0]] = f.Name
	}
	if tags["key_encryption"] != "KeyEncryption" {
		t.Errorf("key_encryption is read into %q, want KeyEncryption where it always was", tags["key_encryption"])
	}
	if tags["retained_copy_encryption"] != "RetainedCopyEncryptionBlock" {
		t.Errorf("retained_copy_encryption is read into %q, want its own block, RetainedCopyEncryptionBlock", tags["retained_copy_encryption"])
	}
}
