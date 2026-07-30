import { Alert, Button, Group, Text } from "@mantine/core";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { approvalsQueryKey, tasksQueryKey, useApi, type Approval } from "../../app/api";

export function ApprovalCard({ approval }: { approval: Approval }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: approvalsQueryKey() });
    void queryClient.invalidateQueries({ queryKey: tasksQueryKey() });
  };
  const approve = useMutation({
    mutationFn: () => api.approvals.approve(approval.id),
    onSuccess: refresh,
  });
  const deny = useMutation({
    mutationFn: () => api.approvals.deny(approval.id),
    onSuccess: refresh,
  });
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
