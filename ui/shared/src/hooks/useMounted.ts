/**
 * Whether the component is still on screen, for code that runs after an
 * await (#1599).
 *
 * A handler or an effect that awaits a request can come back to a component
 * that has gone: the operator left the page, a dialog closed itself, a test
 * unmounted it. A setter called then does nothing useful in a browser, and in
 * vitest it throws once jsdom has been torn down (React reads `window.event`
 * before it drops the update), which is how #1589 failed the hook in files
 * that had nothing to do with it. So every setter after an await checks this
 * first:
 *
 *   const mountedRef = useMounted();
 *   const save = async () => {
 *     setBusy(true);
 *     const saved = await api.save(draft);
 *     if (!mountedRef.current) return;
 *     setBusy(false);
 *     ...
 *   };
 *
 * It returns the same ref on every render, so a handler made in an earlier
 * render reads the value now, not the one it closed over. Call it
 * `mountedRef` (or any name ending in Ref) and list it in a hook's
 * dependencies: the hooks lint can't tell that a custom hook's ref never
 * changes, and React Compiler only treats a `.current` read as a ref read
 * when the name ends in Ref. Listing it costs nothing, since it's the same
 * object every render.
 *
 * An effect with dependencies that only wants its own answer (not one from a
 * run it has since replaced) still wants its own `let live` flag: this one
 * only ever says whether the component is there.
 *
 * Why two effects. The passive effect is the one that matters: it sets the
 * ref and its cleanup clears it on unmount, and under StrictMode's rehearsal
 * (mount, unmount, mount, in one synchronous pass) it ends up true again
 * before any answer can land, since a `.then` is always a later microtask.
 * But a passive effect can run a little after the commit, and a click in
 * that gap whose await settles at once would read false on a component
 * that's plainly there. The layout effect closes that gap: it runs inside
 * the commit, before the browser can deliver an event, so the ref is true
 * the moment there's anything to click. It deliberately has no cleanup:
 * React runs layout cleanups when a Suspense boundary hides content it keeps
 * mounted, and a guard that went false then would drop real updates to a
 * component that's still alive. Only the passive cleanup, which runs on a
 * real unmount, sets it false.
 */
import { useEffect, useLayoutEffect, useRef } from "react";

export function useMounted(): { readonly current: boolean } {
  const mounted = useRef(false);
  useLayoutEffect(() => {
    mounted.current = true;
  }, []);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
