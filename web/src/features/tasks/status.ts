import type { Attention, Phase, Status } from "@upmo-ai/components";

import type { Task } from "./types";

type TaskState = Pick<Task, "status" | "phase">;

/** The lifecycle step a task is on, or its outcome once it has finished. */
export function taskPhase(task: TaskState): Phase {
  if (task.status === "Done" || task.status === "Failed") return task.status;
  return task.phase;
}

/**
 * What the task wants from the person. Needs approval is a connector call that
 * waits for a decision. Paused only applies while the task is underway.
 */
export function taskAttention(task: TaskState, paused = false): Attention | null {
  if (task.status === "Needs approval") return "Needs approval";
  if (paused && task.status === "Working") return "Paused";
  return null;
}

/** The one word a badge shows: the attention when there is one, else the phase. */
export function taskStatus(task: TaskState, paused = false): Status {
  return taskAttention(task, paused) ?? taskPhase(task);
}
