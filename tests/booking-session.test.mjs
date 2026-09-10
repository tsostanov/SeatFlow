import test from "node:test";
import assert from "node:assert/strict";
import {
  BookingSession,
  SESSION_KEY,
} from "../internal/gateway/web/booking-session.mjs";

const key = "b3e038d8-0ad7-4ce0-8c02-4195c0600598";
const booking = {
  id: "43199c92-02ba-4e64-88de-4bc526e4bfbf",
  event_id: "1",
  seat_id: "12",
  status: "RESERVED",
};
function storage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
  };
}

test("a lost response and reload reuse the persisted key and body", async () => {
  const saved = storage();
  const requests = [];
  let first = true;
  const request = async (_, options) => {
    requests.push(options);
    assert(saved.getItem(SESSION_KEY), "request was persisted before sending");
    if (first) {
      first = false;
      throw new TypeError("network failure after commit");
    }
    return booking;
  };
  const original = new BookingSession(saved, request, () => key);
  await assert.rejects(original.create(1, 12));
  const reloaded = new BookingSession(saved, request, () => {
    throw new Error("must not generate another key");
  });
  assert.deepEqual(await reloaded.retry(), booking);
  assert.deepEqual(requests[0], requests[1]);
  assert.equal(reloaded.pending, null);
  assert.equal(new BookingSession(saved, request).bookingID, booking.id);
});

test("an unresolved request cannot be replaced by another seat or restored booking", async () => {
  const session = new BookingSession(
    storage(),
    async () => {
      throw new Error("offline");
    },
    () => key,
  );
  await assert.rejects(session.create(1, 12));
  await assert.rejects(session.create(1, 13), /предыдущего/);
  await assert.rejects(session.restore(booking.id), /предыдущего/);
  assert.equal(session.pending.body.seat_id, 12);
});

test("server timeout keeps pending state; a definite conflict clears it", async () => {
  for (const [status, pending] of [
    [504, true],
    [503, true],
    [408, true],
    [409, false],
    [400, false],
    [404, false],
  ]) {
    const session = new BookingSession(
      storage(),
      async () => {
        throw Object.assign(new Error("request failed"), { status });
      },
      () => key,
    );
    await assert.rejects(session.create(1, 12));
    assert.equal(Boolean(session.pending), pending, `HTTP ${status}`);
  }
});

test("unavailable storage prevents a new request from being sent", async () => {
  let sent = false;
  const saved = storage();
  saved.setItem = () => {
    throw new Error("quota exceeded");
  };
  const session = new BookingSession(
    saved,
    async () => {
      sent = true;
      return booking;
    },
    () => key,
  );
  await assert.rejects(session.create(1, 12), /сохранить/);
  assert.equal(sent, false);
});

test("failure to save a successful response preserves recovery information", async () => {
  const saved = storage();
  const write = saved.setItem;
  const session = new BookingSession(
    saved,
    async () => {
      saved.setItem = () => {
        throw new Error("quota exceeded");
      };
      return booking;
    },
    () => key,
  );
  await assert.rejects(session.create(1, 12));
  saved.setItem = write;
  const reloaded = new BookingSession(saved, async () => booking);
  assert.equal(reloaded.pending.key, key);
  assert.deepEqual(await reloaded.retry(), booking);
});

test("an invalid successful response cannot erase the pending request", async () => {
  const session = new BookingSession(
    storage(),
    async () => ({ ...booking, seat_id: "13" }),
    () => key,
  );
  await assert.rejects(session.create(1, 12), /ответ сервера/);
  assert.equal(session.pending.key, key);
});

test("old sessions migrate and failed restoration preserves the saved booking", async () => {
  const saved = storage();
  saved.setItem("booking-id", booking.id);
  const session = new BookingSession(saved, async () => {
    throw Object.assign(new Error("not found"), { status: 404 });
  });
  assert.equal(session.bookingID, booking.id);
  await assert.rejects(session.restore(key));
  assert.equal(session.bookingID, booking.id);
});

test("an invalid restore response cannot replace the saved booking", async () => {
  const saved = storage();
  saved.setItem("booking-id", booking.id);
  const session = new BookingSession(saved, async () => ({
    ...booking,
    id: key,
  }));
  await assert.rejects(session.restore(booking.id), /ответ сервера/);
  assert.equal(session.bookingID, booking.id);
  assert.equal(saved.getItem(SESSION_KEY), null);
});
