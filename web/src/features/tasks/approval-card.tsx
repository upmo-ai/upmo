import { VisuallyHidden } from "@mantine/core";
import { ApprovalCard as UpmoApprovalCard } from "@upmo-ai/components";
import { useEffect, useRef, useState } from "react";

import {
  APPROVALS_KEY,
  TASKS_KEY,
  useApi,
  useWrite,
  type Approval,
} from "../../app/api";

export function ApprovalCard({ approval }: { approval: Approval }) {
  const api = useApi();
  const approve = useWrite(
    () => api.approvals.approve(approval.id),
    [APPROVALS_KEY, TASKS_KEY],
  );
  const deny = useWrite(
    () => api.approvals.deny(approval.id),
    [APPROVALS_KEY, TASKS_KEY],
  );

  return (
    <UpmoApprovalCard
      action={approval.action}
      busy={approve.isPending || deny.isPending}
      onApprove={() => approve.mutate()}
      onDeny={() => deny.mutate()}
    />
  );
}

/**
 * Announces approvals that arrive after the first render. The cards are
 * labelled groups, not alerts, so screen readers hear new ones from here.
 * Pass undefined until the approvals have loaded.
 */
export function ApprovalAnnouncer({ pending }: { pending: Approval[] | undefined }) {
  const seen = useRef<Set<string> | null>(null);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (!pending) return;
    const previous = seen.current;
    seen.current = new Set(pending.map((item) => item.id));
    if (!previous) return;
    const fresh = pending.filter((item) => !previous.has(item.id));
    if (fresh.length > 0) {
      setMessage(fresh.map((item) => `Approval needed: ${item.action}`).join(". "));
    }
  }, [pending]);

  return (
    <VisuallyHidden role="status" aria-live="polite" aria-atomic="true">
      {message}
    </VisuallyHidden>
  );
}
