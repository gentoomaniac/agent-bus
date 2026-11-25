import assert from "node:assert/strict";
import { test } from "node:test";
import { isSeen } from "../viewport";

const view = { top: 100, bottom: 500 };

test("a row is seen when half of it, or half of the view, is visible", () => {
  assert.equal(isSeen({ top: 200, bottom: 260 }, view), true);
  assert.equal(isSeen({ top: 70, bottom: 130 }, view), true); // 30 of 60 px: exactly half counts
  assert.equal(isSeen({ top: 60, bottom: 130 }, view), false); // 30 of 70 px
  assert.equal(isSeen({ top: 480, bottom: 540 }, view), false); // 20 of 60 px
  assert.equal(isSeen({ top: 700, bottom: 760 }, view), false);
  assert.equal(isSeen({ top: -300, bottom: 900 }, view), true); // taller than the view
  assert.equal(isSeen({ top: 50, bottom: 900 }, view), true); // covers the whole view
  assert.equal(isSeen({ top: -300, bottom: 150 }, view), false); // 50 of 450 px, 50 of 400 px
});
