import { Popover } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge, Button, EmptyState, IconButton } from "@upmo-ai/components";
import { useEffect, useRef } from "react";

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
  const bell = useRef<HTMLElement>(null);
  const list = useRef<HTMLUListElement>(null);
  // Where the pressed Approve button sat among the approvable rows, and how
  // many rows there were. Set on press, cleared once focus has been placed.
  const pressed = useRef<{ index: number; actionable: number } | null>(null);
  const approveApproval = useMutation({
    mutationFn: (id: string) => api.approvals.approve(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: approvalsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: tasksQueryKey() });
    },
    onError: () => {
      pressed.current = null;
    },
  });
  const approveNote = useMutation({
    mutationFn: (id: string) => api.approveNotification(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationsQueryKey() });
    },
    onError: () => {
      pressed.current = null;
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
  const actionable = items.filter((item) => needsApproval(item.kind)).length;

  // The pressed Approve button leaves the list, which would drop focus to the
  // body. Land on the next Approve button (the first one when the last row was
  // pressed), or on the bell once nothing is left to approve. Wait until the
  // row is gone and the buttons are enabled again.
  useEffect(() => {
    const from = pressed.current;
    if (!from || !ready || pending || actionable >= from.actionable) return;
    pressed.current = null;
    if (actionable === 0) {
      bell.current?.focus();
      return;
    }
    const buttons = list.current?.querySelectorAll("button");
    buttons?.[from.index < actionable ? from.index : 0]?.focus();
  }, [ready, pending, actionable]);

  return (
    <Popover position="bottom-end" width={320} shadow="md">
      <Popover.Target ref={bell}>
        <IconButton className="shell-bell" aria-label={label} variant="subtle" size={40}>
          <ShellIcon concept="nav.notifications" />
          {count > 0 ? (
            <Badge className="shell-count" size="xs" circle aria-hidden>
              {count}
            </Badge>
          ) : null}
        </IconButton>
      </Popover.Target>
      <Popover.Dropdown>
        <div role="status">
          {ready && items.length === 0 ? (
            <EmptyState
              className="shell-empty"
              title="You are up to date"
              description="Approvals and finished tasks show up here."
            />
          ) : null}
        </div>
        {ready && items.length === 0 ? null : (
          <ul ref={list} className="shell-notes" aria-label="Notifications">
            {items.map((item) => (
              <li key={item.id} className="shell-note">
                <span>{item.title}</span>
                {needsApproval(item.kind) ? (
                  <Button
                    size="xs"
                    variant="light"
                    disabled={pending}
                    onClick={() => {
                      pressed.current = {
                        index: items.filter((entry) => needsApproval(entry.kind)).indexOf(item),
                        actionable,
                      };
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
