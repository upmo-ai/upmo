import { Group } from "@mantine/core";
import { IconAlertCircle } from "@tabler/icons-react";
import { Button } from "@upmo-ai/components";

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
  const busy = approve.isPending || deny.isPending;

  return (
    <div className="task-approval-alert" role="alert">
      <span className="task-approval-icon" aria-hidden="true">
        <IconAlertCircle size={16} stroke={2} />
      </span>
      <div className="task-approval-body">
        <div className="task-approval-title">
          {approval.action}. Approve to continue, deny to let the agent find another way.
        </div>
        <Group gap="xs" mt="xs">
          <Button size="sm" disabled={busy} onClick={() => approve.mutate()}>
            Approve
          </Button>
          <Button size="sm" variant="default" disabled={busy} onClick={() => deny.mutate()}>
            Deny
          </Button>
        </Group>
      </div>
    </div>
  );
}
