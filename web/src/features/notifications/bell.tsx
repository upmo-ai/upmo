import { VisuallyHidden } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, EmptyState, NotificationBell, NotificationItem } from "@upmo-ai/components";
import { useEffect, useRef, useState } from "react";

import {
  approvalsQueryKey,
  notificationsQueryKey,
  tasksQueryKey,
  useApi,
} from "../../app/api";
import type { AppNotification, NotificationKind } from "./types";

const kindOrder = {
  approval: 0,
  question: 1,
  finished: 2,
  failed: 3,
} as const satisfies Record<NotificationKind, number>;

// How long the empty-state announcement stays in the status region.
const ANNOUNCEMENT_CLEAR_MS = 2000;

const upToDate = {
  title: "You are up to date",
  description: "Approvals and finished tasks show up here.",
} as const;

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
  // The popover traps focus while it is open, and that trap swallows Tab when
  // the dialog has nothing left to tab to. So it is held here and closed when
  // focus moves to the bell.
  const [opened, setOpened] = useState(false);
  // Holds the bell button, which NotificationBell does not hand out a ref to.
  const root = useRef<HTMLSpanElement>(null);
  // The live region and the timer that empties it. Written directly: the line
  // is a message to assistive tech, not something the page renders.
  const status = useRef<HTMLDivElement>(null);
  const clearStatus = useRef<ReturnType<typeof setTimeout>>(undefined);
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
  const pending = approveApproval.isPending || approveNote.isPending;
  const actionable = items.filter((item) => needsApproval(item.kind)).length;

  useEffect(() => () => clearTimeout(clearStatus.current), []);

  // The pressed Approve button leaves the list, which would drop focus to the
  // body. Land on the next Approve button (the first one when the last row was
  // pressed), or on the bell once nothing is left to approve. Wait until the
  // row is gone and the buttons are enabled again. When that leaves the list
  // empty, say so, once, and clear the line after a moment so it does not
  // linger in the reading order: the popover is closed by then and the empty
  // state is not a live region.
  useEffect(() => {
    const from = pressed.current;
    if (!from || !ready || pending || actionable >= from.actionable) return;
    pressed.current = null;
    if (actionable === 0) {
      root.current?.querySelector("button")?.focus();
      const region = status.current;
      if (count === 0 && region) {
        region.textContent = upToDate.title;
        clearTimeout(clearStatus.current);
        clearStatus.current = setTimeout(() => {
          region.textContent = "";
        }, ANNOUNCEMENT_CLEAR_MS);
      }
      return;
    }
    const buttons = document.querySelectorAll<HTMLElement>("[data-notification-approve]");
    buttons[from.index < actionable ? from.index : 0]?.focus();
  }, [ready, pending, actionable, count]);

  return (
    <span ref={root} className="shell-bell">
      <NotificationBell
        count={count}
        opened={opened}
        onOpenedChange={setOpened}
        busy={notifications.isPending || approvals.isPending}
        empty={
          ready ? (
            <EmptyState
              compact
              headingLevel={3}
              title={upToDate.title}
              description={upToDate.description}
            />
          ) : (
            false
          )
        }
      >
        {items.map((item) => (
          <NotificationItem
            key={item.id}
            unread
            title={item.title}
            action={
              needsApproval(item.kind) ? (
                <Button
                  size="xs"
                  variant="light"
                  disabled={pending}
                  data-notification-approve
                  onClick={() => {
                    pressed.current = {
                      index: items.filter((entry) => needsApproval(entry.kind)).indexOf(item),
                      actionable,
                    };
                    // Approving the last row closes the popover, so its focus
                    // trap lets go once focus is on the bell.
                    const settle = {
                      onSuccess: () => {
                        if (actionable === 1) setOpened(false);
                      },
                    };
                    if (item.kind === "approval") approveApproval.mutate(item.id, settle);
                    else approveNote.mutate(item.id, settle);
                  }}
                >
                  Approve
                </Button>
              ) : undefined
            }
          />
        ))}
      </NotificationBell>
      <VisuallyHidden ref={status} role="status" />
    </span>
  );
}
