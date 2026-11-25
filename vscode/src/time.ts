const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const DAY_MS = 24 * 60 * 60 * 1000;

const startOfDay = (date: Date): number => new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
const two = (n: number): string => String(n).padStart(2, "0");

// A message time for the narrow column, in local time: "14:05" today, "Mon 14:05" within the last
// week, "24 Nov" earlier this year, "24 Nov 2025" before that. Empty for a time that is no date.
export function shortTime(time: string, now: Date): string {
  const date = new Date(time);
  if (Number.isNaN(date.getTime())) return "";
  const clock = `${two(date.getHours())}:${two(date.getMinutes())}`;
  const daysAgo = Math.round((startOfDay(now) - startOfDay(date)) / DAY_MS);
  if (daysAgo === 0) return clock;
  if (daysAgo > 0 && daysAgo < 7) return `${WEEKDAYS[date.getDay()]} ${clock}`;
  const day = `${date.getDate()} ${MONTHS[date.getMonth()]}`;
  return date.getFullYear() === now.getFullYear() ? day : `${day} ${date.getFullYear()}`;
}
