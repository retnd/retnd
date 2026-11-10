/**
 * The two encryption conditions on the Dashboard (EPIC N, issue #995; design
 * section 10), drawn in the same banner vocabulary as the other warnings and
 * never dismissible: the words are the only thing standing between an operator
 * and a misunderstanding.
 *
 * A deployment that encrypts nothing has no keys, so nothing here is drawn and
 * nothing else on the Dashboard changes, which is also how a test can tell the
 * zero value is not what is doing the work. A read that fails draws nothing
 * either: not knowing is not a condition, and the Settings card says what went
 * wrong when it is opened.
 */
import { Link } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { WarningBanner } from "@shared/components/WarningBanner";
import { useAsync } from "@shared/hooks/useAsync";
import { settingsLink } from "@shared/utilities/routes";
import { encryptionConditions } from "./encryptionState";
import { readKeys } from "./offered";

export function EncryptionHealthRows() {
  const api = useApi();
  const keys = useAsync(() => readKeys(api), [api]);
  const conditions = encryptionConditions(keys.data ?? []);
  if (conditions.length === 0) return null;
  return (
    <div data-encryption-health="" style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      {conditions.map((c) =>
        c.kind === "bundle-unconfirmed" ? (
          <WarningBanner
            key={"b-" + c.keyId}
            tone="warn"
            eyebrow="Encryption"
            title="No confirmed recovery bundle"
            tip="encryption.health.bundle"
            dismissible={false}
            actions={<Link className="btn btn--sm" to={settingsLink("encryption")}>Export and confirm in Settings</Link>}
          >
            {"Copies are encrypted under key " + c.keyId + ", which exists only on this host. If this host is lost, so are they."}
          </WarningBanner>
        ) : (
          <WarningBanner
            key={"k-" + c.keyId}
            tone="danger"
            eyebrow="Encryption"
            title="A required key cannot be resolved"
            tip="encryption.health.keyMissing"
            dismissible={false}
            actions={<Link className="btn btn--sm" to={settingsLink("encryption")}>Open Settings</Link>}
          >
            {"Key " + c.keyId + (c.reason ? ": " + c.reason + ". " : ". ") + "Copies that need it are refused rather than written in plaintext. Nothing was deleted."}
          </WarningBanner>
        )
      )}
    </div>
  );
}
