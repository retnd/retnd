package recoverytest

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/fsopen"
)

// The built-in structure check (#1111).
//
// When a backup set declares no validator, the engine still recovers the
// newest known good copy and matches its digest, and then, instead of
// refusing, reads the recovered file end to end as the compression or archive
// format it is: gzip, zstd, tar, and a tar inside either. It verifies every
// integrity field the format carries and extracts nothing, discards what it
// decodes, execs nothing and never prints a member name.
//
// That proves the file decodes. It does not prove an application can use it,
// which is the operator validator's job, so a built-in pass is stored as a
// pass under a reserved identity and every surface words it differently from
// a validator's. The identity is reserved by prefix (config.RejectsReservedID)
// so an operator's script cannot be mistaken for it.

// BuiltinValidatorID is the reserved identity a built-in verdict is stored
// under. It uses only [a-z0-9-], so it passes the feed's validatorRef and the
// journal's printable check.
const BuiltinValidatorID = "retnd-builtin-structure"

// BuiltinDescriptor is what the identity hash is the hash of. The hash changes
// exactly when the check's meaning or its format list does.
const BuiltinDescriptor = "retnd-builtin-structure v1: gzip zstd tar tar+gzip tar+zstd"

// BuiltinValidatorHash is the descriptor hash stored beside the identity.
func BuiltinValidatorHash() string {
	h := sha256.Sum256([]byte(BuiltinDescriptor))
	return hex.EncodeToString(h[:])
}

// IsBuiltin says whether id is one of the product's own check identities. The
// rule itself (the retnd- prefix) lives in config so the loader can refuse an
// operator's validator_id that begins with it without importing this package.
func IsBuiltin(id string) bool { return config.IsReservedValidatorID(id) }

// The bounds, each a named constant a test drives over its line. Hitting one
// ends the run as could_not_complete and never as failed: a bound says the
// check ran out of room, not that the copy is bad.
const (
	// DefaultBuiltinCheckBudget is the wall clock a check may take.
	DefaultBuiltinCheckBudget = 2 * time.Hour
	// DefaultBuiltinMaxDecodedBytes is the most it will decode. There is no
	// decompression-ratio cap: sparse images legitimately compress 10,000:1.
	DefaultBuiltinMaxDecodedBytes int64 = 4 << 40
	// BuiltinZstdMaxWindow is the largest zstd window it will allocate.
	BuiltinZstdMaxWindow = 128 << 20
	// BuiltinReadBufferBytes is the read buffer decoded bytes pass through
	// on their way to being discarded.
	BuiltinReadBufferBytes = 4 << 20
)

// Outcomes of a built-in check.
const (
	BuiltinPass    = "pass"
	BuiltinFail    = "fail"
	BuiltinStopped = "stopped"
)

// ZstdDecoderConfig is what the zstd decoder is built with. It is a struct
// and not a list of options so a test can assert it through the seam
// (StructureCheck.NewZstd): the options themselves are opaque.
type ZstdDecoderConfig struct {
	Concurrency    int
	Lowmem         bool
	MaxWindow      uint64
	IgnoreChecksum bool
}

// BuiltinZstdConfig is the decoder configuration the check uses: one
// goroutine, low memory, a 128 MiB window ceiling, and the content checksum
// verified (never ignored).
func BuiltinZstdConfig() ZstdDecoderConfig {
	return ZstdDecoderConfig{Concurrency: 1, Lowmem: true, MaxWindow: BuiltinZstdMaxWindow, IgnoreChecksum: false}
}

func (c ZstdDecoderConfig) options() []zstd.DOption {
	o := []zstd.DOption{
		zstd.WithDecoderConcurrency(c.Concurrency),
		zstd.WithDecoderLowmem(c.Lowmem),
		zstd.WithDecoderMaxWindow(c.MaxWindow),
	}
	if c.IgnoreChecksum {
		o = append(o, zstd.IgnoreChecksum(true))
	}
	return o
}

func newZstdReader(r io.Reader, c ZstdDecoderConfig) (*zstd.Decoder, error) {
	return zstd.NewReader(r, c.options()...)
}

// BuiltinInput is the recovered file the check reads. Name is the artifact's
// name: it decides only what the check expects, never what it reads as.
type BuiltinInput struct {
	File string
	Name string
}

