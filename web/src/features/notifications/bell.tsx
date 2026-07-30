import { ActionIcon, Badge, Button, Popover, Text } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  approvalsQueryKey,
  notificationsQueryKey,
  tasksQueryKey,
  useApi,
} from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import type { AppNotification, NotificationKind } from "./types";

const kindOrder = {
  approval: 0,
  question: 1,
  finished: 2,
  failed: 3,
} as const satisfies Record<NotificationKind, number>;

function ordered(items: AppNotification[]) {
  return [...items].sort((left, right) => kindOrder[left.kind] - kindOrder[right.kind]);
}

function needsApproval(kind: NotificationKind) {
  return kind === "approval" || kind === "question";
}

export function NotificationsBell() {
  const api = useApi();
  const queryClient = useQueryClient();
  const notifications = useQuery({
    queryKey: notificationsQueryKey(),
    queryFn: () => api.listNotifications(),
  });
  const approvals = useQuery({
    queryKey: approvalsQueryKey(),
    queryFn: () => api.approvals.list(),
  });
  const approveApproval = useMutation({
    mutationFn: (id: string) => api.approvals.approve(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: approvalsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: tasksQueryKey() });
    },
  });
  const approveNote = useMutation({
    mutationFn: (id: string) => api.approveNotification(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationsQueryKey() });
    },
  });

  const ready = notifications.data !== undefined && approvals.data !== undefined;
  const approvalItems: AppNotification[] = (approvals.data ?? [])
    .filter((item) => item.state === "pending")
    .map((item) => ({ id: item.id, kind: "approval", title: item.title }));
  const items = ready ? ordered([...approvalItems, ...(notifications.data ?? [])]) : [];
  const count = items.length;
  const label = count > 0 ? `Notifications, ${count} unread` : "Notifications";
  const pending = approveApproval.isPending || approveNote.isPending;

  return (
    <Popover position="bottom-end" width={320} shadow="md">
      <Popover.Target>
        <ActionIcon className="shell-bell" aria-label={label} variant="subtle" size={40}>
          <ShellIcon concept="nav.notifications" />
          {count > 0 ? (
            <Badge className="shell-count" size="xs" circle aria-hidden>
              {count}
            </Badge>
          ) : null}
        </ActionIcon>
      </Popover.Target>
      <Popover.Dropdown>
        {ready && items.length === 0 ? (
          <>
            <Text fw={600}>You are up to date</Text>
            <Text c="dimmed" size="sm">
              Approvals and finished tasks show up here.
            </Text>
          </>
        ) : (
          <ul className="shell-notes" aria-label="Notifications">
            {items.map((item) => (
              <li key={item.id} className="shell-note">
                <span>{item.title}</span>
                {needsApproval(item.kind) ? (
                  <Button
                    size="xs"
                    variant="light"
                    disabled={pending}
                    onClick={() => {
                      if (item.kind === "approval") approveApproval.mutate(item.id);
                      else approveNote.mutate(item.id);
                    }}
                  >
                    Approve
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Popover.Dropdown>
    </Popover>
  );
}
