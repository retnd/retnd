/**
 * The mock's medium configuration read maps every field the engine does,
 * `path` included, so a declared local volume reads back with its directory
 * (found reviewing #1257, folded into #1249).
 */
import { describe, expect, it } from "vitest";
import { createMockApi } from "@shared/api/mock";

describe("the mock's getStorageMediumConfiguration", () => {
  it("reads a declared local volume's path back", async () => {
    const got = await createMockApi("default").getStorageMediumConfiguration("usb_shelf");
    expect(got.fields.path).toBe("/mnt/usb-shelf");
  });
});
