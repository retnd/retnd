/**
 * Issue #1163: when the engine says this connection's session cookie cannot
 * be Secure and the peer is not on the same machine, the operator is asked,
 * in words, before the request is sent again with the acknowledgement.
 */
import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";

import { askInsecureTransport, setInsecureTransportConfirmer } from "@shared/api/client";
import { InsecureTransportPrompt } from "@shared/components/InsecureTransportPrompt";

describe("the insecure-transport prompt", () => {
  afterEach(() => {
    cleanup();
    setInsecureTransportConfirmer(null);
  });

  it("says what is wrong in plain words and resolves true only on Continue anyway", async () => {
    render(<InsecureTransportPrompt />);
    let answer: Promise<boolean> = Promise.resolve(false);
    act(() => {
      answer = askInsecureTransport();
    });
    const dialog = await screen.findByRole("dialog", { name: "This connection is not encrypted" });
    expect(dialog.textContent).toMatch(/not encrypted/i);
    expect(dialog.textContent).toMatch(/password/i);
    act(() => {
      screen.getByRole("button", { name: "Continue anyway" }).click();
    });
    await expect(answer).resolves.toBe(true);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("resolves false on Cancel", async () => {
    render(<InsecureTransportPrompt />);
    let answer: Promise<boolean> = Promise.resolve(true);
    act(() => {
      answer = askInsecureTransport();
    });
    await screen.findByRole("dialog", { name: "This connection is not encrypted" });
    act(() => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    await expect(answer).resolves.toBe(false);
  });

  it("with the prompt not mounted nobody can say yes", async () => {
    await expect(askInsecureTransport()).resolves.toBe(false);
  });

  it("two asks at once are one question", async () => {
    render(<InsecureTransportPrompt />);
    let a: Promise<boolean> = Promise.resolve(false);
    let b: Promise<boolean> = Promise.resolve(false);
    act(() => {
      a = askInsecureTransport();
      b = askInsecureTransport();
    });
    await screen.findByRole("dialog", { name: "This connection is not encrypted" });
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    act(() => {
      screen.getByRole("button", { name: "Continue anyway" }).click();
    });
    await expect(Promise.all([a, b])).resolves.toEqual([true, true]);
  });
});