// BuiltinReport is what a check concluded and found. It holds counts and
// words the engine wrote and never anything an archive named.
type BuiltinReport struct {
	Outcome string

	// Recognised is what the bytes are ("zstd", "tar in gzip"), Layers the
	// same as a list (outermost first), Magic what told them apart.
	Recognised string
	Layers     []string
	Magic      string
	// NameSuffix is the part of the artifact's name that made the check try.
	NameSuffix string
	// Mismatch is one line when the name said one thing and the bytes another.
	Mismatch string

	// Members is the gzip members or zstd frames read, Entries the tar
	// entries (counted, never named), Decoded the bytes decoded and
	// discarded.
	Members int64
	Entries int64
	Decoded int64

	// ChecksumNote says which integrity fields were verified. Set on a pass.
	ChecksumNote string

	// Detail says why a check failed or stopped, in the engine's own words.
	Detail string
}

// BuiltinChecker is the seam Run consults.
type BuiltinChecker interface {
	// Applies says whether the check has a reader for a file of this name,
	// and which suffix made it say so. It reads nothing.
	Applies(name string) (suffix string, ok bool)
	// Check reads the recovered file end to end.
	Check(ctx context.Context, in BuiltinInput) BuiltinReport
}

// nameKinds are the suffixes the check will try, longest first, and what each
// promises about the bytes.
var nameKinds = []struct {
	suffix string
	outer  string // gzip, zstd or tar
	tar    bool   // the decoded stream is expected to be a tar
}{
	{".tar.gz", "gzip", true},
	{".tar.zst", "zstd", true},
	{".tgz", "gzip", true},
	{".tzst", "zstd", true},
	{".gz", "gzip", false},
	{".zst", "zstd", false},
	{".tar", "tar", true},
}

// StructureCheck is the product's checker. The zero value is the default:
// every bound at its named default and the real zstd decoder.
type StructureCheck struct {
	// Budget and MaxDecoded override the defaults when positive.
	Budget     time.Duration
	MaxDecoded int64
	// NewZstd builds the zstd decoder. Nil is the real one.
	NewZstd func(r io.Reader, c ZstdDecoderConfig) (*zstd.Decoder, error)
}

func (c StructureCheck) budget() time.Duration {
	if c.Budget > 0 {
		return c.Budget
	}
	return DefaultBuiltinCheckBudget
}

func (c StructureCheck) maxDecoded() int64 {
	if c.MaxDecoded > 0 {
		return c.MaxDecoded
	}
	return DefaultBuiltinMaxDecodedBytes
}

// Applies is by file name only.
func (StructureCheck) Applies(name string) (string, bool) {
	k, ok := kindOf(name)
	return k.suffix, ok
}

func kindOf(name string) (k struct {
	suffix string
	outer  string
	tar    bool
}, ok bool) {
	l := strings.ToLower(name)
	for _, n := range nameKinds {
		if strings.HasSuffix(l, n.suffix) && len(l) > len(n.suffix) {
			return n, true
		}
	}
	return k, false
}

var (
	errCeiling = errors.New("decoded-bytes ceiling reached")
	errNotTar  = errors.New("not a tar")
)

// posReader stops a read once the context is done and counts what it passed.
type posReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
}

func (c *posReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// capReader is the decoded-bytes ceiling. It lets exactly max bytes through
// and, on the next byte, ends the read with errCeiling and reports max.
type capReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
	max int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if room := c.max - c.n + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		c.n = c.max
		return 0, errCeiling
	}
	return n, err
}

// memberReader presents a gzip stream of any number of members as one decoded
// stream. It resets the reader at each member boundary, which is what lets it
// count members and insist the stream ends exactly on one: Reset at a clean
// end of input is io.EOF, and at anything else (a partial header, garbage) an
// error.
type memberReader struct {
	zr      *gzip.Reader
	src     *bufio.Reader
	members int64
}

func (m *memberReader) Read(p []byte) (int, error) {
	for {
		n, err := m.zr.Read(p)
		if err != io.EOF {
			return n, err
		}
		m.members++
		if rerr := m.zr.Reset(m.src); rerr != nil {
			return n, rerr // io.EOF here is the clean end of the stream
		}
		m.zr.Multistream(false)
		if n > 0 {
			return n, nil
		}
	}
}

