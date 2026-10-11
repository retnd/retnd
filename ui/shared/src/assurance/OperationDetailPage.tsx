/**
 * The operation detail view (issue #985): the live stage, the durable lines,
 * a Following toggle and Cancel, for one scrub or recovery test. It is the
 * same panel the set page mounts, so what a reload shows here is what the
 * set page showed.
 */
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { PageHeader } from "@shared/components/PageHeader";
import { EmptyState } from "@shared/components/EmptyState";
import { OperationPanel } from "./OperationPanel";
import { useOperation } from "./useOperation";

export function OperationDetailPage({ readOnly }: { readOnly: boolean }) {
  const { id } = useParams();
  const api = useApi().assurance;
  const navigate = useNavigate();
  const [following, setFollowing] = useState(true);
  const offered = !!api?.offers.operations;
  const op = useOperation(offered ? api : undefined, id ? decodeURIComponent(id) : null);
  return (
    <>
      <PageHeader title="Operation" back={{ label: "Back", onClick: () => navigate(-1) }} />
      {!offered ? (
        <EmptyState title="Not available">This server does not offer scrub or recovery operations yet.</EmptyState>
      ) : (
        <OperationPanel
          operation={op.operation}
          events={op.events}
          error={op.error}
          onCancel={() => void op.cancel()}
          readOnly={readOnly}
          detailLink={false}
          following={following}
          onFollowing={setFollowing}
        />
      )}
    </>
  );
}
