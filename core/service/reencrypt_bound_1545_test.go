package service

import (
	"bytes"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// The dry run's destination figure is P + 16 * max(1, ceil(P / 65536)) + 16 +
// 4096: the payload, a tag per 64 KiB chunk, the nonce, and 4 KiB for the
// header. A suite with a bigger header would make that a lie, so every
// registered suite seals an empty plaintext here and its whole output (header,
// nonce and the one tag) has to fit the allowance. Red if a suite's header
// outgrows it.
func TestTheDryRunSizeBoundHoldsForEveryRegisteredSuite_1545(t *testing.T) {
	for _, s := range copycrypt.Suites() {
		t.Run(s.ID(), func(t *testing.T) {
			secret, recipient, err := s.Generate()
			if err != nil || len(secret) == 0 {
				t.Fatalf("Generate: %v", err)
			}
			var out bytes.Buffer
			w, err := s.Seal(&out, recipient)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			const allowance = 16 + 4096 // what the bound adds beyond the payload, with the one tag
			if out.Len() > allowance+16 {
				t.Fatalf("%s seals an empty plaintext into %d bytes, over the bound's %d", s.ID(), out.Len(), allowance+16)
			}
		})
	}
}