// frameTap watches the compressed bytes pass to the zstd decoder and keeps
// count of the frames, because the decoder does not say how many it read. It
// parses frame framing only (headers and block sizes), so a stream it cannot
// follow just stops being counted and the decoder's own verdict stands.
type frameTap struct {
	r       io.Reader
	frames  int64
	withSum int64
	lost    bool

	state    int
	acc      [8]byte
	have     int
	skip     int64
	skipKind int
	hasSum   bool
	lastBlk  bool
}

const (
	tapMagic = iota
	tapSkipSize
	tapSkip
	tapFHD
	tapHdr
	tapBlockHdr
	tapSum

	skipSkippable = iota
	skipBlock
)

func (t *frameTap) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if !t.lost {
		t.feed(p[:n])
	}
	return n, err
}

// take accumulates count bytes into acc and says when it has them all.
func (t *frameTap) take(count int, b *[]byte) bool {
	for t.have < count && len(*b) > 0 {
		t.acc[t.have] = (*b)[0]
		*b = (*b)[1:]
		t.have++
	}
	if t.have < count {
		return false
	}
	t.have = 0
	return true
}

func (t *frameTap) frameDone() {
	t.frames++
	if t.hasSum {
		t.withSum++
	}
	t.state = tapMagic
}

func (t *frameTap) startSkip(n int64, kind int) {
	t.skip, t.skipKind, t.state = n, kind, tapSkip
	if n == 0 {
		t.afterSkip()
	}
}

func (t *frameTap) afterSkip() {
	if t.skipKind == skipSkippable {
		t.state = tapMagic
		return
	}
	switch {
	case !t.lastBlk:
		t.state = tapBlockHdr
	case t.hasSum:
		t.state = tapSum
	default:
		t.frameDone()
	}
}

func (t *frameTap) feed(b []byte) {
	for len(b) > 0 && !t.lost {
		switch t.state {
		case tapMagic:
			if !t.take(4, &b) {
				return
			}
			m := binary.LittleEndian.Uint32(t.acc[:4])
			switch {
			case m == 0xFD2FB528:
				t.state = tapFHD
			case m&0xFFFFFFF0 == 0x184D2A50:
				t.state = tapSkipSize
			default:
				t.lost = true
			}
		case tapSkipSize:
			if !t.take(4, &b) {
				return
			}
			t.startSkip(int64(binary.LittleEndian.Uint32(t.acc[:4])), skipSkippable)
		case tapSkip:
			if int64(len(b)) < t.skip {
				t.skip -= int64(len(b))
				return
			}
			b = b[t.skip:]
			t.skip = 0
			t.afterSkip()
		case tapFHD:
			if !t.take(1, &b) {
				return
			}
			fhd := t.acc[0]
			single := fhd&0x20 != 0
			t.hasSum = fhd&0x04 != 0
			rest := [...]int{0, 1, 2, 4}[fhd&3]
			if !single {
				rest++ // the window descriptor
			}
			switch fhd >> 6 {
			case 0:
				if single {
					rest++
				}
			case 1:
				rest += 2
			case 2:
				rest += 4
			case 3:
				rest += 8
			}
			t.skip, t.state = int64(rest), tapHdr
			if rest == 0 {
				t.state = tapBlockHdr
			}
		case tapHdr:
			if int64(len(b)) < t.skip {
				t.skip -= int64(len(b))
				return
			}
			b = b[t.skip:]
			t.skip, t.state = 0, tapBlockHdr
		case tapBlockHdr:
			if !t.take(3, &b) {
				return
			}
			h := uint32(t.acc[0]) | uint32(t.acc[1])<<8 | uint32(t.acc[2])<<16
			t.lastBlk = h&1 != 0
			size := int64(h >> 3)
			switch (h >> 1) & 3 {
			case 0, 2:
			case 1:
				size = 1
			default:
				t.lost = true
				return
			}
			t.startSkip(size, skipBlock)
		case tapSum:
			if !t.take(4, &b) {
				return
			}
			t.frameDone()
		}
	}
}

