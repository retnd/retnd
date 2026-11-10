/**
 * The encryption conditions on the Dashboard (EPIC N, issue #995; design
 * section 10), drawn in the same banner vocabulary as the other warnings and
 * never dismissible: the words are the only thing standing between an operator
 * and a misunderstanding.
 *
 * EPIC T (#1318) adds the suites: a row for a policy whose suite has no active
 * key (copies under it are refused until a rotation), and a row per deprecated
 * or retired suite still recorded by an active copy or a key, in T2.1's
 * sentence, with Re-encrypt under current suite, which runs its dry run first
 * (ReencryptDialog.tsx).
 *
 * A deployment that encrypts nothing has no keys and no recorded suites, so
 * nothing here is drawn and nothing else on the Dashboard changes, which is
 * also how a test can tell the zero value is not what is doing the work. A
 * read that fails draws nothing either: not knowing is not a condition, and
 * the Settings card says what went wrong when it is opened.
 */
import { useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { WarningBanner } from "@shared/components/WarningBanner";
import { useAsync } from "@shared/hooks/useAsync";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { EncryptionSuite } from "@shared/types/encryption";
import { settingsLink } from "@shared/utilities/routes";
import { clipText } from "./ClippedText";
import { encryptionConditions, suiteHealthSentence, suiteWithoutKey } from "./encryptionState";
import { readKeys, readPolicy, readSuites } from "./offered";
import { ReencryptDialog } from "./ReencryptDialog";

export function EncryptionHealthRows() {
  const api = useApi();
  const keys = useAsync(() => readKeys(api), [api]);
  const suites = useAsync(() => readSuites(api), [api]);
  // A policy that cannot be read is not a condition (see above).
  const policy = useAsync(() => readPolicy(api).catch(() => null), [api]);
  const [reencrypt, setReencrypt] = useState<string | null>(null);

  const keyList = keys.data ?? [];
  const conditions = encryptionConditions(keyList);
  const owedSuite = suiteWithoutKey(policy.data ?? null, keyList);
  const activeSuite = keyList.find((k) => k.status === "active")?.suite ?? null;
  const suiteRows = (suites.data ?? [])
    .map((s) => ({ s, sentence: suiteHealthSentence(s, activeSuite === s.id) }))
    .filter((r): r is { s: EncryptionSuite; sentence: string } => r.sentence !== null);

  if (conditions.length === 0 && owedSuite === null && suiteRows.length === 0) return null;
  return (
    <div data-encryption-health="" style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      {owedSuite ? (
        <WarningBanner
          tone="danger"
          eyebrow="Encryption"
          title={"No active key for suite " + clipText(owedSuite)}
          tip="encryption.health.suite"
          dismissible={false}
          actions={<Link className="btn btn--sm" to={settingsLink("encryption")}>Rotate in Settings</Link>}
        >
          {"The policy names " + clipText(owedSuite) + " and no active key belongs to it, so copies under the policy are refused, before anything is deleted, until a key is rotated under it."}
        </WarningBanner>
      ) : null}
      {suiteRows.map(({ s, sentence }) => (
        <div key={"s-" + s.id} data-suite-health={s.id}>
          <WarningBanner
            tone="warn"
            eyebrow="Encryption"
            title={(s.status === "retired" ? "Retired suite: " : "Deprecated suite: ") + clipText(s.id)}
            tip="encryption.health.suite"
            dismissible={false}
            actions={
              s.activeCopies > 0 ? (
                <InfoTooltip id="encryption.reencrypt">
                  <button type="button" className="btn btn--sm" onClick={() => setReencrypt(s.id)}>
                    Re-encrypt under current suite
                  </button>
                </InfoTooltip>
              ) : null
            }
          >
            {sentence}
          </WarningBanner>
        </div>
      ))}
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
            {"Copies are encrypted under key " + clipText(c.keyId) + ", which exists only on this host. If this host is lost, so are they."}
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
            {"Key " + clipText(c.keyId) + (c.reason ? ": " + clipText(c.reason) + ". " : ". ") + "Copies that need it are refused rather than written in plaintext. Nothing was deleted."}
          </WarningBanner>
        )
      )}
      {reencrypt ? <ReencryptDialog suite={reencrypt} onClose={() => setReencrypt(null)} /> : null}
    </div>
  );
}
