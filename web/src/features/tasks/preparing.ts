import type {
  PrepareStep,
  PrepareStepName,
  PrepareStepState,
  Preparing,
  RuntimeTier,
  Task,
} from "./types";

/** How often the task is read again while it is still Preparing. */
export const PREPARING_POLL_MS = 1000;

/** How often the task is read again while it is working but not preparing. */
export const WORKING_POLL_MS = 5_000;

/**
 * The refetch interval for the open task. The payload decides: the task is read
 * again while it says it is working and still preparing, and left alone after.
 */
export function preparingPollMs(tasks: Task[] | undefined, taskId: string): number | false {
  const task = tasks?.find((item) => item.id === taskId);
  return task && task.status === "Working" && task.phase === "Preparing" ? PREPARING_POLL_MS : false;
}

/**
 * The other polling case: while the page polls a task that says it is working
 * and no longer preparing, it is read again every five seconds, so a task the
 * stream has gone quiet on still moves.
 */
export function workingPollMs(tasks: Task[] | undefined, taskId: string): number | false {
  const task = tasks?.find((item) => item.id === taskId);
  return task && task.status === "Working" && task.phase !== "Preparing" ? WORKING_POLL_MS : false;
}

export const stepLabel: Record<PrepareStepName, string> = {
  clone: "Clone",
  toolchain: "Toolchain",
  probe: "Probe",
};

export const stepHint: Record<PrepareStepName, string> = {
  clone: "Fetch the repository",
  toolchain: "Check the tools the task needs",
  probe: "Check the workspace is ready",
};

export const stateLabel: Record<PrepareStepState, string> = {
  pending: "Waiting",
  running: "Running",
  done: "Done",
  failed: "Failed",
};

export const tierLabel: Record<RuntimeTier, string> = {
  container: "Container",
  gvisor: "gVisor",
  kata: "Kata",
};

function millis(stamp: string | null) {
  if (stamp === null) return null;
  const value = Date.parse(stamp);
  return Number.isNaN(value) ? null : value;
}

/** How long the step took, or null while it has not both started and finished. */
export function stepMillis(step: PrepareStep) {
  const start = millis(step.started_at);
  const end = millis(step.finished_at);
  return start === null || end === null || end < start ? null : end - start;
}

/**
 * From the first step starting to the last one finishing, or null when that is
 * not known. A step that has not finished leaves the total unknown, so a
 * partial span is never reported as the whole.
 */
export function totalMillis(preparing: Preparing) {
  const starts = preparing.steps.map((step) => millis(step.started_at));
  const ends = preparing.steps.map((step) => millis(step.finished_at));
  if (starts.length === 0 || starts.includes(null) || ends.includes(null)) return null;
  const span = Math.max(...(ends as number[])) - Math.min(...(starts as number[]));
  return span < 0 ? null : span;
}

export function formatDuration(ms: number) {
  const whole = Math.round(ms);
  if (whole < 1000) return `${whole} ms`;
  // Round first, then split, so a value that rounds up carries into the next unit.
  const tenths = Math.round(ms / 100) / 10;
  if (tenths < 60) return `${tenths.toFixed(1)} s`;
  const total = Math.round(ms / 1000);
  const minutes = Math.floor(total / 60);
  const rest = total % 60;
  return rest === 0 ? `${minutes} min` : `${minutes} min ${rest} s`;
}

export function formatCpu(cpu: number) {
  return cpu === 1 ? "1 CPU" : `${cpu} CPUs`;
}

export function formatMemory(megabytes: number) {
  return megabytes >= 1024 && megabytes % 1024 === 0 ? `${megabytes / 1024} GB` : `${megabytes} MB`;
}

export function formatTimeout(seconds: number) {
  return seconds >= 60 && seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`;
}

/** The block shows its steps while the task prepares, or whenever a step has failed. */
export function isPreparingOpen(task: Pick<Task, "phase">, preparing: Preparing) {
  return task.phase === "Preparing" || preparing.steps.some((step) => step.state === "failed");
}
