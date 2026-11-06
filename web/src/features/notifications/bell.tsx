import { VisuallyHidden } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button, EmptyState, Icon, NotificationBell, NotificationItem } from "@upmo-ai/components";
import type { IconConcept } from "@upmo-ai/icons";
import { useEffect, useMemo, useRef, useState } from "react";

import {
  approvalsQueryKey,
  notificationsQueryKey,
  tasksQueryKey,
  useApi,
} from "../../app/api";
import { NOTIFICATION_LIMIT, type AppNotification, type NotificationKind } from "./types";

const kindOrder = {
  approval: 0,
  question: 1,
  proposal: 2,
  finished: 3,
  failed: 4,
} as const satisfies Record<NotificationKind, number>;

const kindCopy = {
  approval: { label: "Needs your approval", icon: "status.needs_approval" },
  question: { label: "Question", icon: "status.help" },
  proposal: { label: "Proposal", icon: "object.task" },
  finished: { label: "Finished", icon: "status.done" },
  failed: { label: "Failed", icon: "status.failed" },
} as const satisfies Record<NotificationKind, { label: string; icon: IconConcept }>;

// How long the empty-state announcement stays in the status region.
const ANNOUNCEMENT_CLEAR_MS = 2000;

const upToDate = {
  title: "You are up to date",
  description: "Approvals, proposals and finished tasks show up here.",
} as const;

function ordered(items: AppNotification[]) {
  return [...items].sort((left, right) => kindOrder[left.kind] - kindOrder[right.kind]);
}

export function NotificationsBell() {
  const api = useApi();
  const queryClient = useQueryClient();
  // One more than is shown, so a full page tells the bell that more exist.
  const notifications = useQuery({
    queryKey: notificationsQueryKey(),
    queryFn: () => api.listNotifications({ limit: NOTIFICATION_LIMIT + 1 }),
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
  // The row whose button was pressed, and where it sat in the list. Set on
  // press, cleared once focus has been placed.
  const pressed = useRef<{ id: string; index: number } | null>(null);
  // The action button of each listed row, by row id. Focus is placed from
  // here, not by searching the document, so nothing outside the list is hit.
  const actions = useRef(new Map<string, HTMLButtonElement>());
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
  const dismissNote = useMutation({
    mutationFn: (id: string) => api.dismissNotification(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationsQueryKey() });
    },
    onError: () => {
      pressed.current = null;
    },
  });

  const ready = notifications.data !== undefined && approvals.data !== undefined;
  const more = (notifications.data?.length ?? 0) > NOTIFICATION_LIMIT;
  const pendingApprovals = useMemo(
    () => (approvals.data ?? []).filter((item) => item.state === "pending"),
    [approvals.data],
  );
  const items = useMemo(() => {
    if (notifications.data === undefined || approvals.data === undefined) return [];
    const approvalItems: AppNotification[] = pendingApprovals.map((item) => ({
      id: item.id,
      kind: "approval",
      title: item.title,
      taskId: item.taskId,
    }));
    return ordered([...approvalItems, ...notifications.data.slice(0, NOTIFICATION_LIMIT)]);
  }, [notifications.data, approvals.data, pendingApprovals]);
  const count = items.length;
  const pending = approveApproval.isPending || dismissNote.isPending;

  useEffect(() => () => clearTimeout(clearStatus.current), []);

  // The pressed button leaves the list with its row, which would drop focus to
  // the body. Land on the button now at that position (the first one when the
  // last row was pressed), or on the bell once nothing is left. Wait until the
  // row is gone and the buttons are enabled again. When that leaves the list
  // empty, say so, once, and clear the line after a moment so it does not
  // linger in the reading order: the popover is closed by then and the empty
  // state is not a live region.
  useEffect(() => {
    const from = pressed.current;
    if (!from || !ready || pending || items.some((item) => item.id === from.id)) return;
    pressed.current = null;
    if (count === 0) {
      root.current?.querySelector("button")?.focus();
      const region = status.current;
      if (region) {
        region.textContent = upToDate.title;
        clearTimeout(clearStatus.current);
        clearStatus.current = setTimeout(() => {
          region.textContent = "";
        }, ANNOUNCEMENT_CLEAR_MS);
      }
      return;
    }
    const target = items[from.index < count ? from.index : 0];
    if (target) actions.current.get(target.id)?.focus();
  }, [ready, pending, items, count]);

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
        {items.map((item, index) => {
          const copy = kindCopy[item.kind];
          const isApproval = item.kind === "approval";
          return (
            <NotificationItem
              key={item.id}
              unread
              icon={<Icon concept={copy.icon} size={16} />}
              title={
                item.taskId ? (
                  <Link
                    to="/tasks/$taskId"
                    params={{ taskId: item.taskId }}
                    className="shell-bell-link"
                    onClick={() => setOpened(false)}
                  >
                    {item.title}
                  </Link>
                ) : (
                  item.title
                )
              }
              meta={copy.label}
              action={
                <Button
                  size="xs"
                  variant={isApproval ? "light" : "subtle"}
                  disabled={pending}
                  ref={(node: HTMLButtonElement | null) => {
                    if (node) actions.current.set(item.id, node);
                    else actions.current.delete(item.id);
                  }}
                  onClick={() => {
                    pressed.current = { id: item.id, index };
                    // Settling the last row closes the popover, so its focus
                    // trap lets go once focus is on the bell.
                    const settle = {
                      onSuccess: () => {
                        if (count === 1) setOpened(false);
                      },
                    };
                    if (isApproval) approveApproval.mutate(item.id, settle);
                    else dismissNote.mutate(item.id, settle);
                  }}
                >
                  {isApproval ? "Approve" : "Dismiss"}
                </Button>
              }
            />
          );
        })}
        {more ? (
          <li className="shell-bell-more">
            {pendingApprovals.length > 0
              ? `Showing the latest ${NOTIFICATION_LIMIT} notifications, besides approvals`
              : `Showing the latest ${NOTIFICATION_LIMIT} notifications`}
          </li>
        ) : null}
      </NotificationBell>
      <VisuallyHidden ref={status} role="status" />
    </span>
  );
}
