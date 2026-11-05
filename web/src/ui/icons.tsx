import { Icon, IconButton } from "@upmo-ai/components";
import type { IconConcept } from "@upmo-ai/icons";

export const shellConcepts = [
  "nav.home",
  "nav.chats",
  "nav.tasks",
  "nav.automations",
  "nav.agents",
  "nav.knowledge",
  "nav.usage",
  "nav.settings",
  "nav.sidebar",
  "action.edit",
  "action.delete",
  "action.remove",
  "action.add",
  "action.copy",
  "action.more",
  "action.send",
  "object.folder",
  "object.source",
  "object.automation",
  "object.agent",
] as const satisfies readonly IconConcept[];

export type ShellConcept = (typeof shellConcepts)[number];

export function CaretIcon({
  size = 20,
  strokeWidth = 10,
  className,
}: {
  size?: number;
  strokeWidth?: number;
  className?: string;
}) {
  return (
    <svg
      viewBox="0 0 64 64"
      width={size}
      height={size}
      fill="none"
      className={className}
      aria-hidden="true"
    >
      <path
        d="M10 44 32 22l22 22"
        stroke="currentColor"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function ShellIcon({
  concept,
  size = 20,
}: {
  concept: ShellConcept;
  size?: 16 | 20 | 24 | 28;
}) {
  return <Icon concept={concept} size={size} />;
}

export function RowAction({
  label,
  concept,
  onClick,
  disabled = false,
  size = 40,
  className,
}: {
  label: string;
  concept: ShellConcept;
  onClick?: () => void;
  disabled?: boolean;
  size?: number;
  className?: string;
}) {
  return (
    <IconButton
      aria-label={label}
      className={className}
      variant="subtle"
      size={size}
      disabled={disabled}
      onClick={onClick}
    >
      <ShellIcon concept={concept} size={20} />
    </IconButton>
  );
}
