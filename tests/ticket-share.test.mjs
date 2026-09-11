import test from "node:test";
import assert from "node:assert/strict";
import {
  createTicketShare,
  ticketIDFromURL,
} from "../internal/gateway/web/ticket-share.mjs";

const id = "43199c92-02ba-4e64-88de-4bc526e4bfbf";
const event = { title: "Jazz Evening" };
const booking = { id, seat_id: "12", status: "SOLD" };

test("a purchased ticket creates a private fragment deep link", () => {
  const share = createTicketShare(
    "https://seatflow.example/?ref=demo#old-fragment",
    event,
    booking,
  );
  assert.deepEqual(share, {
    title: "SeatFlow — Jazz Evening",
    text: "Jazz Evening, место 12. Электронный билет SeatFlow.",
    url: `https://seatflow.example/?ref=demo#ticket=${id}`,
  });
  assert.equal(ticketIDFromURL(share.url), id);
});

test("ticket links accept one valid nonzero UUID and nothing else", () => {
  assert.equal(ticketIDFromURL(`https://seatflow.example/#ticket=${id.toUpperCase()}`), id);
  for (const value of [
    "https://seatflow.example/",
    "https://seatflow.example/#ticket=not-a-uuid",
    "https://seatflow.example/#ticket=00000000-0000-0000-0000-000000000000",
    `https://seatflow.example/#ticket=${id}&ticket=${id}`,
    "not a URL",
  ])
    assert.equal(ticketIDFromURL(value), null, value);
});

test("active or incomplete bookings cannot produce share links", () => {
  assert.throws(
    () => createTicketShare("https://seatflow.example", event, { ...booking, status: "RESERVED" }),
    /purchased ticket/,
  );
  assert.throws(
    () => createTicketShare("https://seatflow.example", {}, booking),
    /purchased ticket/,
  );
});
