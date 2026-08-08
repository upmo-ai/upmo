import { Alert, Button, Group, Text } from "@mantine/core";

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
    <Alert role="alert" variant="light">
      <Text>
        {approval.action}. Approve to continue, deny to let the agent find another way.
      </Text>
      <Group gap="xs" mt="sm">
        <Button size="sm" disabled={busy} onClick={() => approve.mutate()}>
          Approve
        </Button>
        <Button size="sm" variant="default" disabled={busy} onClick={() => deny.mutate()}>
          Deny
        </Button>
      </Group>
    </Alert>
  );
}
