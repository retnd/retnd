/**
 * retnd#1049, the leftovers on the failure side of a first install.
 *
 * Three things the first pass left undone, each asserted on what is in the
 * DOM at the end rather than on a call being made:
 *
 *  1. The "retnd could not be reached" surfaces (the full page that replaces
 *     the sign-in form, and the ErrorState a card draws for a gateway
 *     refusal) used to wait for somebody to press Try again or reload. The
 *     service coming back is not something the page needs a person to
 *     notice: it asks /health/live, which serve-ui proxies to the engine,
 *     and retries by itself when that answers.
 *  2. "Nothing is configured yet" and "the service is down" are different
 *     states and the DOM says so: `first-run-not-configured` on the guide,
 *     `service-unreachable` on a surface that could not reach the service.
 *     Neither testid ever appears for the other state.
 *  3. When the engine could not write its default configuration because the
 *     directory is read-only, the guide says which directory and what to do
 *     instead of leaving an operator at a setup flow that cannot finish.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ErrorState } from "@shared/components/EmptyState";
import { FirstRunGuide } from "@shared/components/FirstRunGuide";
import { ServiceUnreachablePage } from "@shared/pages/SessionCheckFailure";
import type { ApiError, FirstRunStatus } from "@shared/api/contracts";

const POLL_MS = 3_000;

const gatewayError: ApiError = {
  code: "INTERNAL",
  message: "retnd's web interface could not reach the retnd service.",
  correlationId: "cid_proxy502",
  origin: "gateway"
};

/** fetch, answering from a script: a number is an HTTP status, "down" is a
 *  rejected fetch (nothing arrived at all). The last entry repeats. */
function scriptedFetch(script: Array<number | "down">) {
  let call = 0;
  const calls: string[] = [];
  const impl = vi.fn((input: RequestInfo | URL) => {
    calls.push(String(input));
    const step = script[Math.min(call, script.length - 1)];
    call += 1;
    if (step === "down") return Promise.reject(new TypeError("Failed to fetch"));
    return Promise.resolve(new Response(step === 200 ? '{"status":"ok"}' : "", { status: step }));
  });
  vi.stubGlobal("fetch", impl);
  return { calls, impl };
}

async function tick(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the page that replaces the sign-in form when the service is unreachable", () => {
  it("is marked service-unreachable and retries by itself once /health/live answers", async () => {
    const { calls } = scriptedFetch([502, "down", 200]);
    const onRetry = vi.fn();
    render(<ServiceUnreachablePage error={gatewayError} onRetry={onRetry} />);

    expect(screen.getByTestId("service-unreachable")).toBeInTheDocument();
    expect(screen.queryByTestId("first-run-not-configured")).toBeNull();

    // Still down for two polls: nothing is retried, and nothing is said
    // to have recovered.
    await tick(POLL_MS);
    await tick(POLL_MS);
    expect(onRetry).not.toHaveBeenCalled();

    // The third poll answers 200, so the page asks again without a click.
    await tick(POLL_MS);
    expect(onRetry).toHaveBeenCalledTimes(1);
    expect(calls.every((c) => c.endsWith("/health/live"))).toBe(true);
  });

  it("stops asking once it has recovered, and when it is unmounted", async () => {
    const { impl } = scriptedFetch([200]);
    const onRetry = vi.fn();
    const { unmount } = render(<ServiceUnreachablePage error={gatewayError} onRetry={onRetry} />);
    await tick(POLL_MS);
    expect(onRetry).toHaveBeenCalledTimes(1);
    const askedAtRecovery = impl.mock.calls.length;
    await tick(POLL_MS * 3);
    expect(impl.mock.calls.length).toBe(askedAtRecovery);

    scriptedFetch([502]);
    const second = vi.fn();
    const again = render(<ServiceUnreachablePage error={gatewayError} onRetry={second} />);
    again.unmount();
    unmount();
    await tick(POLL_MS * 3);
    expect(second).not.toHaveBeenCalled();
  });
});

describe("an ErrorState that could not reach the service", () => {
  it("clears itself when the service recovers: it asks again without a click", async () => {
    scriptedFetch([503, 200]);
    const onRetry = vi.fn();
    render(<ErrorState {...gatewayError} onRetry={onRetry} />);

    expect(screen.getByTestId("service-unreachable")).toBeInTheDocument();
    await tick(POLL_MS);
    expect(onRetry).not.toHaveBeenCalled();
    await tick(POLL_MS);
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("is not marked, and does not poll, when the service itself refused", async () => {
    const { impl } = scriptedFetch([200]);
    const onRetry = vi.fn();
    render(
      <ErrorState
        message="Could not read the settings."
        correlationId="cid_real"
        origin="service"
        onRetry={onRetry}
      />
    );
    await tick(POLL_MS * 3);
    expect(screen.queryByTestId("service-unreachable")).toBeNull();
    expect(impl).not.toHaveBeenCalled();
    expect(onRetry).not.toHaveBeenCalled();
  });
});

describe("the first-run guide", () => {
  const fresh: FirstRunStatus = {
    configured: true,
    nextStep: "backup_set",
    steps: { administrator: "done", email: "skipped", backup_set: "pending" }
  };

  function renderGuide(status: FirstRunStatus, configured = true) {
    return render(
      <MemoryRouter>
        <FirstRunGuide status={status} configured={configured} onSkip={async () => undefined} />
      </MemoryRouter>
    );
  }

  it("is marked first-run-not-configured and is never the unreachable state", () => {
    renderGuide(fresh);
    expect(screen.getByTestId("first-run-not-configured")).toBeInTheDocument();
    expect(screen.queryByTestId("service-unreachable")).toBeNull();
    // A fresh install is a notice, not an alert.
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("names an unwritable configuration directory and what to do about it", () => {
    renderGuide(
      {
        ...fresh,
        configured: false,
        configDir: "/etc/retnd/config",
        configDirProblem:
          "The configuration directory /etc/retnd/config is not writable, so retnd cannot write its default configuration there. Mount that directory writable for the user retnd runs as, then restart retnd."
      },
      false
    );
    const alert = screen.getByTestId("config-dir-unwritable");
    expect(alert).toHaveAttribute("role", "alert");
    expect(alert).toHaveTextContent("/etc/retnd/config");
    expect(alert).toHaveTextContent(/not writable/i);
    expect(alert).toHaveTextContent(/mount that directory writable/i);
    // Still the first-run state, and still not the unreachable one.
    expect(screen.getByTestId("first-run-not-configured")).toBeInTheDocument();
    expect(screen.queryByTestId("service-unreachable")).toBeNull();
  });
});
