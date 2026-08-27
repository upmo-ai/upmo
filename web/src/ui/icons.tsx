import {
  IconBell,
  IconBooks,
  IconChartBar,
  IconClockPlay,
  IconHome,
  IconLayoutSidebar,
  IconListCheck,
  IconMessage,
  IconPencil,
  IconSettings,
  IconTrash,
  IconUsers,
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
  trash: IconTrash,
} as const;

export function ShellIcon({ concept }: { concept: ShellConcept }) {
  const Icon = byName[icons[concept]];
  return <Icon size={20} stroke={1.75} aria-hidden />;
}
