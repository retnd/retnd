/**
 * The operation detail view (issue #985): the live stage, the durable lines,
 * a Following toggle and Cancel, for one scrub or recovery test. It is the
 * same panel the set page mounts, so what a reload shows here is what the
 * set page showed.
 *
 * A run that has left the live feed, or that a link or a reload opens cold,
 * is read back from the durable activity record (#1200): its lines, the stage
 * it reached and how it ended. A finished recovery test also shows its
 * validator's output when that output is this run's (the server keeps the
 * newest test's per set, so an older run's would be someone else's). A run
 * with nothing on record is said in words. The terminal twin,
 * `retnd assurance show <id>`, prints the same lines.
 */
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { PageHeader } from "@shared/components/PageHeader";
import { EmptyState } from "@shared/components/EmptyState";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { OperationPanel } from "./OperationPanel";
import { ValidatorLog } from "./ValidatorLog";
import { assuranceShowCommand } from "./assuranceCommands";
import { useOperation } from "./useOperation";
import type { AssuranceApi, Operation, ValidatorResult } from "./types";

/** The set's newest validator output, when it is this finished run's. */
function useRunOutput(api: AssuranceApi | undefined, op: Operation | null): ValidatorResult | null {
  const [held, setHeld] = useState<{ id: string; result: ValidatorResult | null } | null>(null);
  const ended = !!op && op.kind === "recovery_test" && op.state !== "running";
  const id = op?.id ?? "";
  const setId = op?.setId ?? "";
  useEffect(() => {
    if (!api || !ended || !setId) return;
    let live = true;
    api.getValidatorResult(setId)
      .then((r) => { if (live) setHeld({ id, result: r && r.operationId === id ? r : null }); })
      .catch(() => { if (live) setHeld({ id, result: null }); });
    return () => { live = false; };
  }, [api, ended, id, setId]);
  return held && held.id === id ? held.result : null;
}

export function OperationDetailPage({ readOnly }: { readOnly: boolean }) {
  const { id } = useParams();
  const api = useApi().assurance;
  const navigate = useNavigate();
  const [following, setFollowing] = useState(true);
  const offered = !!api?.offers.operations;
  const opId = id ? decodeURIComponent(id) : null;
  const op = useOperation(offered ? api : undefined, opId);
  const output = useRunOutput(offered ? api : undefined, op.operation);
  return (
    <>
      <PageHeader title="Operation" back={{ label: "Back", onClick: () => navigate(-1) }} />
      {!offered ? (
        <EmptyState title="Not available">This server does not offer scrub or recovery operations yet.</EmptyState>
      ) : (
        <div data-assurance="" data-testid="operation-page" style={{ display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
          {op.missing ? (
            <div className="aop" data-testid="operation-none" role="status">
              <strong>Nothing is on record for this operation.</strong>
              <span>A run&rsquo;s lines are kept for 30 days, so this one&rsquo;s were pruned, or the id isn&rsquo;t one this deployment ran. Its result, if it had one, is on the chip it fed.</span>
            </div>
          ) : (
            <OperationPanel
              operation={op.operation}
              events={op.events}
              error={op.error}
              onCancel={() => void op.cancel()}
              cancelError={op.cancelError}
              readOnly={readOnly}
              detailLink={false}
              following={following}
              onFollowing={setFollowing}
            />
          )}
          {output ? <ValidatorLog result={output} /> : null}
          {opId ? <CommandEcho label="Read it from the terminal" commands={[assuranceShowCommand(opId)]} /> : null}
        </div>
      )}
    </>
  );
}
