/**
 * The external key reference: a path, a variable name or an executable, and
 * never material (issue #995; design section 7).
 *
 * One three-way choice and one single-line box whose label follows it. There
 * is no multi-line form, no paste-the-key variant, no file-upload control and
 * no "or enter the key directly" anywhere here. A value that is key material
 * rather than a reference is refused by the server at Load, and the caller
 * draws that refusal in the server's own words; this component does not try to
 * guess what material looks like.
 *
 * The box is never pre-filled from what the policy reads back. A reference you
 * can parse out of the UI is a second way to declare a key, which the type's
 * missing parse (and the API's write-only field) exist to prevent.
 *
 * Test reference has three answers (resolves, declared but unreachable, not
 * declared) and none of them renders a byte of what was resolved.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { HelpField } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { ExternalKeyReference, ProviderProbe } from "@shared/types/encryption";
import { EncryptionChip } from "./EncryptionChips";
import type { ChipState } from "./EncryptionChips";

export type ReferenceSource = "file" | "env" | "command";

export interface ReferenceDraft {
  source: ReferenceSource;
  text: string;
}

export const EMPTY_REFERENCE: ReferenceDraft = { source: "file", text: "" };

const LABEL: Record<ReferenceSource, string> = {
  file: "Path to the key file",
  env: "Variable name",
  command: "Executable"
};

const CHOICES: { source: ReferenceSource; label: string }[] = [
  { source: "file", label: "File" },
  { source: "env", label: "Environment variable" },
  { source: "command", label: "Command" }
];

/** The reference a draft stands for, or null while the box is empty. A command
 *  is split on spaces and run without a shell, as `--external-command` is. */
export function referenceOf(d: ReferenceDraft): ExternalKeyReference | null {
  const text = d.text.trim();
  if (text === "") return null;
  if (d.source === "file") return { file: text };
  if (d.source === "env") return { env: text };
  return { command: text.split(/\s+/) };
}

const PROBE_CHIP: Record<ProviderProbe["state"], ChipState> = {
  resolves: "resolves",
  unreachable: "unreachable",
  not_declared: "not-declared"
};

export function ExternalReferenceFields({
  draft, onChange, disabled = false, idPrefix
}: {
  draft: ReferenceDraft;
  onChange(next: ReferenceDraft): void;
  disabled?: boolean;
  idPrefix: string;
}) {
  const api = useApi();
  const [probe, setProbe] = useState<ProviderProbe | null>(null);
  const [probeFailure, setProbeFailure] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  const reference = referenceOf(draft);

  const test = () => {
    if (!reference) return;
    setTesting(true);
    setProbe(null);
    setProbeFailure(null);
    api.testEncryptionProvider({ external: reference }).then(
      (p) => { setProbe(p); setTesting(false); },
      (e: unknown) => { setProbeFailure(describeFailure(e, "the reference could not be tested").message); setTesting(false); }
    );
  };

  const change = (next: ReferenceDraft) => {
    // A result describes the reference that was tested, not the one typed since.
    setProbe(null);
    setProbeFailure(null);
    onChange(next);
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }} data-external-reference="">
      <InfoTooltip id="encryption.external.source" block>
        <div role="radiogroup" aria-label="Where the key reference points" className="echoice">
          {CHOICES.map((c) => (
            <label key={c.source}>
              <input
                type="radio"
                name={idPrefix + "-source"}
                checked={draft.source === c.source}
                disabled={disabled}
                onChange={() => change({ source: c.source, text: "" })}
              />
              {c.label}
            </label>
          ))}
        </div>
      </InfoTooltip>

      <HelpField label={LABEL[draft.source]} help={FIELD_HELP.encryptionExternalReference}>
        {(helpId) => (
          <input
            className="input input--mono"
            type="text"
            autoComplete="off"
            spellCheck={false}
            aria-describedby={helpId}
            value={draft.text}
            disabled={disabled}
            onChange={(e) => change({ ...draft, text: e.target.value })}
          />
        )}
      </HelpField>

      <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
        <InfoTooltip id="encryption.external.test">
          <button type="button" className="btn btn--sm" disabled={disabled || reference === null || testing} onClick={test}>
            Test reference
          </button>
        </InfoTooltip>
        {probe ? (
          <span className="echips" data-probe={probe.state}>
            <EncryptionChip state={PROBE_CHIP[probe.state]} />
            <span className="echips__note">{probe.state === "resolves" ? "" : probe.reason ?? ""}</span>
          </span>
        ) : null}
        {probeFailure ? <span role="alert" className="echips__note">{probeFailure}</span> : null}
      </div>
    </div>
  );
}
