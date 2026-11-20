/**
 * Declare a validator from a backup's own page (issue #1203), or from a backup
 * set's own page (issue #1520), where the dialog opens on the set and on no
 * particular backup. The dialog opens
 * on that backup's set and on the pattern its own name gives, so the common case
 * is a name and a script away. The row also says which validator already checks
 * this backup, so an operator is not left guessing why a new one is refused.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useOptionalApi } from "@shared/api/ApiContext";
import type { CustomValidatorList } from "@shared/api/contracts";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { addCommand } from "./validatorCommands";
import { CustomValidatorDialog } from "./CustomValidatorDialog";
import { scriptStateWords } from "./customValidators";
import { ValidatorEnvLabel } from "./ValidatorEnvLabel";

export function DeclareValidator({
  artifactId,
  setId: setScope,
  readOnly,
  embedded = false
}: {
  /** A backup's page: the dialog opens on this backup's set and its name's pattern. */
  artifactId?: string;
  /** A set's page: the dialog opens on this set and no backup. One of the two is given. */
  setId?: string;
  readOnly: boolean;
  /** Inside a card that already pads its body (the set page's Validation card). */
  embedded?: boolean;
}) {
  const api = useOptionalApi();
  const [list, setList] = useState<CustomValidatorList | null>(null);
  const [open, setOpen] = useState(false);
  const [said, setSaid] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const setId = setScope ?? (artifactId ?? "").split("/").slice(0, 2).join("/");
  const filename = (artifactId ?? "").split("/").slice(2).join("/");

  // Answers can arrive out of order (another backup, a save while a read is
  // out); only the newest question's answer is kept. An answer that arrives
  // after this has unmounted is dropped too: setting state then throws once
  // a test's window has been torn down (#1589).
  const asked = useRef(0);
  const mounted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  const load = useCallback(() => {
    // An engine older than #1203 has no such route; the row is then not drawn.
    if (!api || typeof api.listCustomValidators !== "function") return Promise.resolve();
    const mine = ++asked.current;
    return api.listCustomValidators(artifactId ? { artifactId } : { backupSetId: setId }).then(
      (l) => { if (mounted.current && mine === asked.current) setList(l); },
      () => { if (mounted.current && mine === asked.current) setList(null); }
    );
  }, [api, artifactId, setId]);

  useEffect(() => { void load(); }, [load]);

  if (!api || !list) return null;
  const r = list.resolution;
  // On a set's page: the validators whose scope reaches this set (its own, and the global ones).
  const covering = artifactId ? [] : list.validators.filter((v) => v.scope === "global" || v.backupSetId === setId);
  // The validator that would run, with the state its script is in (#1230).
  const runs = r && r.validator ? list.validators.find((v) => v.name === r.validator) : undefined;
  const words = runs ? scriptStateWords(runs.scriptState) : null;
  return (
    <div className={embedded ? undefined : "card__body"} data-validators="" style={{ display: "flex", gap: 16, alignItems: "center", flexWrap: "wrap", paddingTop: 0 }}>
      <div style={{ flex: 1, minWidth: 240, fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
        {!artifactId ? (
          <>
            <span data-validator-covered="">
              {covering.length > 0
                ? "Validators that check this set: " + covering.map((v) => v.name).join(", ") + "."
                : "No validator is declared for this set, so only the built-in structure check runs."}
            </span>
            <div data-validator-where="" style={{ color: "var(--text-2)" }}>
              {list.scriptsDir
                ? "Validator scripts are kept in " + list.scriptsDir + " (recovery-test/<source>/<set> under the workflow root)."
                : "Validator scripts are kept under recovery-test/<source>/<set> in the workflow root."}
            </div>
          </>
        ) : null}
        {r && r.validator ? (
          <span data-validator-covered="">A recovery test of this backup runs the validator {r.validator}.</span>
        ) : r && r.ambiguous.length > 0 ? (
          <span data-validator-covered="" role="alert" style={{ color: "var(--danger)", fontWeight: 600 }}>
            {r.ambiguous.join(" and ")} both match this name at one scope, so a recovery test of it ends could not complete.
          </span>
        ) : null}
        {runs && words ? (
          <div
            data-validator-script-state=""
            data-script-state={runs.scriptState}
            role={words.failing ? "alert" : undefined}
            style={words.failing ? { color: "var(--danger)", fontWeight: 600 } : { color: "var(--text)" }}
          >
            Script: {words.label}.{" "}
            {runs.scriptState === "edited_on_disk"
              ? "A recovery test of this backup ends could not complete until the script is approved: Edit " + runs.name +
                " on Settings and send the script again."
              : runs.scriptState === "missing"
              ? "A recovery test of this backup cannot complete. Restore " + runs.scriptFile +
                " on the host, or upload the script again from " + runs.name + " on Settings."
              : words.action}
          </div>
        ) : null}
        {runs ? (
          <div data-validator-env-line="" style={{ color: "var(--text)" }}>
            {runs.name} starts with the <ValidatorEnvLabel env={runs.env} />.
          </div>
        ) : null}
        <div role="status" data-validator-declared="" style={{ color: "var(--text)" }}>{said}</div>
        {!artifactId ? <CommandEcho label="Same thing from the terminal" commands={[addCommand("NAME", "PATTERN", setId)]} /> : null}
      </div>
      <InfoTooltip id="assurance.validators.declare">
        <button ref={opener} className="btn btn--strong-edge" disabled={readOnly || !list.available} onClick={() => setOpen(true)}>
          Declare validator
        </button>
      </InfoTooltip>
      {open ? (
        <CustomValidatorDialog
          mode={artifactId ? { kind: "add", backupSetId: setId, artifact: { id: artifactId, filename } } : { kind: "add", backupSetId: setId }}
          list={list}
          returnFocus={opener}
          onClose={() => setOpen(false)}
          onSaved={({ validator }) => {
            setOpen(false);
            setSaid(
              "Uploaded " + validator.name + ". It checks backups named like " + validator.pattern + " in " +
              (validator.backupSetId || "every backup set") + "."
            );
            void load();
          }}
        />
      ) : null}
    </div>
  );
}
