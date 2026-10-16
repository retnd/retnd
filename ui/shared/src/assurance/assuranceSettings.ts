/**
 * One read of the assurance settings for the screens that only show them
 * (issue #1190's review): the staging folder field wants the staging root and
 * the Backups page wants the download ceiling, and each used to fetch the
 * whole document on every mount, so switching the radio away and back read it
 * again.
 *
 * The read is kept per API instance until Settings saves a change
 * (AssuranceSettingsCard hands the saved document to rememberAssuranceSettings)
 * or the read fails, so a failure is retried on the next mount rather than
 * remembered.
 */
import { useEffect, useState } from "react";
import type { AssuranceSettings, RetndApi } from "@shared/api/contracts";
import { useApi } from "@shared/api/ApiContext";

const reads = new WeakMap<RetndApi, Promise<AssuranceSettings>>();

export function readAssuranceSettings(api: RetndApi): Promise<AssuranceSettings> {
  let p = reads.get(api);
  if (!p) {
    p = api.getAssuranceSettings();
    reads.set(api, p);
    void p.catch(() => { if (reads.get(api) === p) reads.delete(api); });
  }
  return p;
}

/** What Settings just saved, so the next screen shows it without a read. */
export function rememberAssuranceSettings(api: RetndApi, saved: AssuranceSettings) {
  reads.set(api, Promise.resolve(saved));
}

/** The settings, or null until they are read (and if the read fails). */
export function useAssuranceSettings(enabled = true): AssuranceSettings | null {
  const api = useApi();
  const [settings, setSettings] = useState<AssuranceSettings | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    readAssuranceSettings(api).then((s) => { if (live) setSettings(s); }, () => undefined);
    return () => { live = false; };
  }, [api, enabled]);
  return settings;
}
