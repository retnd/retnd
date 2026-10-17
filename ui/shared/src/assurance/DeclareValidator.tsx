/**
 * Declare a validator from a backup's own page (issue #1203). The dialog opens
 * on that backup's set and on the pattern its own name gives, so the common case
 * is a name and a script away. The row also says which validator already checks
 * this backup, so an operator is not left guessing why a new one is refused.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useOptionalApi } from "@shared/api/ApiContext";
import type { CustomValidatorList } from "@shared/api/contracts";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CustomValidatorDialog } from "./CustomValidatorDialog";

export function DeclareValidator({ artifactId, readOnly }: { artifactId: string; readOnly: boolean }) {
  const api = useOptionalApi();
  const [list, setList] = useState<CustomValidatorList | null>(null);
  const [open, setOpen] = useState(false);
  const [said, setSaid] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const setId = artifactId.split("/").slice(0, 2).join("/");
  const filename = artifactId.split("/").slice(2).join("/");

  const load = useCallback(() => {
    // An engine older than #1203 has no such route; the row is then not drawn.
    if (!api || typeof api.listCustomValidators !== "function") return Promise.resolve();
    return api.listCustomValidators({ artifactId }).then(setList, () => setList(null));
  }, [api, artifactId]);

  useEffect(() => { void load(); }, [load]);

  if (!api || !list) return null;
  const r = list.resolution;
  return (
    <div className="card__body" data-validators="" style={{ display: "flex", gap: 16, alignItems: "center", flexWrap: "wrap", paddingTop: 0 }}>
      <div style={{ flex: 1, minWidth: 240, fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
        {r && r.validator ? (
          <span data-validator-covered="">A recovery test of this backup runs the validator {r.validator}.</span>
        ) : r && r.ambiguous.length > 0 ? (
          <span data-validator-covered="" role="alert" style={{ color: "var(--danger)", fontWeight: 600 }}>
            {r.ambiguous.join(" and ")} both match this name at one scope, so a recovery test of it ends could not complete.
          </span>
        ) : null}
        <div role="status" data-validator-declared="" style={{ color: "var(--text)" }}>{said}</div>
      </div>
      <InfoTooltip id="assurance.validators.declare">
        <button ref={opener} className="btn btn--strong-edge" disabled={readOnly || !list.available} onClick={() => setOpen(true)}>
          Declare validator
        </button>
      </InfoTooltip>
      {open ? (
        <CustomValidatorDialog
          mode={{ kind: "add", backupSetId: setId, artifact: { id: artifactId, filename } }}
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
