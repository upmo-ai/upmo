import { useQuery } from "@tanstack/react-query";
import { LivenessIndicator } from "@upmo-ai/components";

import { tasksQueryKey, useApi } from "../../app/api";

/** The shell's count of underway tasks that want nothing from anyone, in any phase. */
export function TasksLiveness() {
  const api = useApi();
  const tasks = useQuery({
    queryKey: tasksQueryKey(),
    queryFn: () => api.listTasks(),
  });
  const count = (tasks.data ?? []).filter((task) => task.status === "Working").length;
  const label = count === 1 ? "1 task underway" : count > 0 ? `${count} tasks underway` : "No tasks underway";

  return <LivenessIndicator count={count} label={label} />;
}
