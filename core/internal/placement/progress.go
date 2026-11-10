package placement

import (
	"io"

	"github.com/retnd/retnd/core/internal/model"
)

// The two stages of an encrypted copy that carry byte progress (#1290).
//
// They are the stage names core/internal/app publishes in app.Progress and
// api/v1/openapi.json declares in OperationProgress.stage, spelled once here
// and re-used there, so the engine and the contract cannot drift apart. They
// are the progress face of the obs events copy_encrypting and
// copy_decrypt_verifying (#992): the events say once, durably, that a stage
// ended and how; these say, live, how far a running one has got.
const (
	StageEncrypting       = "encrypting"
	StageDecryptVerifying = "decrypt-verifying"
)

// defaultProgressStep is how many bytes pass between two readings of one
// stage when Engine.ProgressStep is not set. A reading per Read would be a
// reading per 32 KiB of a multi-gigabyte artifact.
const defaultProgressStep = 1 << 20

// CopyProgress is one reading of an encrypted copy's running stage.
//
// There is no key id, no digest and no path in it on purpose: it reaches the
// browser, and a reading that could name a key would be a second place a key
// identity leaks from.
type CopyProgress struct {
	// Stage is StageEncrypting or StageDecryptVerifying.
	Stage    string
	Artifact model.ArtifactID

	// Done and Total are bytes of the thing the stage reads: the artifact
	// for encrypting, the stored (encrypted) object for decrypt-verifying.
	// Total is 0 when it is not known, which a reader must not take for an
	// empty object. Done never goes backward within one stage and never
	// exceeds a known Total.
	Done  int64
	Total int64

	// Ended closes the stage. It is sent however the stage ended, success,
	// failure or cancellation, so a surface never keeps showing a stage
	// that is over.
	Ended bool
}

// meter turns the bytes read through it into CopyProgress readings for one
// stage. A nil *meter is the engine with no Progress callback: wrap returns
// its reader untouched and the rest are no-ops.
//
// Its two callers (the encrypting pass and the decrypt-verify) share the
// stepping and the bounding, which is the part that has to be right.
type meter struct {
	emit     func(CopyProgress)
	stage    string
	artifact model.ArtifactID
	total    int64
	step     int64
	done     int64
	last     int64
}

// newMeter opens a stage with a reading of a measured zero, or returns nil
// when nobody is listening.
func (e *Engine) newMeter(stage string, artifact model.ArtifactID, total int64) *meter {
	if e.Progress == nil {
		return nil
	}
	if total < 0 {
		total = 0
	}
	step := e.ProgressStep
	if step <= 0 {
		step = defaultProgressStep
	}
	m := &meter{emit: e.Progress, stage: stage, artifact: artifact, total: total, step: step}
	m.report()
	return m
}

func (m *meter) report() {
	done := m.done
	if m.total > 0 && done > m.total {
		done = m.total
	}
	m.last = done
	m.emit(CopyProgress{Stage: m.stage, Artifact: m.artifact, Done: done, Total: m.total})
}

// wrap counts what r yields.
func (m *meter) wrap(r io.Reader) io.Reader {
	if m == nil {
		return r
	}
	return &meteredReader{r: r, m: m}
}

// end sends the final reading when it differs from the last one, then closes
// the stage. It is deferred by both callers so that no exit path skips it.
func (m *meter) end() {
	if m == nil {
		return
	}
	if m.done != m.last && (m.total <= 0 || m.last < m.total) {
		m.report()
	}
	m.emit(CopyProgress{Stage: m.stage, Artifact: m.artifact, Done: m.last, Total: m.total, Ended: true})
}

type meteredReader struct {
	r io.Reader
	m *meter
}

func (mr *meteredReader) Read(p []byte) (int, error) {
	n, err := mr.r.Read(p)
	mr.m.done += int64(n)
	if mr.m.done-mr.m.last >= mr.m.step {
		mr.m.report()
	}
	return n, err
}
