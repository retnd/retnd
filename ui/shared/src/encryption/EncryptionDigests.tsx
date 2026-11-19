/**
 * The two digests on Backup detail (EPIC N, issue #995; design section 5), as
 * definition rows for the artifact's facts. They are two numbers in two places
 * and the labels say which is which: the plaintext digest is the artifact's
 * (artifacts.local_hash), and each encrypted copy has its own stored-bytes
 * digest, with the suite and key it was written under (#1318). Under
 * encryption they are supposed to differ. A digest the route
 * omitted gets no row, never a placeholder, and a deployment with no encrypted
 * copy gets nothing at all.
 */
import { Row } from "@shared/components/Definitions";
import type { CopyEncryption } from "@shared/types/encryption";
import { clipText } from "./ClippedText";

export function EncryptionDigests({ copies }: { copies: CopyEncryption[] | null }) {
  const encrypted = (copies ?? []).filter((c) => c.encrypted);
  // The artifact has one plaintext digest; every encrypted copy repeats it.
  const plaintext = encrypted.find((c) => c.plaintextDigest !== null)?.plaintextDigest ?? null;
  return (
    <>
      {plaintext !== null ? <Row label="Plaintext digest" value={plaintext} mono tip="encryption.digest.plaintext" /> : null}
      {encrypted.filter((c) => c.storedDigest !== null).map((c) => (
        <Row
          key={c.medium}
          label={"Stored-bytes digest, " + c.medium}
          value={
            <>
              {c.storedDigest}
              {c.format ? (
                // The format id IS the suite id (EPIC T, ADR 0033), so the
                // line names it as the suite (#1318).
                <span style={{ color: "var(--text-2)" }}>
                  {" (suite " + clipText(c.format) + (c.formatVersion !== null ? " v" + c.formatVersion : "") + (c.keyId ? ", key " + clipText(c.keyId) : "") + ")"}
                </span>
              ) : null}
            </>
          }
          mono
          tip="encryption.digest.stored"
        />
      ))}
    </>
  );
}
