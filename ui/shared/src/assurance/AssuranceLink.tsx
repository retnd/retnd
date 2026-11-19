/**
 * A link to somewhere in the app, that is also safe where no router is
 * mounted (a component test that renders one chip) and where the link sits
 * inside something that is itself clickable (a Backups table row, which
 * navigates to the backup on any click).
 */
import { Link, useInRouterContext } from "react-router-dom";
import type { ReactNode } from "react";

export function AssuranceLink({ to, children, testId, label }: { to: string; children: ReactNode; testId?: string; label?: string }) {
  const inRouter = useInRouterContext();
  const stop = (e: { stopPropagation(): void }) => e.stopPropagation();
  return inRouter
    ? <Link to={to} onClick={stop} className="alink" data-testid={testId} aria-label={label}>{children}</Link>
    : <a href={to} onClick={stop} className="alink" data-testid={testId} aria-label={label}>{children}</a>;
}
