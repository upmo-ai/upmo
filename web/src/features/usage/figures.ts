import type { UsageDay, UsageTask } from "../../app/api";

/** Total of the days whose cost is known. A day with unknown cost adds nothing here and is never shown as 0. */
export function knownSpend(days: UsageDay[]): number {
  return days.reduce((sum, day) => sum + (day.spend_usd ?? 0), 0);
}

/** Spend for the period, or null when no day has a known cost, so the total is shown as Unknown rather than 0. */
export function periodSpend(days: UsageDay[]): number | null {
  if (days.every((day) => day.spend_usd === null)) {
    return null;
  }
  return knownSpend(days);
}

export function totalTokens(days: UsageDay[]): number {
  return days.reduce(
    (sum, day) => sum + day.input + day.output + day.cache_read + day.cache_write + day.reasoning,
    0,
  );
}

export function taskTokens(task: UsageTask): number {
  return task.input + task.output + task.cache_read + task.cache_write;
}

export function cacheHitRate(input: number, cacheRead: number): number {
  const denom = input + cacheRead;
  if (denom === 0) {
    return 0;
  }
  return cacheRead / denom;
}

export function periodCacheHit(days: UsageDay[]): number {
  const input = days.reduce((sum, day) => sum + day.input, 0);
  const cacheRead = days.reduce((sum, day) => sum + day.cache_read, 0);
  return cacheHitRate(input, cacheRead);
}
