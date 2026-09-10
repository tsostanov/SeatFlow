import test from "node:test";
import assert from "node:assert/strict";
import { createTicketCalendar } from "../internal/gateway/web/ticket-calendar.mjs";

const encoderLength = (value) => new TextEncoder().encode(value).length;

test("a purchased ticket exports a valid folded calendar event", () => {
  const bookingID = "43199c92-02ba-4e64-88de-4bc526e4bfbf";
  const contents = createTicketCalendar(
    {
      title: "Jazz, вечер; специальная программа",
      venue: "Blue Hall\nSaint Petersburg",
      starts_at: "2026-10-09T20:05:00+03:00",
    },
    {
      id: bookingID,
      seat_id: "12",
      created_at: "2026-09-10T12:00:00Z",
      status: "SOLD",
    },
  );
  const unfolded = contents.replace(/\r\n /g, "");
  assert(unfolded.startsWith("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"));
  assert(unfolded.includes("DTSTAMP:20260910T120000Z\r\n"));
  assert(unfolded.includes("DTSTART:20261009T170500Z\r\n"));
  assert(unfolded.includes("SUMMARY:Jazz\\, вечер\\; специальная программа\r\n"));
  assert(unfolded.includes("LOCATION:Blue Hall\\nSaint Petersburg\r\n"));
  assert(unfolded.includes(`ID билета: ${bookingID}`));
  assert(unfolded.endsWith("END:VCALENDAR\r\n"));
  for (const line of contents.split("\r\n"))
    assert(encoderLength(line) <= 75, `calendar line is too long: ${line}`);
});

test("calendar export rejects incomplete or invalid ticket data", () => {
  assert.throws(() => createTicketCalendar({}, {}), /Incomplete/);
  assert.throws(
    () =>
      createTicketCalendar(
        { title: "Event", starts_at: "not-a-date" },
        { id: "booking", seat_id: "1", created_at: "2026-09-10T12:00:00Z" },
      ),
    /Invalid calendar date/,
  );
});
