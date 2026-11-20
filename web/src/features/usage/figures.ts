import type { UsageBucket, UsageTask } from "../../app/api";

export function totalRequests(buckets: UsageBucket[]): number {
  return buckets.reduce((sum, bucket) => sum + bucket.requests, 0);
}

export function totalErrors(buckets: UsageBucket[]): number {
  return buckets.reduce((sum, bucket) => sum + bucket.errors, 0);
}

export function errorRate(requests: number, errors: number): number {
  return requests === 0 ? 0 : errors / requests;
}

export function totalInput(buckets: UsageBucket[]): number {
  return buckets.reduce((sum, bucket) => sum + bucket.input, 0);
}

export function totalOutput(buckets: UsageBucket[]): number {
  return buckets.reduce((sum, bucket) => sum + bucket.output, 0);
}

export function totalCacheRead(buckets: UsageBucket[]): number {
  return buckets.reduce((sum, bucket) => sum + bucket.cache_read, 0);
}

export function taskTokens(task: Pick<UsageTask, "input" | "output" | "cache_read" | "cache_write">): number {
  return task.input + task.output + task.cache_read + task.cache_write;
}

export function cacheHitRate(input: number, cacheRead: number): number {
  const denom = input + cacheRead;
  if (denom === 0) {
    return 0;
  }
  return cacheRead / denom;
}

export function periodCacheHit(buckets: UsageBucket[]): number {
  return cacheHitRate(totalInput(buckets), totalCacheRead(buckets));
}

/** A range with nothing recorded in it: no calls, and no task that ran. */
export function isEmptyUsage(buckets: UsageBucket[], tasks: UsageTask[]): boolean {
  return totalRequests(buckets) === 0 && tasks.length === 0;
}

/** A task's share of the range's known spend, or null when either cost is unknown. */
export function taskShare(task: UsageTask, spend: number | null): number | null {
  if (task.cost_micro_usd === null || spend === null || spend <= 0) {
    return null;
  }
  return task.cost_micro_usd / spend;
}

export type TaskSortKey = "cost" | "tokens" | "share";
export type SortDirection = "asc" | "desc";

/**
 * Tasks ordered by cost, total tokens or share of spend. A task with unknown
 * cost has nothing to compare, so it goes last in either direction. Ties keep
 * the order the API sent them in, also in either direction.
 */
export function sortTasks(
  tasks: UsageTask[],
  key: TaskSortKey,
  direction: SortDirection,
  spend: number | null,
): UsageTask[] {
  const value = (task: UsageTask): number | null => {
    if (key === "tokens") {
      return taskTokens(task);
    }
    return key === "share" ? taskShare(task, spend) : task.cost_micro_usd;
  };
  const sign = direction === "asc" ? 1 : -1;
  return tasks
    .map((task, index) => ({ task, index, value: value(task) }))
    .sort((a, b) => {
      if (a.value === null || b.value === null) {
        return a.value === b.value ? a.index - b.index : a.value === null ? 1 : -1;
      }
      return sign * (a.value - b.value) || a.index - b.index;
    })
    .map((item) => item.task);
}

/** The tasks that cost the most, biggest first, then tasks with unknown cost. */
export function costliestTasks(tasks: UsageTask[], limit: number): UsageTask[] {
  return sortTasks(tasks, "cost", "desc", null).slice(0, limit);
}

export type LimitState = "ok" | "near" | "over";

/** Where a limit bar turns to its warning, as a share of the limit. */
export const limitWarnPercent = 80;

/**
 * How much of a monthly limit is used. The percent is for the bar only. The
 * state is decided on whole micro-USD, so 80% of the limit is "near" exactly
 * and not a rounding away. A limit of 0 allows nothing: any spend is over it.
 */
export function limitUse(spent: number, limit: number): { percent: number; state: LimitState } {
  const state: LimitState =
    spent >= limit && (limit > 0 || spent > 0)
      ? "over"
      : spent * 100 >= limit * limitWarnPercent && limit > 0
        ? "near"
        : "ok";
  const percent = limit <= 0 ? (spent > 0 ? 100 : 0) : Math.min(100, (spent / limit) * 100);
  return { percent, state };
}

const sourceLabels: Record<string, string> = {
  chat: "Chat",
  task: "Tasks",
  automation: "Automations",
  knowledge: "Knowledge",
  memory: "Memory",
  system: "System",
};

/** A kind of work as a person reads it. A kind the page does not know is shown as the server named it. */
export function sourceLabel(source: string): string {
  return sourceLabels[source] ?? source;
}