// Check reads the recovered file end to end. It never returns an error: every
// way it can end is one of the three outcomes, and a panic in a decoder is
// the third (stopped), logged by the caller from the detail, so a bad stream
// cannot take the scheduler down with it.
func (c StructureCheck) Check(ctx context.Context, in BuiltinInput) (rep BuiltinReport) {
	k, _ := kindOf(in.Name)
	rep.NameSuffix = k.suffix
	var st checkState
	defer func() {
		if r := recover(); r != nil {
			rep.Outcome = BuiltinStopped
			rep.Detail = "a decoder panicked (" + SanitizeLabel(fmt.Sprint(r), 120) + "), so nothing is known about the rest of the stream"
			st.fill(&rep)
		}
	}()
	rep = c.check(ctx, in, &st)
	return rep
}

// checkState is what a check has seen so far, shared with the panic path.
type checkState struct {
	layers  []string
	magic   string
	members int64
	entries int64
	decoded int64
	note    string
}

func (s *checkState) fill(r *BuiltinReport) {
	r.Layers = append([]string(nil), s.layers...)
	r.Magic = s.magic
	r.Members, r.Entries, r.Decoded = s.members, s.entries, s.decoded
	switch len(s.layers) {
	case 0:
	case 1:
		r.Recognised = s.layers[0]
	default:
		r.Recognised = s.layers[len(s.layers)-1] + " in " + s.layers[0]
	}
}

