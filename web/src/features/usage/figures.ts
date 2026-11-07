import type { UsageBucket, UsageTask } from "../../app/api";

/** Total of the buckets whose cost is known. A bucket with unknown cost adds nothing here and is never shown as 0. */
export function knownSpend(buckets: UsageBucket[]): number {
  const total = buckets.reduce((sum, bucket) => sum + (bucket.spend_usd ?? 0), 0);
  return Math.round(total * 10_000) / 10_000;
}

/** Spend for the range, or null when no bucket has a known cost, so the total is shown as Unknown rather than 0. */
export function periodSpend(buckets: UsageBucket[]): number | null {
  if (buckets.every((bucket) => bucket.spend_usd === null)) {
    return null;
  }
  return knownSpend(buckets);
}

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
  if (task.cost_usd === null || spend === null || spend <= 0) {
    return null;
  }
  return task.cost_usd / spend;
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
    return key === "share" ? taskShare(task, spend) : task.cost_usd;
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
