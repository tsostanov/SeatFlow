import test from "node:test";
import assert from "node:assert/strict";
import { recommendSeat } from "../internal/gateway/web/seat-recommendation.mjs";

test("the front-row center is recommended first", () => {
  const seats = Array.from({ length: 32 }, (_, index) => index + 1);
  assert.equal(recommendSeat(seats, seats), "4");
  assert.equal(recommendSeat(seats, seats.filter((id) => id !== 4)), "5");
  assert.equal(recommendSeat(seats, seats.slice(8)), "12");
});

test("recommendations use layout positions rather than numeric IDs", () => {
  assert.equal(recommendSeat([10, 40, 99], [40, 99]), "99");
  assert.equal(recommendSeat([91, 7, 300, 2], [91, 7, 300, 2], 4), "7");
});

test("invalid or unavailable seats do not produce a recommendation", () => {
  assert.equal(recommendSeat([1, 2], [3]), null);
  assert.equal(recommendSeat([], []), null);
  assert.equal(recommendSeat(null, [], 8), null);
  assert.equal(recommendSeat([1], [1], 0), null);
});
