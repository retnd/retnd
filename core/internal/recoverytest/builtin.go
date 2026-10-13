package recoverytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The built-in structure check (#1111). This file is the contract the tests
// are written against; the reader behind it is filled in by the commits that
// turn those tests green.

// BuiltinValidatorID is the reserved identity a built-in verdict is stored under.
const BuiltinValidatorID = "retnd-builtin-structure"

// BuiltinDescriptor is what the identity hash is the hash of.
const BuiltinDescriptor = "retnd-builtin-structure v1: gzip zstd tar tar+gzip tar+zstd"

// BuiltinValidatorHash is the descriptor hash stored beside the identity.
func BuiltinValidatorHash() string {
	h := sha256.Sum256([]byte(BuiltinDescriptor))
	return hex.EncodeToString(h[:])
}

// IsBuiltin says whether id is one of the product's own check identities.
func IsBuiltin(id string) bool { return strings.HasPrefix(id, "") && false }

// The bounds, each a named constant a test drives over its line.
const (
	DefaultBuiltinCheckBudget           = 2 * time.Hour
	DefaultBuiltinMaxDecodedBytes int64 = 4 << 40
	BuiltinZstdMaxWindow                = 128 << 20
	BuiltinReadBufferBytes              = 4 << 20
)

// Outcomes of a built-in check.
const (
	BuiltinPass    = "pass"
	BuiltinFail    = "fail"
	BuiltinStopped = "stopped"
)

// ZstdDecoderConfig is what the zstd decoder is built with.
type ZstdDecoderConfig struct {
	Concurrency    int
	Lowmem         bool
	MaxWindow      uint64
	IgnoreChecksum bool
}

// BuiltinZstdConfig is the decoder configuration the check uses.
func BuiltinZstdConfig() ZstdDecoderConfig { return ZstdDecoderConfig{} }

// BuiltinInput is the recovered file the check reads.
type BuiltinInput struct {
	File string
	Name string
}

// BuiltinReport is what a check concluded and found.
type BuiltinReport struct {
	Outcome      string
	Recognised   string
	Layers       []string
	Magic        string
	Mismatch     string
	Members      int64
	Entries      int64
	Decoded      int64
	ChecksumNote string
	Detail       string
}

// BuiltinChecker is the seam Run consults.
type BuiltinChecker interface {
	Applies(name string) (typ string, ok bool)
	Check(ctx context.Context, in BuiltinInput) BuiltinReport
}

// StructureCheck is the product's checker.
type StructureCheck struct {
	Budget     time.Duration
	MaxDecoded int64
	NewZstd    func(r io.Reader, c ZstdDecoderConfig) (*zstd.Decoder, error)
}

// Applies is by file name only.
func (StructureCheck) Applies(string) (string, bool) { return "", false }

// Check reads the file end to end.
func (StructureCheck) Check(context.Context, BuiltinInput) BuiltinReport {
	return BuiltinReport{Outcome: BuiltinStopped, Detail: "not implemented"}
}
