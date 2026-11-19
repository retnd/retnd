/**
 * The composition root: where the provider tree is assembled and every
 * app-wide choice is made once.
 *
 * The nesting order below is load-bearing rather than incidental. The
 * error boundary is outermost so it can still render when the thing that
 * threw is a provider itself, the platform provider comes before the API
 * one because auth is what decides whether anything under it should
 * render at all, and the router is innermost because it is the only part
 * that pages read on every navigation. The tooltip layer (#874) is
 * inside the router and around everything routed, because it is a
 * delegated listener over the whole application's markup and has to
 * survive every navigation without re-installing itself.
 *
 * The API default is the one place the mock is chosen, keyed on the dev
 * build. A provider shell that wants something else passes it, which is
 * how the browser suite drives real screens against fixtures without this
 * file knowing that is happening.
 *
 * The mock is loaded with a dynamic import() (#1216), so the first render
 * waits one module fetch in development and a production build, where the
 * condition folds to false, never contains the mock or its fixtures.
 */
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "@shared/App";
import { ApiProvider } from "@shared/api/ApiContext";
import { PlatformProvider } from "@shared/platform/PlatformContext";
import { httpApi } from "@shared/api/client";
import { withHttpAssurance } from "@shared/assurance/realAssurance";
import type { RetndApi } from "@shared/api/contracts";
import type { PlatformBridge } from "@shared/types/platform";
import { TooltipAutoAttach } from "@shared/tooltips/TooltipAutoAttach";
import { ErrorBoundary } from "./ErrorBoundary";

/** The single mount path every provider shell calls. A provider supplies its
 *  bridge and, optionally, its own API instance. Nothing provider-specific
 *  lives below this line. */
export async function createApp(container: HTMLElement, bridge: PlatformBridge, provided?: RetndApi): Promise<void> {
  const api = provided ?? (import.meta.env.DEV ? (await import("./devApi")).createDevApi() : withHttpAssurance(httpApi));
  createRoot(container).render(
    <StrictMode>
      <ErrorBoundary>
        <PlatformProvider bridge={bridge}>
          <ApiProvider api={api}>
            <BrowserRouter>
              <TooltipAutoAttach>
                <App />
              </TooltipAutoAttach>
            </BrowserRouter>
          </ApiProvider>
        </PlatformProvider>
      </ErrorBoundary>
    </StrictMode>
  );
}
