/**
 * The Recovery-test validators card on Settings (issue #1203): where custom
 * validators are managed. Each is an operator's NAME.local.sh attached to an
 * artifact-name pattern, scoped to one backup set or to every set.
 *
 * Declaring one from a backup's own page (assurance/DeclareValidator.tsx) opens
 * the same dialog; this card is where they are listed, edited, read back and
 * removed. Removing is deliberately lighter than uploading: it only stops a
 * script from running, so it asks for a confirmation and nothing more, and a
 * shut destructive gate does not refuse it.
 */
import "./assurance.css";
import { useCallback, useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { CustomValidator, CustomValidatorList, ValidatorScript } from "@shared/api/contracts";
import { apiErrorOf, describeFailure, isNotConfigured } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CustomValidatorDialog } from "./CustomValidatorDialog";
import type { ValidatorDialogMode } from "./CustomValidatorDialog";
import { lastResultWords, matchWords } from "./customValidators";
import { ScriptStateCell } from "./ScriptStateCell";
import { ValidatorEnvLabel } from "./ValidatorEnvLabel";
import { ValidatorScriptDialog } from "./ValidatorScriptDialog";

export function CustomValidatorsCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const [list, setList] = useState<CustomValidatorList | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const [dialog, setDialog] = useState<ValidatorDialogMode | null>(null);
  const [removing, setRemoving] = useState<CustomValidator | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [script, setScript] = useState<{ name: string; script: ValidatorScript } | null>(null);
  const addButton = useRef<HTMLButtonElement>(null);

  // Only the newest read's answer is kept: a read that was asked first can
  // be answered last, and must not put an older list back.
  const asked = useRef(0);
  const load = useCallback(() => {
    const mine = ++asked.current;
    return api.listCustomValidators().then(
      (l) => { if (mine === asked.current) { setList(l); setError(null); } },
      (e: unknown) => {
        if (mine !== asked.current) return;
        if (isNotConfigured(apiErrorOf(e))) setNotConfigured(true);
        else setError(describeFailure(e, "could not read the custom validators").message);
      }
    );
  }, [api]);

  useEffect(() => { void load(); }, [load]);

  const viewScript = async (name: string) => {
    setError(null);
    try {
      setScript({ name, script: await api.getCustomValidatorScript(name) });
    } catch (e) {
      setError(describeFailure(e, "could not read the script").message);
    }
  };

  const remove = async () => {
    if (!removing) return;
    setRemoveBusy(true);
    try {
      await api.removeCustomValidator(removing.name);
      setStatus("Removed " + removing.name + ". Nothing runs it any more, and its script was moved aside, not deleted.");
      setRemoving(null);
      await load();
    } catch (e) {
      setRemoving(null);
      setError(describeFailure(e, "could not remove the validator").message);
    } finally {
      setRemoveBusy(false);
    }
  };

  // A directory the engine refuses runs nothing, so there is nothing to add to or edit.
  const refused = list?.directoryRefused ?? "";
  const disabled = readOnly || !list || !list.available || refused !== "";

  return (
    <section className="card" tabIndex={-1} data-assurance="" data-validators="" aria-label="Recovery-test validators">
      <div className="card__header" style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
        <InfoTooltip id="assurance.validators.card"><h2 className="eyebrow">Recovery-test validators</h2></InfoTooltip>
        <button ref={addButton} className="btn btn--sm btn--strong-edge" disabled={disabled} onClick={() => setDialog({ kind: "add" })}>
          Add validator
        </button>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <p style={{ margin: 0 }}>
          A recovery test restores a copy and hands it to a script that says whether it is good. A custom validator is such a
          script attached to a <em>kind</em> of artifact, by a pattern on its file name, so a set that mixes a manifest and a
          database dump can check each one properly.
        </p>
        <div data-validator-order="" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
          For the copy a test is about to check, the first of these that applies runs:
          <ol style={{ margin: "4px 0 4px", paddingLeft: 20 }}>
            <li>a validator scoped to the backup set whose pattern matches the artifact&apos;s name</li>
            <li>a validator for every backup set whose pattern matches</li>
            <li>the set&apos;s own validator_id</li>
            <li>the one script in the set&apos;s recovery-test folder</li>
            <li>the built-in structure check, unless it is switched off</li>
          </ol>
          Two patterns at one scope that both match one name are refused when you add the second, and a test of such a name ends
          could not complete rather than guessing.
        </div>

        {notConfigured ? (
          <p style={{ margin: 0 }}>
            Validators are part of a configuration this instance has not been given yet. They become available once the first
            backup set has been added.
          </p>
        ) : null}
        {error ? <div role="alert" data-validators-error="" style={{ color: "var(--danger)", fontWeight: 600 }}>{error}</div> : null}
        {refused ? (
          <div role="alert" data-validators-refused="" style={{ color: "var(--danger)", fontWeight: 600 }}>
            No validator script runs while the validator directory is refused: {refused}
          </div>
        ) : null}
        {list && list.ambiguities.length > 0 ? (
          <div role="status" data-testid="validators-ambiguities" style={{ fontSize: "var(--text-sm)", color: "var(--text)" }}>
            <strong>Two patterns match one backup.</strong> A recovery test of these ends could not complete rather than guessing,
            so narrow one pattern or change its scope:
            <ul style={{ margin: "4px 0 0", paddingLeft: 20 }}>
              {list.ambiguities.map((a) => (
                <li key={a.example}>
                  {a.validators.join(" and ")} both match <span className="mono" style={{ overflowWrap: "anywhere" }}>{a.example}</span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {list && !list.available ? (
          <p role="status" style={{ margin: 0 }} data-validators-unavailable="">
            This deployment cannot keep custom validators: {list.unavailableReason || "no workflow root is configured"}.
          </p>
        ) : null}
        {list && list.available && list.workflowRoot ? (
          // Where an upload lands, and whether the root is one config.yaml
          // names or the mounted default the engine found (#1246), so an
          // operator can find the scripts and back them up.
          <p style={{ margin: 0, fontSize: "var(--text-sm)", color: "var(--text-2)" }} data-validators-root="">
            Scripts are kept in{" "}
            <span className="mono" style={{ overflowWrap: "anywhere" }}>{list.directory.startsWith("/") ? list.directory : list.workflowRoot + "/" + list.directory}</span>
            {list.workflowRootDefault
              ? ", under the default workflow root: config.yaml names none, and this directory is mounted."
              : "."}
          </p>
        ) : null}

        <div role="status" data-validators-status="" style={{ fontSize: "var(--text-sm)", color: "var(--text)", minHeight: status ? undefined : 0 }}>
          {status}
        </div>

        {list && list.validators.length === 0 && list.available ? (
          <p style={{ margin: 0, color: "var(--text-2)" }} data-validators-empty="">
            No custom validators yet. Every recovery test uses its set&apos;s validator_id, its recovery-test folder or the built-in
            check. Add one here, or choose Declare validator on a backup&apos;s own page.
          </p>
        ) : null}

        {list && list.validators.length > 0 ? (
          <div className="table-scroll">
            <table className="table" style={{ minWidth: 1050 }} aria-label="Custom validators">
              <thead>
                <tr>
                  {["Name", "Script", "Environment", "Pattern", "Applies to", "Matches", "Last test"].map((h) => (
                    <th key={h} scope="col" style={{ color: "var(--text-2)" }}>{h}</th>
                  ))}
                  <th scope="col" style={{ color: "var(--text-2)" }}><span className="visually-hidden">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {list.validators.map((v) => (
                  <tr key={v.name} data-validator={v.name}>
                    <td className="mono" style={{ overflowWrap: "anywhere", minWidth: 130 }}>{v.name}</td>
                    <td><ScriptStateCell v={v} /></td>
                    <td style={{ minWidth: 150 }}><ValidatorEnvLabel env={v.env} /></td>
                    <td>
                      <span className="mono" style={{ overflowWrap: "anywhere" }}>{v.pattern}</span>
                      {!v.anchored ? (
                        <div style={{ fontSize: "var(--text-xs)", color: "var(--text-2)" }}>
                          Not anchored: matches anywhere in a name
                        </div>
                      ) : null}
                    </td>
                    <td>{v.backupSetId || "Every backup set"}</td>
                    <td title={v.matchSets.join(", ")}>{matchWords(v.matchCount)}</td>
                    <td>{lastResultWords(v.lastResult)}</td>
                    <td style={{ whiteSpace: "nowrap", textAlign: "right" }}>
                      <span style={{ display: "inline-flex", gap: 6 }}>
                        <button className="btn btn--sm btn--strong-edge" aria-label={"View script " + v.name} onClick={() => void viewScript(v.name)}>View</button>
                        <button className="btn btn--sm btn--strong-edge" aria-label={"Edit " + v.name} disabled={disabled} onClick={() => setDialog({ kind: "edit", validator: v })}>Edit</button>
                        <button className="btn btn--sm btn--strong-edge" aria-label={"Remove " + v.name} disabled={readOnly} onClick={() => setRemoving(v)}>Remove</button>
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
      </div>

      {dialog && list ? (
        <CustomValidatorDialog
          mode={dialog}
          list={list}
          returnFocus={dialog.kind === "add" ? addButton : undefined}
          onClose={() => { setDialog(null); void load(); }}
          onSaved={({ validator, edited }) => {
            setDialog(null);
            setStatus((edited ? "Saved " : "Uploaded ") + validator.name + ".");
            void load();
          }}
        />
      ) : null}

      {script ? <ValidatorScriptDialog name={script.name} script={script.script} onClose={() => setScript(null)} /> : null}

      <ConfirmationDialog
        open={removing !== null}
        eyebrow="Remove validator"
        title={"Remove validator " + (removing?.name ?? "")}
        confirmLabel="Remove validator"
        destructive
        disabled={removeBusy}
        onConfirm={() => void remove()}
        onCancel={() => setRemoving(null)}
      >
        <p style={{ margin: 0 }}>
          <span className="mono">{removing?.name}</span> applies to{" "}
          <span className="mono">{removing?.backupSetId || "every backup set"}</span>, and {matchWords(removing?.matchCount ?? 0)}{" "}
          match its pattern now. Removing it stops it running and moves its script aside, to recovery-test-validators.superseded
          beside the stage directory; nothing is deleted.
        </p>
        <p style={{ margin: 0 }}>
          A recovery test of those backups then falls back to the next check in the order above: the set&apos;s validator_id, the one
          script in its recovery-test folder, or the built-in check (and to no check at all if that is switched off). Backups and past
          results are not touched.
        </p>
      </ConfirmationDialog>
    </section>
  );
}
