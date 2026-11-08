import { GroupedBarChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatUsd } from "./format";
import { taskCostGroups } from "./series";

export function TaskCostChart({ usage }: { usage: Usage }) {
  const groups = taskCostGroups(usage);
  return (
    <UsagePanel wide title="Cost per task" empty={groups.length === 0 ? "No task costs in this range." : undefined}>
      <GroupedBarChart title="Cost per task" orientation="horizontal" groups={groups} formatValue={formatUsd} />
    </UsagePanel>
  );
}
