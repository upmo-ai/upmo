import { Alert, ThemeIcon } from "@mantine/core";
import {
  IconAlertCircle,
  IconCheck,
  IconClock,
  IconInfoCircle,
} from "@tabler/icons-react";
import type { ReactNode } from "react";

export interface StatusAlertProps {
  tone?: "info" | "success" | "warning" | "danger";
  title?: string;
  children: ReactNode;
  icon?: ReactNode;
  className?: string;
  role?: string;
}

export function StatusAlert({
  tone = "info",
  title,
  children,
  icon,
  className,
  role,
}: StatusAlertProps) {
  const edgeColor = {
    info: "var(--border-strong)",
    success: "var(--emerald-deep)",
    warning: "var(--warning)",
    danger: "var(--danger)",
  }[tone];

  const defaultIcon = {
    info: <IconInfoCircle size={14} stroke={1.75} />,
    success: <IconCheck size={14} stroke={1.75} />,
    warning: <IconClock size={14} stroke={1.75} />,
    danger: <IconAlertCircle size={14} stroke={1.75} />,
  }[tone];

  const themeIconColor = {
    info: "gray",
    success: "teal",
    warning: "yellow",
    danger: "red",
  }[tone];

  return (
    <Alert
      role={role}
      variant="default"
      radius="md"
      title={title}
      className={className}
      icon={
        <ThemeIcon variant="light" color={themeIconColor} radius="md" size={24}>
          {icon ?? defaultIcon}
        </ThemeIcon>
      }
      styles={{
        root: {
          position: "relative",
          background: "var(--surface-100)",
          border: "1px solid var(--border)",
          borderLeft: `3px solid ${edgeColor}`,
          borderRadius: "var(--radius-md)",
          padding: "var(--space-2) var(--space-3)",
        },
        title: {
          fontWeight: 600,
          fontSize: "13px",
          color: "var(--ink)",
        },
        message: {
          color: "var(--ink-secondary, var(--ink-muted))",
          fontSize: "13px",
          lineHeight: "1.4",
        },
      }}
    >
      {children}
    </Alert>
  );
}
