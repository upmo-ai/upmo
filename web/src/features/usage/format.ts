const money = new Intl.NumberFormat("en-GB", {
  style: "currency",
  currency: "USD",
});

const count = new Intl.NumberFormat("en-GB");

const percent = new Intl.NumberFormat("en-GB", {
  style: "percent",
  maximumFractionDigits: 0,
});

const day = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  timeZone: "UTC",
});

/** A YYYY-MM-DD calendar day (or a full timestamp on that day) as "24 Oct". Unique within a year, so safe as a chart label. */
export function formatDay(date: string): string {
  return day.format(new Date(`${date.slice(0, 10)}T00:00:00Z`));
}

const hour = new Intl.DateTimeFormat("en-GB", {
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
  timeZone: "UTC",
});

const precisePercent = new Intl.NumberFormat("en-GB", {
  style: "percent",
  maximumFractionDigits: 1,
});

/** A YYYY-MM-DDTHH:00:00Z hour as "14:00" in UTC. Unique within a day, so safe as a chart label. */
export function formatHour(at: string): string {
  return hour.format(new Date(at));
}

/** A bucket start as a chart label: the hour for hourly buckets, the calendar day for daily ones. */
export function formatBucket(at: string, unit: "hour" | "day"): string {
  return unit === "hour" ? formatHour(at) : formatDay(at);
}

export function formatUsd(value: number | null | undefined): string {
  if (value === null || value === undefined) {
    return "Unknown";
  }
  return money.format(value);
}

export function formatTokens(value: number): string {
  return count.format(value);
}

export function formatRate(value: number): string {
  return percent.format(value);
}

/** A rate with one decimal, for figures that are small, such as an error rate. */
export function formatRatePrecise(value: number): string {
  return precisePercent.format(value);
}
