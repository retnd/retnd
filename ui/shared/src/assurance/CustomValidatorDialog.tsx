/**
 * Add or edit a custom recovery-test validator (issue #1203).
 *
 * # Nobody writes a regex by hand
 *
 * The patterns the catalog already implies are offered as choices (every run
 * of digits generalised, with how many existing backups each matches and the
 * validator already on it), and whatever ends up in the pattern field is
 * judged by the ENGINE before anything is saved: the preview route runs Go's
 * RE2, so a lookahead a browser would accept is refused here with the
 * engine's own reason, and a pattern that an existing validator at the same
 * scope already matches is refused before the upload, not after it.
 *
 * # An upload is the one act that plants code
 *
 * It takes what a recovery onto the source host takes: the destructive gate
 * (said before anything is pressed when it is shut) and the administrator's
 * password again, sent in a header with this one request and forgotten the
 * moment it has gone. On a connection that is not encrypted the operator is
 * asked first, before the password leaves the browser. An edit takes the same.
 * Removing one takes neither (Settings' card), because it only stops a script
 * from running.
 */
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import type {
  CustomValidator, CustomValidatorList, CustomValidatorPatterns, CustomValidatorPreview, StepUpOptions
} from "@shared/api/contracts";
import type { BackupSet } from "@shared/types/backup";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { gateRefusalNotice, isGateRefusal } from "@shared/api/gate";
import { GateNotice } from "@shared/components/GateNotice";
import { GateRefusal } from "@shared/components/GateRefusal";
import { StepUpPassword, stepUpMessage } from "@shared/components/StepUpFields";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { useCausl } from "@shared/state/graph";
import { setsNode } from "@shared/state/appNodes";
import {
  addCommand, canKeepStoredScript, kib, matchWords, readScriptFile, scriptBytes, scriptProblem, scriptStateWords, shortHash, suggestPattern, validatorNameProblem
} from "./customValidators";
import { defaultValidatorScript } from "./defaultValidatorScript";
import { useDialogFocus } from "@shared/hooks/useDialogFocus";

export type ValidatorDialogMode =
  | { kind: "add"; backupSetId?: string; artifact?: { id: string; filename: string } }
  | { kind: "edit"; validator: CustomValidator };

export interface ValidatorSaved {
  validator: CustomValidator;
  edited: boolean;
}

const small = { fontSize: "var(--text-sm)" } as const;
const quiet = { ...small, color: "var(--text-2)" } as const;
const bad = { ...small, color: "var(--danger)", fontWeight: 600 } as const;

