/**
 * The guided first-run panel (retnd#1049).
 *
 * A fresh install is a working deployment that has nothing to do yet, so
 * this panel does not refuse anything: it lists the three setup steps
 * (administrator, email, first backup set), says which are done, and puts
 * one control on the step that comes next. Email and the first backup set
 * can be skipped for now, and a skipped step stays reachable from here.
 *
 * It sits above the routed pages and is mounted once for the session, so it
 * is not dismissible: closing it would last until a hard reload, and its
 * body says nothing is backed up yet.
 */
import { useState } from "react";
import { Link } from "react-router-dom";
import { WarningBanner } from "@shared/components/WarningBanner";
import type { FirstRunStatus, FirstRunStep, FirstRunStepState } from "@shared/api/contracts";

const LABEL: Record<FirstRunStep, string> = {
  administrator: "Create the administrator",
  email: "Set up email for account recovery",
  backup_set: "Add your first backup set"
};

const STATE_WORD: Record<FirstRunStepState, string> = {
  done: "Done",
  pending: "To do",
  skipped: "Skipped for now"
};

const ORDER: FirstRunStep[] = ["administrator", "email", "backup_set"];

export function FirstRunGuide({
  status,
  configured,
  onSkip
}: {
  status: FirstRunStatus;
  configured: boolean;
  onSkip(step: "email" | "backup_set"): Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const steps = status.steps;
  if (!steps || !status.nextStep || status.nextStep === "none") return null;
  const next = status.nextStep;

  const act = (step: FirstRunStep) => {
    if (step === "email") return { to: "/settings", label: "Set up email" };
    if (step === "backup_set") return { to: "/sets/new", label: "Add backup set" };
    return undefined;
  };

  return (
    <WarningBanner
      tone="info"
      eyebrow="First run"
      title={configured ? "Welcome to retnd" : "retnd has no configuration yet"}
      dismissible={false}
    >
      <div data-testid="first-run-next-step" data-next-step={next}>
        <p style={{ margin: "0 0 8px" }}>
          {configured
            ? "retnd is running on its default settings. Nothing is backed up until you add a backup set, and every step here can wait."
            : "Add your first backup set and retnd writes its configuration for you. Until that is done nothing is backed up."}
        </p>
        <ol style={{ margin: 0, paddingLeft: 20, display: "flex", flexDirection: "column", gap: 6 }}>
          {ORDER.map((step) => {
            const state = steps[step];
            const action = act(step);
            const canSkip = (step === "email" || step === "backup_set") && state === "pending";
            return (
              <li key={step} data-step={step} data-state={state}>
                <span style={{ fontWeight: step === next ? 600 : 400 }}>{LABEL[step]}</span>
                {" "}
                <span style={{ color: "var(--text-3)" }}>({STATE_WORD[state]})</span>
                {state !== "done" && action ? (
                  <>
                    {" "}
                    <Link className="btn btn--sm" to={action.to}>
                      {action.label}
                    </Link>
                  </>
                ) : null}
                {canSkip ? (
                  <>
                    {" "}
                    <button
                      type="button"
                      className="btn btn--sm btn--quiet"
                      disabled={busy}
                      onClick={() => {
                        setBusy(true);
                        void onSkip(step as "email" | "backup_set").finally(() => setBusy(false));
                      }}
                    >
                      Skip for now
                    </button>
                  </>
                ) : null}
              </li>
            );
          })}
        </ol>
      </div>
    </WarningBanner>
  );
}
