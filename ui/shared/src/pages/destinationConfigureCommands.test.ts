import { describe, expect, it } from "vitest";
import type { BackendManifest } from "@shared/api/contracts";
import {
  configureCommand,
  endpointNote,
  endpointWithoutUserinfo,
  importCredentialsCommand,
  preflightCommand
} from "@shared/pages/destinationConfigureCommands";
import { setDefaultCommand } from "@shared/pages/storageDestinationCommands";
// The vectors core/cliecho's configure_golden_1252_test.go checks too.
// Through `?raw` for the reason manifest-field-renderer.test.tsx gives.
import goldenSource from "../../../../core/cliecho/testdata/medium_configure_1252.json?raw";

/**
 * The lines the configure-a-destination wizard prints (#1252).
 *
 * The hand-written cases below read well as documentation. The ones that
 * keep this copy honest are at the bottom: the shared golden file, which
 * the Go suite checks core/cliecho against, so the line printed beside a
 * button and the line the terminal records after it describe the same
 * configuration. Where they differ is written down in the module's own
 * docblock.
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
    expect(setDefaultCommand("usb_disk")).toBe("retnd medium default usb_disk");
  });

  it("still takes credential material on stdin only", () => {
    expect(importCredentialsCommand()).toBe("retnd medium import-credentials --stdin");
  });

  it("keeps a field whose id is an Object.prototype key (#1252 review)", () => {
    const odd: BackendManifest = {
      ...LOCAL_VOLUME,
      fields: [
        { id: "constructor", label: "Odd", kind: "string", required: false },
        { id: "toString", label: "Odder", kind: "string", required: false }
      ]
    };
    expect(configureCommand(odd, "x", { constructor: "a", toString: "b" })).toBe(
      "retnd medium configure x --backend local_volume --field constructor=a --field toString=b"
    );
  });

  it("cuts userinfo out of any url-kind field, whatever its id (#1252 review)", () => {
    const webdav: BackendManifest = {
      ...LOCAL_VOLUME,
      id: "webdav",
      fields: [{ id: "webdav_url", label: "Address", kind: "url", required: true }]
    };
    const line = configureCommand(webdav, "dav", { webdav_url: "https://u:secret@dav.example.test/x" });
    expect(line).toBe("retnd medium configure dav --backend webdav --field webdav_url=https://dav.example.test/x");
    expect(endpointNote(webdav, { webdav_url: "https://u:secret@dav.example.test/x" })).toBe("stripped");
  });

  it("says which way the endpoint went, for the note under the line", () => {
    expect(endpointNote(S3, { bucket: "b" })).toBeNull();
    expect(endpointNote(S3, { bucket: "b", endpoint: "https://s3.example.test" })).toBeNull();
    expect(endpointNote(S3, { bucket: "b", endpoint: "https://k:s@s3.example.test" })).toBe("stripped");
    expect(endpointNote(S3, { bucket: "b", endpoint: "https://k:s/x@s3.example.test" })).toBe("withheld");
  });
});

interface Golden {
  endpoint_vectors: { in: string; out: string; withheld: boolean }[];
  configure_cases: {
    name: string;
    id: string;
    backend: string;
    fields: Record<string, string>;
    kinds: Record<string, string>;
    credentials_id: string;
    preflight: boolean;
    line: string;
    runnable: boolean;
  }[];
  default_cases: { id: string; line: string }[];
}

const golden = JSON.parse(goldenSource) as Golden;

/** A manifest carrying just the fields a golden case names, with the kinds
 *  it gives them: the browser decides by kind, and cliecho by the bundled
 *  manifest, which is where these kinds were copied from. */
function goldenManifest(c: Golden["configure_cases"][number]): BackendManifest {
  return {
    ...LOCAL_VOLUME,
    id: c.backend,
    fields: Object.keys(c.fields).map((id) => ({
      id,
      label: id,
      kind: (c.kinds[id] ?? "string") as BackendManifest["fields"][number]["kind"],
      required: false
    }))
  };
}

describe("the golden file core/cliecho is checked against (#1252)", () => {
  it("has cases in it, so the loops below cannot pass on nothing", () => {
    expect(golden.endpoint_vectors.length).toBeGreaterThan(5);
    expect(golden.configure_cases.length).toBeGreaterThan(3);
    expect(golden.default_cases.length).toBeGreaterThan(0);
  });

  it.each(golden.endpoint_vectors)("cuts the endpoint $in the way cliecho does", (v) => {
    expect(endpointWithoutUserinfo(v.in)).toEqual({ value: v.out, withheld: v.withheld });
  });

  it.each(golden.configure_cases)("prints the line cliecho prints: $name", (c) => {
    const build = c.preflight ? preflightCommand : configureCommand;
    const manifest = goldenManifest(c);
    expect(build(manifest, c.id, c.fields, c.credentials_id || null)).toBe(c.line);
    expect(endpointNote(manifest, c.fields) !== "withheld").toBe(c.runnable);
  });

  it.each(golden.default_cases)("prints medium default $id the way cliecho does", (c) => {
    expect(setDefaultCommand(c.id)).toBe(c.line);
  });
});
