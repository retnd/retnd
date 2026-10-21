import { describe, expect, it } from "vitest";
import type { BackendManifest } from "@shared/api/contracts";
import {
  configureCommand,
  defaultCommand,
  importCredentialsCommand,
  preflightCommand
} from "@shared/pages/destinationConfigureCommands";

/**
 * The lines the configure-a-destination wizard prints (#1252).
 *
 * Each expected string below is the one core/cliecho prints for the same
 * request (storageconfigure_1154_test.go), so the line beside a control and
 * the line the global terminal records after it agree byte for byte.
 */

const LOCAL_VOLUME: BackendManifest = {
  id: "local_volume",
  label: "Local volume",
  summary: "a directory on this machine",
  role: "local_volume",
  configurable: true,
  fields: [
    { id: "path", label: "Directory", kind: "path", required: true },
    { id: "prefix", label: "Subdirectory", kind: "key_prefix", required: false },
    { id: "upload_verification", label: "How a copy is proven", kind: "enum", required: false }
  ],
  probe: { steps: [] }
};

const S3: BackendManifest = {
  id: "s3",
  label: "S3 or S3-compatible",
  summary: "a bucket",
  role: "object_store",
  configurable: true,
  fields: [
    { id: "credentials", label: "Access key", kind: "credential", required: true },
    { id: "bucket", label: "Bucket", kind: "string", required: true },
    { id: "region", label: "Region", kind: "string", required: false },
    { id: "endpoint", label: "Endpoint", kind: "url", required: false },
    { id: "prefix", label: "Key namespace", kind: "key_prefix", required: false },
    { id: "storage_class", label: "Storage class", kind: "enum", required: false }
  ],
  probe: { steps: [] }
};

describe("the configure wizard's terminal lines (#1252)", () => {
  it("carries a local volume's path as a --field on medium configure", () => {
    expect(configureCommand(LOCAL_VOLUME, "usb_disk", { path: "/mnt/usb", prefix: "retnd" })).toBe(
      "retnd medium configure usb_disk --backend local_volume --field path=/mnt/usb --field prefix=retnd"
    );
  });

  it("puts every field on the line, sorted by field id the way the request body is", () => {
    expect(
      configureCommand(S3, "cold", {
        bucket: "acme-backups",
        region: "eu-central-1",
        endpoint: "https://s3.example.test",
        prefix: "monthly",
        storage_class: "DEEP_ARCHIVE"
      })
    ).toBe(
      "retnd medium configure cold --backend s3 --field bucket=acme-backups" +
        " --field endpoint=https://s3.example.test --field prefix=monthly" +
        " --field region=eu-central-1 --field storage_class=DEEP_ARCHIVE"
    );
  });

  it("names the credential by reference and never as a field", () => {
    const line = configureCommand(S3, "cold", { bucket: "acme-backups", region: "eu-central-1" }, "cred_01HX");
    expect(line).toBe(
      "retnd medium configure cold --backend s3 --field bucket=acme-backups --field region=eu-central-1 --credentials-id cred_01HX"
    );
    expect(line).not.toContain("--field credentials");
  });

  it("leaves the credential off when none was exchanged, so the stored one is kept", () => {
    expect(configureCommand(S3, "cold", { bucket: "acme-backups" }, null)).toBe(
      "retnd medium configure cold --backend s3 --field bucket=acme-backups"
    );
  });

  it("quotes a value the shell would split", () => {
    expect(configureCommand(S3, "cold", { bucket: "acme backups" })).toBe(
      "retnd medium configure cold --backend s3 --field 'bucket=acme backups'"
    );
  });

  it("takes the userinfo out of an endpoint, as the server's echo does", () => {
    const line = configureCommand(S3, "cold", { bucket: "b", endpoint: "https://AKIA:secret@s3.example.test/x" });
    expect(line).toContain("--field endpoint=https://s3.example.test/x");
    expect(line).not.toContain("secret");
  });

  it("skips an empty value, which medium configure refuses", () => {
    expect(configureCommand(LOCAL_VOLUME, "usb_disk", { path: "/mnt/usb", prefix: "" })).toBe(
      "retnd medium configure usb_disk --backend local_volume --field path=/mnt/usb"
    );
  });

  it("prints the test step as the same line with --preflight", () => {
    expect(preflightCommand(LOCAL_VOLUME, "usb_disk", { path: "/mnt/usb" })).toBe(
      "retnd medium configure usb_disk --backend local_volume --field path=/mnt/usb --preflight"
    );
    expect(preflightCommand(S3, "cold", { bucket: "acme-backups" }, "cred_01HX")).toBe(
      "retnd medium configure cold --backend s3 --field bucket=acme-backups --credentials-id cred_01HX --preflight"
    );
  });

  it("prints medium default for making it the default", () => {
    expect(defaultCommand("usb_disk")).toBe("retnd medium default usb_disk");
  });

  it("still takes credential material on stdin only", () => {
    expect(importCredentialsCommand()).toBe("retnd medium import-credentials --stdin");
  });
});
