/**
 * The documented default validator script, fetched once per API (issue #1203).
 * "Start from the default" and "Download" both want it, and an operator can
 * open and close the dialog several times; the text does not change under a
 * running engine, so it is asked for once and a failed ask is not remembered.
 */
import type { RetndApi, ValidatorScript } from "@shared/api/contracts";

// Keyed by the method itself, which is one function per API however many
// times a component is handed that API.
const cache = new WeakMap<object, Promise<ValidatorScript>>();

export function defaultValidatorScript(api: Pick<RetndApi, "getDefaultValidatorScript">): Promise<ValidatorScript> {
  const key = api.getDefaultValidatorScript;
  let p = cache.get(key);
  if (!p) {
    const fresh = api.getDefaultValidatorScript();
    p = fresh;
    cache.set(key, fresh);
    fresh.catch(() => { if (cache.get(key) === fresh) cache.delete(key); });
  }
  return p;
}
