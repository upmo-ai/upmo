export const phases = ["Preparing", "Planning", "Working", "Checking"] as const;

export type Phase = (typeof phases)[number];

export const taskStatuses = ["Working", "Needs approval", "Done", "Failed"] as const;

export type TaskStatus = (typeof taskStatuses)[number];

export interface Subtask {
  id: string;
  title: string;
  status: TaskStatus;
  children?: Subtask[];
}

export interface TaskArtifact {
  id: string;
  name: string;
  kind: "markdown" | "csv";
  size: string;
  time: string;
  content: string;
}

export interface TaskEvent {
  id: string;
  time: string;
  phase: Phase;
  message: string;
  type?: string;
  detail?: string[];
}

export interface TaskRunHistory {
  run: number;
  status: TaskStatus;
}

export interface Task {
  id: string;
  title: string;
  status: TaskStatus;
  phase: Phase;
  result: string;
  detail: string;
  subtasks?: Subtask[];
  cost?: string;
  tokens?: number;
  spendLimit?: string;
  pausePercent?: number;
  runNumber?: number;
  model?: string;
  engine?: string;
  startedAt?: string;
  deliveryChannel?: string;
  deliveryWhen?: string;
  sources?: string[];
  runHistory?: TaskRunHistory[];
  artifacts?: TaskArtifact[];
  events?: TaskEvent[];
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
