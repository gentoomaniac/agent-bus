import assert from "node:assert/strict";
import { test } from "node:test";
import { shortTime } from "../time";

// Local times, so the test holds in any time zone.
const local = (year: number, month: number, day: number, hour = 12, minute = 0) => new Date(year, month - 1, day, hour, minute);
const now = local(2026, 11, 25, 15, 0); // a Wednesday

test("shortTime is the clock today, weekday and clock within a week, then the date", () => {
  assert.equal(shortTime(local(2026, 11, 25, 14, 5).toISOString(), now), "14:05");
  assert.equal(shortTime(local(2026, 11, 25, 0, 7).toISOString(), now), "00:07");
  assert.equal(shortTime(local(2026, 11, 24, 9, 30).toISOString(), now), "Tue 09:30");
  assert.equal(shortTime(local(2026, 11, 19, 23, 59).toISOString(), now), "Thu 23:59");
  assert.equal(shortTime(local(2026, 11, 18, 12).toISOString(), now), "18 Nov"); // 7 days ago
  assert.equal(shortTime(local(2026, 1, 3).toISOString(), now), "3 Jan");
  assert.equal(shortTime(local(2025, 11, 24).toISOString(), now), "24 Nov 2025");
});

test("shortTime of a time that is not a date is empty", () => {
  assert.equal(shortTime("soon", now), "");
});