func (c StructureCheck) check(parent context.Context, in BuiltinInput, st *checkState) BuiltinReport {
	k, _ := kindOf(in.Name)
	ctx, cancel := context.WithTimeout(parent, c.budget())
	defer cancel()

	f, err := fsopen.OpenRegular(in.File)
	if err != nil {
		return BuiltinReport{Outcome: BuiltinStopped, NameSuffix: k.suffix, Detail: "the recovered copy could not be opened: " + SanitizeLabel(err.Error(), 160)}
	}
	defer f.Close()
	var size int64
	if fi, serr := f.Stat(); serr == nil {
		size = fi.Size()
	}
	file := &posReader{ctx: ctx, r: f}
	base := bufio.NewReaderSize(file, BuiltinReadBufferBytes)

	finish := func(rep BuiltinReport) BuiltinReport {
		rep.NameSuffix = k.suffix
		st.fill(&rep)
		if rep.Outcome == BuiltinPass {
			rep.ChecksumNote = st.note
		}
		return rep
	}
	// where says how far into the compressed file the read got.
	where := func() string {
		if size > 0 {
			return fmt.Sprintf(" after %s of %s", humanBytes(file.n), humanBytes(size))
		}
		return ""
	}
	stopOrFail := func(err error) BuiltinReport {
		switch {
		case parent.Err() != nil:
			return finish(BuiltinReport{Outcome: BuiltinStopped, Detail: "the check was cancelled" + where()})
		case ctx.Err() != nil:
			return finish(BuiltinReport{Outcome: BuiltinStopped, Detail: fmt.Sprintf("stopped at its %s budget after reading %s of the decompressed stream, all of it well-formed so far; nothing is known about the rest", wholeDuration(c.budget()), humanBytes(st.decoded))})
		case errors.Is(err, errCeiling):
			return finish(BuiltinReport{Outcome: BuiltinStopped, Detail: fmt.Sprintf("stopped at its decoded-bytes ceiling of %s, all of it well-formed so far; nothing is known about the rest", humanBytes(c.maxDecoded()))})
		case errors.Is(err, zstd.ErrWindowSizeExceeded):
			return finish(BuiltinReport{Outcome: BuiltinStopped, Detail: fmt.Sprintf("the zstd stream asks for a window above the check's %s ceiling, so it was not read", humanBytes(BuiltinZstdMaxWindow))})
		case errors.Is(err, zstd.ErrUnknownDictionary):
			return finish(BuiltinReport{Outcome: BuiltinStopped, Detail: "the zstd stream needs a dictionary, which the check does not have, so it was not read"})
		}
		msg := "unexpected EOF"
		if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			msg = SanitizeLabel(err.Error(), 160)
		}
		return finish(BuiltinReport{Outcome: BuiltinFail, Detail: msg + where()})
	}

	head, perr := base.Peek(512)
	if len(head) == 0 {
		if perr != nil && perr != io.EOF {
			return stopOrFail(perr)
		}
		return finish(BuiltinReport{Outcome: BuiltinFail, Detail: "the file is empty, and " + nameSays(k.outer, k.suffix)})
	}

	outer := detect(head)
	if outer == "" {
		return finish(BuiltinReport{Outcome: BuiltinFail, Detail: "the bytes are not gzip, zstd or tar, and " + nameSays(k.outer, k.suffix)})
	}
	st.layers = []string{outer}
	st.magic = magicOf(outer, head)
	rep := BuiltinReport{}
	if k.outer != "" && k.outer != outer {
		rep.Mismatch = fmt.Sprintf("the name says %s, the bytes are %s", k.outer, outer)
	}

	// The decoded stream: the outermost decoder's output, capped.
	var decoded io.Reader
	var closeDecoder func()
	var tap *frameTap
	var mr *memberReader
	switch outer {
	case "gzip":
		zr, err := gzip.NewReader(base)
		if err != nil {
			return mergeMismatch(stopOrFail(err), rep.Mismatch)
		}
		zr.Multistream(false)
		mr = &memberReader{zr: zr, src: base}
		decoded = mr
		closeDecoder = func() { zr.Close() }
		st.note = "CRC-32 and length verified in every member"
	case "zstd":
		tap = &frameTap{r: base}
		newZ := c.NewZstd
		if newZ == nil {
			newZ = newZstdReader
		}
		d, err := newZ(tap, BuiltinZstdConfig())
		if err != nil {
			return mergeMismatch(stopOrFail(err), rep.Mismatch)
		}
		decoded = d
		closeDecoder = d.Close
	case "tar":
		decoded = base
	}
	if closeDecoder != nil {
		defer closeDecoder()
	}
	capped := &capReader{ctx: ctx, r: decoded, max: c.maxDecoded()}
	sync := func() {
		st.decoded = capped.n
		if mr != nil {
			st.members = mr.members
		}
		if tap != nil {
			st.members = tap.frames
		}
	}
	defer sync()

	var stream io.Reader = capped
	isTar := outer == "tar"
	var br *bufio.Reader
	if !isTar {
		br = bufio.NewReaderSize(capped, 64<<10)
		inner, perr := br.Peek(512)
		if perr != nil && perr != io.EOF && len(inner) == 0 {
			sync()
			return mergeMismatch(stopOrFail(perr), rep.Mismatch)
		}
		isTar = len(inner) >= 262 && string(inner[257:262]) == "ustar"
		stream = br
		if !isTar && k.tar {
			rep.Mismatch = joinNote(rep.Mismatch, "the name says a tar archive, the decoded stream is not one")
		}
	}
	if isTar {
		if outer != "tar" {
			st.layers = append(st.layers, "tar")
		}
		tr := tar.NewReader(stream)
		for {
			_, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				sync()
				return mergeMismatch(stopOrFail(err), rep.Mismatch)
			}
			st.entries++
		}
		if outer == "tar" {
			st.note = "tar header checksums verified"
		}
	}
	// Whatever follows the tar's end blocks, or the whole stream when it is
	// not a tar, is read to the end too: a trailing CRC or content checksum
	// is only verified once the decoder reaches it.
	buf := make([]byte, BuiltinReadBufferBytes)
	for {
		_, err := stream.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			sync()
			return mergeMismatch(stopOrFail(err), rep.Mismatch)
		}
	}
	sync()
	if tap != nil {
		switch {
		case tap.frames > 0 && tap.withSum == tap.frames:
			st.note = "content checksum present and verified"
		case tap.withSum == 0:
			st.note = "no content checksum in the stream, so only its framing and blocks were verified"
		default:
			st.note = fmt.Sprintf("content checksum present in %d of %d frames and verified where present", tap.withSum, tap.frames)
		}
	}
	rep.Outcome = BuiltinPass
	return mergeMismatch(finish(rep), rep.Mismatch)
}

func mergeMismatch(r BuiltinReport, m string) BuiltinReport {
	r.Mismatch = joinNote(r.Mismatch, m)
	return r
}

