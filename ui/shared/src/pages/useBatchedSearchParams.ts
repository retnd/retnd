/**
 * The address's query parameters, with edits that survive a second edit in
 * the same tick (issue #1202, the review of #1214).
 *
 * react-router's `setSearchParams(fn)` hands `fn` the parameters of the render
 * it was made in, not the result of the call before it, so two edits in one
 * handler (clear the set filter AND drop the assurance bucket, say) each start
 * from the same old parameters and the second one silently undoes the first.
 * Here every edit starts from what the earlier edits of this tick produced,
 * and the result is read afresh from the address once it has rendered.
 */
import { useCallback, useEffect, useRef } from "react";
import { useSearchParams } from "react-router-dom";
import type { NavigateOptions } from "react-router-dom";

export function useBatchedSearchParams(): [URLSearchParams, (edit: (p: URLSearchParams) => void, options?: NavigateOptions) => void] {
  const [params, setParams] = useSearchParams();
  const rendered = useRef(params);
  const pending = useRef<URLSearchParams | null>(null);
  // The address has caught up: the next edit starts from it.
  useEffect(() => { rendered.current = params; pending.current = null; }, [params]);
  const patch = useCallback((edit: (p: URLSearchParams) => void, options?: NavigateOptions) => {
    const next = new URLSearchParams(pending.current ?? rendered.current);
    edit(next);
    pending.current = next;
    setParams(next, options);
  }, [setParams]);
  return [params, patch];
}
