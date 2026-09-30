import { ActionIcon, Tooltip } from "@mantine/core";
import {
  IconBell,
  IconBooks,
  IconChartBar,
  IconClockPlay,
  IconCopy,
  IconDots,
  IconFileSearch,
  IconFolder,
  IconHome,
  IconLayoutSidebar,
  IconListCheck,
  IconMessage,
  IconPencil,
  IconPlus,
  IconSettings,
  IconTrash,
  IconUserHexagon,
  IconUsers,
  IconX,
} from "@tabler/icons-react";
import { icons, type IconConcept } from "@upmo-ai/icons";

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
] as const satisfies readonly IconConcept[];

export type ShellConcept = (typeof shellConcepts)[number];

const byName = {
  home: IconHome,
  message: IconMessage,
  "list-check": IconListCheck,
  "clock-play": IconClockPlay,
  users: IconUsers,
  books: IconBooks,
  "chart-bar": IconChartBar,
  settings: IconSettings,
  "layout-sidebar": IconLayoutSidebar,
  bell: IconBell,
  pencil: IconPencil,
  plus: IconPlus,
  trash: IconTrash,
  folder: IconFolder,
  copy: IconCopy,
  dots: IconDots,
  x: IconX,
  "file-search": IconFileSearch,
  "user-hexagon": IconUserHexagon,
} as const;

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
  const iconName = icons[concept];
  if (iconName === "caret") {
    return <CaretIcon size={size} />;
  }
  const Icon = byName[iconName as keyof typeof byName];
  if (!Icon) return null;
  return <Icon size={size} stroke={1.75} aria-hidden="true" />;
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
    <Tooltip label={label} position="bottom" openDelay={400}>
      <ActionIcon
        aria-label={label}
        className={className}
        variant="subtle"
        size={size}
        disabled={disabled}
        onClick={onClick}
      >
        <ShellIcon concept={concept} size={20} />
      </ActionIcon>
    </Tooltip>
  );
}

