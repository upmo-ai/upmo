export type Day = {
  label: string;
  spendUsd: number | null;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  reasoning: number;
};

export type TaskRow = {
  title: string;
  tokens: number;
};

export const sampleDays: Day[] = [
  { label: "Fri 24", spendUsd: 1.12, input: 80_000, output: 22_000, cacheRead: 40_000, cacheWrite: 8_000, reasoning: 4_000 },
  { label: "Sat 25", spendUsd: 0.84, input: 50_000, output: 14_000, cacheRead: 70_000, cacheWrite: 6_000, reasoning: 2_000 },
  { label: "Sun 26", spendUsd: 1.95, input: 120_000, output: 36_000, cacheRead: 30_000, cacheWrite: 12_000, reasoning: 8_000 },
  { label: "Mon 27", spendUsd: 2.4, input: 150_000, output: 48_000, cacheRead: 90_000, cacheWrite: 15_000, reasoning: 10_000 },
  { label: "Tue 28", spendUsd: null, input: 60_000, output: 18_000, cacheRead: 20_000, cacheWrite: 5_000, reasoning: 3_000 },
  { label: "Wed 29", spendUsd: 3.1, input: 180_000, output: 52_000, cacheRead: 110_000, cacheWrite: 18_000, reasoning: 12_000 },
  { label: "Thu 30", spendUsd: 2.06, input: 140_000, output: 44_000, cacheRead: 85_000, cacheWrite: 14_000, reasoning: 9_000 },
];

export const sampleTasks: TaskRow[] = [
  { title: "Weekly review", tokens: 420_000 },
  { title: "Draft the report", tokens: 310_000 },
  { title: "Index the handbook", tokens: 265_000 },
  { title: "Summarise the board pack", tokens: 198_000 },
];

export function knownSpend(days: Day[]): number {
  return days.reduce((sum, day) => sum + (day.spendUsd ?? 0), 0);
}

export function totalTokens(days: Day[]): number {
  return days.reduce(
    (sum, day) => sum + day.input + day.output + day.cacheRead + day.cacheWrite + day.reasoning,
    0,
  );
}

export function cacheHitRate(input: number, cacheRead: number): number {
  const denom = input + cacheRead;
  if (denom === 0) {
    return 0;
  }
  return cacheRead / denom;
}

export function periodCacheHit(days: Day[]): number {
  const input = days.reduce((sum, day) => sum + day.input, 0);
  const cacheRead = days.reduce((sum, day) => sum + day.cacheRead, 0);
  return cacheHitRate(input, cacheRead);
}
