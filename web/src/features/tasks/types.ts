export const phases = ["Preparing", "Planning", "Working", "Checking"] as const;

export type Phase = (typeof phases)[number];

export const taskStatuses = ["Working", "Needs approval", "Done", "Failed"] as const;

export type TaskStatus = (typeof taskStatuses)[number];

export interface Task {
  id: string;
  title: string;
  status: TaskStatus;
  phase: Phase;
  result: string;
  detail: string;
}

export const pendingResult = "The final result shows up here when the task is done.";

export type TaskFilter = "All" | TaskStatus;

export function isTaskFilter(value: string): value is TaskFilter {
  return (
    value === "All" ||
    value === "Working" ||
    value === "Needs approval" ||
    value === "Done" ||
    value === "Failed"
  );
}
