import { describe, expect, it } from "vitest";
import { API_OPERATIONS } from "@shared/api/generated/contract";
import {
  copiesCommand, keyConfirmCommand, keyCreateCommand, keyExportCommand, keyListCommand, keyRetireCommand,
  keyRotateCommand, policyPatchCommand, policyResetCommand, policyShowCommand, providerTestCommand,
  reencryptCommand, setEncryptionCommand, setEncryptionResetCommand, verifyCommand
} from "./encryptionCommands";

/**
 * Issue #995: the equivalent command for every mutating encryption control,
 * and the reverse-direction sweep #993 left to this issue. Written by reading
 * core/cliecho/routes.go and encryption_993_test.go; not run.
 */

describe("the echo says what core/cliecho prints for the same route (#995)", () => {
  it("matches the strings encryption_993_test.go pins", () => {
    expect(policyShowCommand()).toBe("retnd encryption policy");
    expect(policyPatchCommand({ mode: "required", keyProvider: { managed: true } })).toBe("retnd encryption policy patch --mode required --managed");
    expect(policyPatchCommand({ keyProvider: { external: { file: "/run/secrets/key" } } })).toBe("retnd encryption policy patch --external-file /run/secrets/key");
    expect(policyResetCommand()).toBe("retnd encryption policy reset");
    expect(setEncryptionCommand("api-server/var-backups", "required", { managed: true })).toBe("retnd encryption backup-set api-server/var-backups --mode required --managed");
    expect(setEncryptionResetCommand("api-server/var-backups")).toBe("retnd encryption backup-set api-server/var-backups --reset");
    expect(keyListCommand()).toBe("retnd encryption key list");
    expect(keyCreateCommand()).toBe("retnd encryption key create");
    expect(keyRotateCommand()).toBe("retnd encryption key rotate");
    expect(keyRetireCommand("k01")).toBe("retnd encryption key retire k01");
    expect(keyConfirmCommand("k01")).toBe("retnd encryption key confirm k01");
    expect(verifyCommand("api-server/var-backups/a.dump", { medium: "offsite", allowEgress: true })).toBe(
      "retnd encryption verify api-server/var-backups/a.dump --medium offsite --allow-egress"
    );
    expect(copiesCommand("api-server/var-backups/a.dump")).toBe("retnd encryption copies api-server/var-backups/a.dump");
  });

  it("never echoes a command reference's arguments, only the stand-in", () => {
    const line = policyPatchCommand({ keyProvider: { external: { command: ["vault", "kv", "get", "secret/offsite"] } } });
    expect(line).toBe("retnd encryption policy patch --external-command '<the command that prints the key>'");
    expect(line).not.toContain("vault");
    expect(line).not.toContain("secret/offsite");
    expect(providerTestCommand({ external: { command: ["fetch-key", "--token", "abc"] } })).not.toContain("abc");
  });

  it("names a placeholder file for the bundle export, so there is nothing on the line that could be the key", () => {
    expect(keyExportCommand("k01")).toBe("retnd encryption key export k01 --out '<a path to write the bundle to>'");
  });

  it("quotes an operand that needs it", () => {
    expect(keyRetireCommand("a key; rm")).toBe("retnd encryption key retire 'a key; rm'");
  });
});

/**
 * Every mutating control the browser draws, by the operation it calls and the
 * echo shown beside it. The table is written by hand on purpose: it is the
 * record of the sweep, and the assertions below fail in both directions.
 */
const CONTROLS: Record<string, { control: string; echo: () => string }> = {
  patchEncryptionPolicy: { control: "Enable / Turn off / Use an external key reference (Settings card)", echo: () => policyPatchCommand({ mode: "required" }) },
  resetEncryptionPolicy: { control: "Remove the policy block (Settings card)", echo: policyResetCommand },
  setBackupSetEncryption: { control: "Off / Required, and a set's own key reference (set card)", echo: () => setEncryptionCommand("a/b", "required") },
  clearBackupSetEncryption: { control: "Inherit (set card)", echo: () => setEncryptionResetCommand("a/b") },
  createEncryptionKey: { control: "Enable and create key (Settings card)", echo: keyCreateCommand },
  rotateEncryptionKey: { control: "Rotate key (Settings card)", echo: keyRotateCommand },
  retireEncryptionKey: { control: "Retire (key table)", echo: () => keyRetireCommand("k") },
  exportRecoveryBundle: { control: "Export recovery bundle", echo: () => keyExportCommand("k") },
  confirmRecoveryBundle: { control: "I have stored the bundle safely", echo: () => keyConfirmCommand("k") },
  testEncryptionProvider: { control: "Test reference", echo: () => providerTestCommand({ external: { env: "X" } }) },
  startDecryptVerify: { control: "Verify recoverable", echo: () => verifyCommand("a/b/c", { medium: "m" }) },
  startReencrypt: { control: "Re-encrypt under current suite (Dashboard, #1318)", echo: () => reencryptCommand({ suite: "age/v1", to: "offsite_cold" }) }
};

describe("no browser action without a command, and no command without an action (#995 sweep)", () => {
  const mutating = API_OPERATIONS.filter((op) => op.method !== "GET" && /encryption/.test(op.path)).map((op) => op.id).sort();

  it("finds the operations at all", () => {
    // An empty scan is not a clean scan.
    expect(mutating.length).toBeGreaterThanOrEqual(11);
  });

  it("has a control row for every mutating encryption operation", () => {
    expect(Object.keys(CONTROLS).sort()).toEqual(mutating);
  });

  it("gives each control a real retnd encryption command", () => {
    for (const [id, c] of Object.entries(CONTROLS)) {
      expect(c.echo(), id).toMatch(/^retnd encryption (policy|backup-set|key|provider|verify|reencrypt)/);
    }
  });
});