func joinNote(a, b string) string {
	switch {
	case a == "" || a == b:
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// detect names the outer format by magic bytes. The file name only decided
// whether to try.
func detect(head []byte) string {
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return "gzip"
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "zstd"
	case len(head) >= 4 && head[0]&0xf0 == 0x50 && head[1] == 0x2a && head[2] == 0x4d && head[3] == 0x18:
		return "zstd" // starts with a skippable frame
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return "tar"
	}
	return ""
}

func magicOf(outer string, head []byte) string {
	switch outer {
	case "gzip":
		return "1f 8b"
	case "zstd":
		return fmt.Sprintf("% x", head[:4])
	}
	return "ustar at offset 257"
}

func nameSays(outer, suffix string) string {
	if suffix == "" {
		return "the name does not say"
	}
	return "the name ends " + suffix
}

// Report text helpers. Everything here is counts and words the engine wrote.

func plural(n int64, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%s %s", commas(n), many)
}

func commas(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func humanBytes(n int64) string {
	if n < 1024 {
		return plural(n, "byte", "bytes")
	}
	f, unit := float64(n), ""
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		f /= 1024
		unit = u
		if f < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", f, unit)
}

func wholeDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// Words is the one-line account of what the check read, e.g. "zstd, 1 frame,
// content checksum verified, 48.2 GiB decoded and discarded".
func (r BuiltinReport) Words() string {
	parts := []string{r.Recognised}
	if r.Members > 0 {
		unit := "member"
		if len(r.Layers) > 0 && r.Layers[0] == "zstd" {
			unit = "frame"
		}
		parts = append(parts, plural(r.Members, unit, unit+"s"))
	}
	if r.Entries > 0 {
		parts = append(parts, plural(r.Entries, "tar entry", "tar entries"))
	}
	switch {
	case strings.HasPrefix(r.ChecksumNote, "content checksum present and"):
		parts = append(parts, "content checksum verified")
	case strings.HasPrefix(r.ChecksumNote, "no content checksum"):
		parts = append(parts, "no content checksum")
	case strings.HasPrefix(r.ChecksumNote, "CRC-32"):
		parts = append(parts, "CRC-32 and length verified")
	case strings.HasPrefix(r.ChecksumNote, "tar header"):
		parts = append(parts, "tar header checksums verified")
	case r.ChecksumNote != "":
		parts = append(parts, "content checksum verified where present")
	}
	parts = append(parts, humanBytes(r.Decoded)+" decoded and discarded")
	return strings.Join(parts, ", ")
}

// Kind is what the bytes were read as, for a sentence about them.
func (r BuiltinReport) Kind() string {
	switch strings.Join(r.Layers, "+") {
	case "gzip":
		return "gzip stream"
	case "zstd":
		return "zstd stream"
	case "tar":
		return "tar archive"
	case "gzip+tar":
		return "gzip-compressed tar archive"
	case "zstd+tar":
		return "zstd-compressed tar archive"
	}
	return "gzip, zstd or tar file"
}

// Records are the four lines stored as the run's output.
func (r BuiltinReport) Records() []OutputRecord {
	lines := []string{"built-in structure check v1; no validator is declared for this backup set"}
	if len(r.Layers) > 0 {
		l := fmt.Sprintf("recognised: %s (magic %s; the name ends %s)", r.Recognised, r.Magic, r.NameSuffix)
		if r.Mismatch != "" {
			l += "; " + r.Mismatch
		}
		lines = append(lines, l)
	} else {
		lines = append(lines, "recognised: nothing ("+r.Detail+")")
	}
	switch r.Outcome {
	case BuiltinPass:
		note := r.ChecksumNote
		unit := ""
		if r.Members > 0 {
			u := "member"
			if r.Layers[0] == "zstd" {
				u = "frame"
			}
			unit = plural(r.Members, u, u+"s") + ", "
		}
		if r.Entries > 0 {
			unit += plural(r.Entries, "tar entry", "tar entries") + ", "
		}
		lines = append(lines, fmt.Sprintf("read %s%s decoded and discarded, %s", unit, plural(r.Decoded, "byte", "bytes"), note))
	case BuiltinFail:
		lines = append(lines, "did not read to the end: "+r.Detail)
	default:
		lines = append(lines, "stopped before the end: "+r.Detail)
	}
	lines = append(lines, "this proves the file decodes end to end; it does not prove an application can use it")
	out := make([]OutputRecord, len(lines))
	for i, l := range lines {
		out[i] = OutputRecord{Kind: OutputText, Text: sanitizeText(l)}
	}
	return out
}
