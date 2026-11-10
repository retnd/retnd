/**
 * The two chips of one copy's encryption state, and the one-off chips the
 * cards draw (issue #995; design section 1).
 *
 * Encrypted (or Plaintext) is one chip and the recovery question is a second,
 * never one badge for both: an operator who reads "encrypted" as "safe" has
 * been told something the backend has not proved. The words, glyphs and edges
 * are in encryption.css; the decision of WHICH chips a copy gets is
 * encryptionState.ts, a pure function of the copy's own rows.
 *
 * Every chip names a registry tooltip. The explanation is stated from what
 * the backend records, and a chip never claims more than that: no algorithm
 * name, no strength, no "securely".
 */
import "./encryption.css";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import { stamp } from "@shared/utilities/format";
import type { CopyEncryptionView, RecoveryChip } from "./encryptionState";

/** Text glyphs, never emoji: an emoji is drawn in its own colours, which would
 *  break "the ink is always --text". */
const GLYPH = {
  plaintext: "▢",
  encrypted: "▣",
  verified: "✓✓",
  "not-verified": "○",
  "key-missing": "⊘",
  "decrypt-failed": "✕",
  live: "▶",
  resolves: "✓",
  unreachable: "⊘",
  "not-declared": "−",
  "key-active": "⚿"
} as const;

const WORDS: Record<keyof typeof GLYPH, { label: string; tip: TooltipId }> = {
  plaintext: { label: "Plaintext", tip: "encryption.state.plaintext" },
  encrypted: { label: "Encrypted", tip: "encryption.state.encrypted" },
  verified: { label: "Verified recoverable", tip: "encryption.state.verifiedRecoverable" },
  "not-verified": { label: "Not decrypt-verified", tip: "encryption.state.notVerified" },
  "key-missing": { label: "Key missing", tip: "encryption.state.keyMissing" },
  "decrypt-failed": { label: "Decrypt failed", tip: "encryption.state.decryptFailed" },
  live: { label: "Decrypt-verifying", tip: "encryption.verify.run" },
  resolves: { label: "Resolves", tip: "encryption.external.test" },
  unreachable: { label: "Declared but unreachable", tip: "encryption.external.test" },
  "not-declared": { label: "Not declared", tip: "encryption.external.test" },
  "key-active": { label: "Managed key active", tip: "encryption.card" }
};

export type ChipState = keyof typeof GLYPH;

export function EncryptionChip({ state, age, detail }: { state: ChipState; age?: string; detail?: string }) {
  const w = WORDS[state];
  return (
    <InfoTooltip id={w.tip}>
      <span
        className="echip"
        data-echip=""
        data-state={state}
        role="group"
        aria-label={w.label + (age ? " " + age : "") + (detail ? ", " + detail : "")}
      >
        <span className="echip__glyph" aria-hidden="true">{GLYPH[state]}</span>
        <span>{w.label}</span>
        {age ? <span className="echip__age">{age}</span> : null}
      </span>
    </InfoTooltip>
  );
}

const RECOVERY_STATE: Record<Exclude<RecoveryChip, "none">, ChipState> = {
  verified: "verified",
  "not-verified": "not-verified",
  "key-missing": "key-missing",
  "decrypt-failed": "decrypt-failed"
};

/** One copy's chips: Plaintext alone, or Encrypted plus the recovery chip. */
export function CopyChips({ view, failure }: { view: CopyEncryptionView; failure?: string }) {
  if (!view.sealed) {
    return (
      <span className="echips" data-copy-chips="" data-sealed="false">
        <EncryptionChip state="plaintext" />
      </span>
    );
  }
  const second = view.recovery === "none" ? null : RECOVERY_STATE[view.recovery];
  return (
    <span className="echips" data-copy-chips="" data-sealed="true" data-recovery={view.recovery}>
      <EncryptionChip state="encrypted" detail={view.keyId ? "key " + view.keyId : undefined} />
      {second ? (
        <EncryptionChip state={second} age={view.recovery === "verified" && view.verifiedAt ? stamp(view.verifiedAt) : undefined} detail={failure} />
      ) : null}
      {failure ? <span className="echips__note">{failure}</span> : null}
    </span>
  );
}