export function CustomValidatorDialog({
  mode, list, onClose, onSaved, returnFocus
}: {
  mode: ValidatorDialogMode;
  list: CustomValidatorList;
  onClose(): void;
  onSaved(saved: ValidatorSaved): void;
  returnFocus?: RefObject<HTMLElement>;
}) {
  const api = useApi();
  const gate = useDestructiveGate();
  const box = useRef<HTMLDivElement>(null);
  const edit = mode.kind === "edit" ? mode.validator : null;
  const artifact = mode.kind === "add" ? mode.artifact : undefined;

  const [name, setName] = useState(edit?.name ?? "");
  const [setId, setSetId] = useState(edit ? edit.backupSetId : mode.kind === "add" ? mode.backupSetId ?? "" : "");
  // What the operator chose or typed; null until they do. Until then, for a
  // backup's own page, the engine's suggestion for that backup stands (and the
  // browser's own derivation only while the engine has not answered).
  const [typed, setTyped] = useState<string | null>(edit ? edit.pattern : null);
  const [script, setScript] = useState("");
  // The sets come from the shell's own node, not a fetch of this dialog's.
  const sets: BackupSet[] = useCausl(setsNode).data ?? [];
  // What the suggestion read last answered, and for which scope: a scope
  // that has not been answered yet shows as "reading", not as the last one's.
  const [read, setRead] = useState<{ scope: string; patterns: CustomValidatorPatterns | null } | null>(null);
  const [preview, setPreview] = useState<{ for: string; p: CustomValidatorPreview } | null>(null);
  const [previewFailed, setPreviewFailed] = useState<{ for: string; message: string } | null>(null);
  const [fileProblem, setFileProblem] = useState<string | null>(null);
  const [fileNote, setFileNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refusal, setRefusal] = useState<ReturnType<typeof gateRefusalNotice> | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [defaultFailed, setDefaultFailed] = useState<string | null>(null);

  // The password lives in a ref and the input is remounted (by key) to forget
  // it; it is never part of any state a render could show or a log could keep.
  const password = useRef("");
  const [hasPassword, setHasPassword] = useState(false);
  const [passwordKey, setPasswordKey] = useState(0);
  const forget = () => { password.current = ""; setHasPassword(false); setPasswordKey((k) => k + 1); };

  useDialogFocus(box, onClose, { returnFocus, busy: submitting });

  // The suggestions follow the scope: a set's own, or every set's.
  useEffect(() => {
    let live = true;
    api.listCustomValidatorPatterns(setId || undefined).then(
      (p) => { if (live) setRead({ scope: setId, patterns: p }); },
      () => { if (live) setRead({ scope: setId, patterns: null }); }
    );
    return () => { live = false; };
  }, [api, setId]);

  const patterns = read && read.scope === setId ? read.patterns : null;
  const patternsFailed = read !== null && read.scope === setId && read.patterns === null;
  const pattern =
    typed ??
    (artifact
      ? patterns?.patterns.find((p) => p.example === artifact.filename)?.pattern ?? suggestPattern(artifact.filename)
      : "");
  const setPattern = setTyped;
  const judgedKey = pattern + "\u0000" + setId;

  // What the pattern matches, asked of the engine a beat after the last key.
  // A newer pattern (or closing the dialog) aborts the request in flight, and
  // a failure is kept against the pattern it was for, so it goes away when the
  // pattern does.
  useEffect(() => {
    if (pattern === "") return;
    const ac = new AbortController();
    const timer = setTimeout(() => {
      api.previewCustomValidator({ pattern, backupSetId: setId || undefined, replacing: edit?.name }, { signal: ac.signal }).then(
        (p) => { if (!ac.signal.aborted) setPreview({ for: pattern + "\u0000" + setId, p }); },
        (e: unknown) => {
          if (!ac.signal.aborted) {
            setPreviewFailed({ for: pattern + "\u0000" + setId, message: describeFailure(e, "the engine could not judge this pattern").message });
          }
        }
      );
    }, 200);
    return () => { ac.abort(); clearTimeout(timer); };
  }, [api, pattern, setId, edit?.name]);

  const judged = preview && preview.for === judgedKey ? preview.p : null;
  const failedPreview = previewFailed && previewFailed.for === judgedKey ? previewFailed.message : null;

  const nameProblem = edit ? null : validatorNameProblem(name);
  // One encode of the script per change to it, however often this renders.
  const bytes = useMemo(() => scriptBytes(script), [script]);
  const scriptIssue = useMemo(() => scriptProblem(script, list.maxScriptBytes, bytes), [script, list.maxScriptBytes, bytes]);
  const shownScriptProblem = script === "" ? null : scriptIssue;
  const atLimit = !edit && list.validators.length >= list.maxValidators;
  const needsPassword = gate.stepUpRequired;
  // An edit may leave the script empty to keep the stored one, unless the
  // engine would refuse that (edited on disk, or the file is gone).
  const mayKeep = edit !== null && canKeepStoredScript(edit);

  const ready =
    !submitting && !atLimit && list.available &&
    nameProblem === null &&
    (mayKeep ? script === "" || scriptIssue === null : scriptIssue === null) &&
    pattern !== "" && judged !== null && judged.valid && judged.conflicts.length === 0 &&
    (!needsPassword || hasPassword);

  const setLabel = useCallback((s: BackupSet) => s.name + " (" + s.id + ")", []);

  const download = async () => {
    setDefaultFailed(null);
    try {
      const d = await defaultValidatorScript(api);
      const url = URL.createObjectURL(new Blob([d.content], { type: "text/x-shellscript" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = d.fileName;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setDefaultFailed(describeFailure(e, "could not read the default script").message);
    }
  };

  const startFromDefault = async () => {
    setDefaultFailed(null);
    try {
      setScript((await defaultValidatorScript(api)).content);
      setFileNote(null);
      setFileProblem(null);
    } catch (e) {
      setDefaultFailed(describeFailure(e, "could not read the default script").message);
    }
  };

  const save = async () => {
    if (!ready) return;
    setSubmitting(true);
    setError(null);
    setRefusal(null);
    // The question comes BEFORE the password leaves the browser.
    if (needsPassword && gate.insecureTransportAckRequired && !(await acknowledgeInsecureTransport())) {
      setError(stepUpMessage("INSECURE_TRANSPORT_ACK_REQUIRED", "validator") ?? "Nothing was sent.");
      setSubmitting(false);
      return;
    }
    const opts: StepUpOptions = needsPassword && password.current ? { stepUpPassword: password.current } : {};
    forget();
    try {
      const validator = edit
        ? await api.updateCustomValidator(
            edit.name,
            // A replacement: an empty script box keeps the stored script by the hash this list read.
            script === "" && mayKeep
              ? { pattern, backupSetId: setId || undefined, keepScriptSha256: edit.scriptSha256, env: edit.env }
              : { pattern, backupSetId: setId || undefined, script, env: edit.env },
            opts
          )
        : await api.createCustomValidator({ name, pattern, backupSetId: setId || undefined, script }, opts);
      onSaved({ validator, edited: edit !== null });
    } catch (e) {
      setSubmitting(false);
      const a = apiErrorOf(e);
      if (isGateRefusal(e)) {
        setRefusal(
          gateRefusalNotice({
            outcome: edit ? "The validator was not changed" : "The validator was not uploaded",
            nothingChanged: "No script was written and nothing was changed.",
            command: addCommand(name || edit?.name || "", pattern, setId),
            reason: gate.reason,
            correlationId: a?.correlationId
          })
        );
        return;
      }
      if ((a?.code as string | undefined) === "VALIDATOR_SCRIPT_CHANGED") {
        setError(
          "The script on the host changed after this list was read, or was edited on disk since it was last saved, so nothing was " +
          "saved and the stored script cannot be kept. Close this, let the list refresh, read the script as it is now and edit " +
          "again, sending the script to approve it."
        );
        return;
      }
      setError(stepUpMessage(a?.code, "validator") ?? refusalText(e));
    }
  };

  const onScriptTyped = useCallback((v: string) => { setScript(v); setFileNote(null); }, []);

  const chooseFile = async (f: File) => {
    const got = await readScriptFile(f, list.maxScriptBytes);
    if (got.ok) {
      setScript(got.text);
      setFileProblem(null);
      setFileNote(got.normalised ? "Line endings were converted to LF, so the script is the one shown here, not byte for byte the file." : null);
    } else {
      setFileProblem(got.problem);
      setFileNote(null);
    }
  };

  const title = edit ? "Edit validator" : "Add validator";
  const known = new Set(sets.map((s) => s.id));
  const extraSet = setId !== "" && !known.has(setId);

  return (
    <div className="dialog-scrim" style={{ gridTemplateColumns: "minmax(0, 1fr)" }} onClick={(e) => e.target === e.currentTarget && !submitting && onClose()}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="validator-title"
        className="dialog"
        data-assurance=""
        data-validator-dialog=""
        style={{ maxWidth: 640, minWidth: 0, maxHeight: "calc(100vh - 48px)", overflowY: "auto" }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="validator-title" style={{ margin: 0, fontSize: 18 }}>{title}</h2>
          {artifact ? (
            <div className="mono" data-validator-artifact="" style={{ ...quiet, marginTop: 4, wordBreak: "break-all" }}>
              Declared from backup {artifact.id}
            </div>
          ) : null}
        </div>

        <div style={{ padding: "14px 22px 18px", display: "flex", flexDirection: "column", gap: 14, fontSize: 13.5 }}>
          {!list.available ? (
            <div role="alert" style={bad}>{list.unavailableReason || "This deployment cannot keep custom validators."}</div>
          ) : null}
          {atLimit ? (
            <div role="alert" data-validator-limit="" style={bad}>
              This deployment already holds {list.maxValidators} validators, the most it keeps. Remove one before adding another.
            </div>
          ) : null}

          <div className="field">
            <label className="field__label" htmlFor="validator-name">Name</label>
            <input
              id="validator-name"
              className="input input--bold input--mono"
              value={name}
              readOnly={edit !== null}
              autoComplete="off"
              spellCheck={false}
              aria-describedby="validator-name-help"
              onChange={(e) => setName(e.target.value)}
            />
            <span id="validator-name-help" style={quiet}>
              The NAME in NAME.local.sh, kept in {list.directory || "the workflow root"}.
              {edit ? " A validator keeps its name; remove it and add another to rename it." : ""}
            </span>
          </div>
          {name !== "" && nameProblem ? <div role="alert" data-validator-field-problem="" style={bad}>{nameProblem}</div> : null}

          {edit ? <ScriptStateNote v={edit} /> : null}

          <div className="field">
            <label className="field__label" htmlFor="validator-scope">Applies to</label>
            <select id="validator-scope" className="select input input--bold" value={setId} aria-describedby="validator-scope-help" onChange={(e) => setSetId(e.target.value)}>
              <option value="">Every backup set</option>
              {extraSet ? <option value={setId}>{setId}</option> : null}
              {sets.map((s) => <option key={s.id} value={s.id}>{setLabel(s)}</option>)}
            </select>
            <span id="validator-scope-help" style={quiet}>
              A validator for one backup set wins over one for every set, so a set can opt out of a global pattern by declaring its own.
            </span>
          </div>

          <fieldset style={{ border: 0, margin: 0, padding: 0, minWidth: 0 }}>
            <legend className="field__label" style={{ padding: 0, marginBottom: 6 }}>Suggested patterns</legend>
            <div style={{ ...quiet, marginBottom: 6 }}>
              Derived from the backups already in the catalog: every run of digits generalised, the rest matched literally.
            </div>
            {patterns === null && !patternsFailed ? <div style={quiet}>Reading the backups...</div> : null}
            {patternsFailed ? <div role="alert" style={bad}>The suggestions could not be read. You can still write a pattern below.</div> : null}
            {patterns && patterns.patterns.length === 0 ? (
              <div style={quiet}>There are no backups here to derive a pattern from yet. Write one below.</div>
            ) : null}
            {patterns && patterns.patterns.length > 0 ? (
              <div style={{ display: "flex", flexDirection: "column", gap: 6, maxHeight: 190, overflowY: "auto" }}>
                {patterns.patterns.map((p) => (
                  <label
                    key={p.pattern}
                    style={{ display: "flex", gap: 8, alignItems: "flex-start", cursor: "pointer", ...small }}
                  >
                    <input
                      type="radio"
                      name="validator-pattern"
                      checked={pattern === p.pattern}
                      onChange={() => setPattern(p.pattern)}
                      style={{ marginTop: 3 }}
                    />
                    <span style={{ minWidth: 0, overflowWrap: "anywhere" }}>
                      <span className="mono" style={{ color: "var(--text)" }}>{p.pattern}</span>{" "}
                      <span style={{ color: "var(--text-2)" }}>
                        matches {matchWords(p.matchCount)}, e.g. {p.example}
                        {p.validator ? ", already checked by " + p.validator : ""}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
            ) : null}
          </fieldset>

          <div className="field">
            <label className="field__label" htmlFor="validator-pattern">Artifact-name pattern</label>
            <input
              id="validator-pattern"
              className="input input--bold input--mono"
              value={pattern}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              aria-describedby="validator-pattern-help"
              onChange={(e) => setPattern(e.target.value)}
            />
            <span id="validator-pattern-help" style={quiet}>
              A Go (RE2) regular expression matched against an artifact's file name, with no repeat count above 255. Anchor it with ^ and $.
            </span>
          </div>
          <PatternPreview pattern={pattern} judged={judged} failed={failedPreview} />

          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            <div className="field">
              <label className="field__label" htmlFor="validator-script">Script</label>
              <ScriptBox value={script} onChange={onScriptTyped} />
            </div>
            <span id="validator-script-help" style={quiet}>
              {edit && mayKeep
                ? "Leave the script empty to keep the one that is uploaded. "
                : edit
                ? "The stored script cannot be kept: send the script to approve it. "
                : "Type it, choose a file, or start from the documented default. "}
              It runs on the retnd host against a recovered copy, so read it as you would any script you put there.
            </span>
            <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
              <button type="button" className="btn btn--sm btn--strong-edge" onClick={() => void startFromDefault()}>
                Start from the default script
              </button>
              <button type="button" className="btn btn--sm btn--strong-edge" onClick={() => void download()}>
                Download default script
              </button>
              <label className="btn btn--sm btn--strong-edge" style={{ cursor: "pointer" }}>
                <span aria-hidden="true">Choose a file</span>
                <input
                  type="file"
                  aria-label="Script file"
                  accept=".sh,text/*"
                  className="visually-hidden"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    e.target.value = "";
                    if (f) void chooseFile(f);
                  }}
                />
              </label>
              <span data-script-size="" style={quiet}>
                {bytes} {bytes === 1 ? "byte" : "bytes"} of {kib(list.maxScriptBytes)}
              </span>
            </div>
            {defaultFailed ? <div role="alert" style={bad}>{defaultFailed}</div> : null}
            {fileProblem ? <div role="alert" data-validator-file-problem="" style={bad}>{fileProblem}</div> : null}
            {fileNote ? <div role="status" data-validator-file-note="" style={quiet}>{fileNote}</div> : null}
            {shownScriptProblem ? <div role="alert" data-validator-field-problem="" style={bad}>{shownScriptProblem}</div> : null}
          </div>

          <GateNotice
            gate={gate}
            headline="Uploading a validator is refused over HTTP on this deployment."
            command={addCommand(name || edit?.name || "", pattern, setId)}
          />

          {needsPassword ? (
            <div data-step-up="" style={{ maxWidth: 520 }}>
              <StepUpPassword
                key={passwordKey}
                act="validator"
                id="validator-step-up-help"
                onPassword={(next) => { password.current = next; setHasPassword(next !== ""); }}
              />
            </div>
          ) : null}

          {error !== null ? <div role="alert" data-validator-error="" style={bad}>{error}</div> : null}
          {refusal ? <GateRefusal notice={refusal} /> : null}
        </div>

        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          <button className="btn btn--strong-edge" disabled={submitting} onClick={onClose}>Cancel</button>
          <button className="btn btn--primary" disabled={!ready} onClick={() => void save()}>
            {edit ? "Save changes" : "Upload validator"}
          </button>
        </div>
      </div>
    </div>
  );
}

/** The state of the script being edited, in words (#1230). An edited-on-disk
 *  script is explained at length because the dialog changes with it: "keep the
 *  stored script" is gone, the engine refuses it (VALIDATOR_SCRIPT_CHANGED),
 *  and sending the script is how it is approved. */
function ScriptStateNote({ v }: { v: CustomValidator }) {
  const words = scriptStateWords(v.scriptState);
  return (
    <div
      data-validator-script-state=""
      data-script-state={v.scriptState}
      role={words.failing ? "alert" : "status"}
      style={{ display: "flex", flexDirection: "column", gap: 4, ...small, color: words.failing ? "var(--danger)" : "var(--text)" }}
    >
      <span style={{ fontWeight: words.failing ? 700 : 600 }}>Script: {words.label}</span>
      {v.scriptState === "pinned" ? (
        <span style={{ color: "var(--text-2)" }}>config.yaml pins sha256 {shortHash(v.pinnedSha256)}, and the script on the host has that hash.</span>
      ) : null}
      {v.scriptState === "unpinned" ? (
        <span style={{ color: "var(--text-2)" }}>
          It was written before pinning and runs as it did. Saving this validator, with or without a new script, pins the script at the
          hash it has now.
        </span>
      ) : null}
      {v.scriptState === "edited_on_disk" ? (
        <span>
          The script on the host no longer matches the hash config.yaml pins (pinned {shortHash(v.pinnedSha256)}, on disk{" "}
          {shortHash(v.scriptSha256)}), so a recovery test this validator applies to ends could not complete without running it. The
          stored script cannot be kept: the engine refuses that with VALIDATOR_SCRIPT_CHANGED, because it would approve code nobody has
          read. Sending the script is how you approve it. Read the one on the host first (Cancel, then View on the card), then paste it,
          choose a file or start from the default below.
        </span>
      ) : null}
      {v.scriptState === "missing" ? (
        <span>
          The script file {v.scriptFile} is not on the host, so a recovery test this validator applies to cannot complete and there is
          nothing to keep. Restore the file, or send the script below to upload it again.
        </span>
      ) : null}
    </div>
  );
}

/** The script's textarea on its own and memoised, so a keystroke in some other
 *  field does not make React compare a 256 KiB value again. */
const ScriptBox = memo(function ScriptBox({ value, onChange }: { value: string; onChange(v: string): void }) {
  return (
    <textarea
      id="validator-script"
      className="input input--bold input--mono"
      value={value}
      rows={9}
      spellCheck={false}
      autoCapitalize="none"
      aria-describedby="validator-script-help"
      style={{ height: "auto", padding: "8px 10px", resize: "vertical", whiteSpace: "pre" }}
      onChange={(e) => onChange(e.target.value)}
    />
  );
});

/** The engine's refusal in its own words, capitalised; the page's own sentence
 *  when it said nothing usable. */
function refusalText(e: unknown): string {
  const a = apiErrorOf(e);
  if (a && a.message) return a.message.charAt(0).toUpperCase() + a.message.slice(1) + (/[.!?]$/.test(a.message) ? "" : ".");
  return describeFailure(e, "The validator could not be saved.").message;
}

function PatternPreview({ pattern, judged, failed }: { pattern: string; judged: CustomValidatorPreview | null; failed: string | null }) {
  return (
    <div data-validator-preview="" aria-live="polite" style={{ display: "flex", flexDirection: "column", gap: 4, ...small }}>
      {pattern === "" ? (
        <span style={{ color: "var(--text-2)" }}>Choose a suggestion or write a pattern, and this says what it matches.</span>
      ) : failed ? (
        <span role="alert" style={bad}>{failed}</span>
      ) : judged === null ? (
        <span style={{ color: "var(--text-2)" }}>Asking the engine what this matches...</span>
      ) : !judged.valid ? (
        <span role="alert" style={bad}>Not a usable pattern: {judged.error}</span>
      ) : (
        <>
          <span style={{ color: "var(--text)" }}>
            Matches {matchWords(judged.matchCount)}
            {judged.matchSets.length > 0 ? " in " + judged.matchSets.join(", ") : ""}
            {judged.sample.length > 0 ? ", e.g. " + judged.sample.slice(0, 3).join(", ") : ""}.
          </span>
          {!judged.anchored ? (
            <span style={{ color: "var(--text-2)" }}>
              This pattern is not anchored with ^ and $, so it matches anywhere in a name.
            </span>
          ) : null}
          {judged.conflicts.map((c) => (
            <span key={c.validator} role="alert" style={bad}>
              {c.validator} already matches {c.example} at this scope, so two validators would match one backup. Narrow this pattern
              or change the scope.
            </span>
          ))}
        </>
      )}
    </div>
  );
}
