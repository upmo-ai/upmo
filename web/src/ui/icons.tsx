import { IconFileSearch } from "@tabler/icons-react";
import { Icon, IconButton } from "@upmo-ai/components";
import { stroke, type IconConcept } from "@upmo-ai/icons";

// Concepts the design-system map no longer carries, kept here until upstream
// adds them back.
const localConcepts = {
  "object.source": IconFileSearch,
} as const;

type LocalConcept = keyof typeof localConcepts;

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
  "nav.notifications",
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
] as const satisfies readonly (IconConcept | LocalConcept)[];

export type ShellConcept = (typeof shellConcepts)[number];

function isLocal(concept: ShellConcept): concept is LocalConcept {
  return concept in localConcepts;
}

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
  if (isLocal(concept)) {
    const Local = localConcepts[concept];
    return <Local size={size} stroke={stroke} aria-hidden="true" />;
  }
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
