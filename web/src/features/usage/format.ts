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

/** A YYYY-MM-DD calendar day as "24 Oct". Unique within a year, so safe as a chart label. */
export function formatDay(date: string): string {
  return day.format(new Date(`${date}T00:00:00Z`));
}

export function formatUsd(value: number | null): string {
  if (value === null) {
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
