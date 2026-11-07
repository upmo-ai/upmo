import { Group, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";
import { Panel, SegmentedControl } from "@upmo-ai/components";

interface AnalyticsPanelProps {
  title: string;
  /** When set, the panel shows this text instead of its chart. */
  empty?: string;
  /** Sits opposite the heading, such as a view toggle. */
  action?: ReactNode;
  /** Shown under the chart, and under the empty text when there is no chart. */
  note?: ReactNode;
  /** Spans the whole row of the panel grid instead of half of it. */
  wide?: boolean;
  children: ReactNode;
}

/** A titled panel for one analytics chart, with a plain empty state for a range with no data. */
export function AnalyticsPanel({ title, empty, action, note, wide = false, children }: AnalyticsPanelProps) {
  return (
    <Panel p="md" className={wide ? "usage-panel usage-wide" : "usage-panel"}>
      <Group justify="space-between" align="center" wrap="wrap" mb="sm">
        <Title order={2} size="h4">
          {title}
        </Title>
        {empty === undefined ? action : null}
      </Group>
      {empty === undefined ? children : <Text c="dimmed">{empty}</Text>}
      {note}
    </Panel>
  );
}

const viewOptions = [
  { label: "Lines", value: "lines" },
  { label: "Stacked", value: "stacked" },
];

/** Lines or stacked areas for a multi-series chart. */
export function StackToggle({
  label,
  stacked,
  onChange,
}: {
  label: string;
  stacked: boolean;
  onChange: (stacked: boolean) => void;
}) {
  return (
    <SegmentedControl
      aria-label={label}
      size="xs"
      value={stacked ? "stacked" : "lines"}
      onChange={(value) => onChange(value === "stacked")}
      data={viewOptions}
    />
  );
}
