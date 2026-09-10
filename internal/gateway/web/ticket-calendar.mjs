const encoder = new TextEncoder();

const escapeText = (value) =>
  String(value)
    .replace(/\\/g, "\\\\")
    .replace(/\r\n|\r|\n/g, "\\n")
    .replace(/;/g, "\\;")
    .replace(/,/g, "\\,");

function calendarDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) throw new Error("Invalid calendar date");
  return date
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d{3}Z$/, "Z");
}

// RFC 5545 limits content lines to 75 octets. Continuation lines begin with a
// single space, and Unicode characters must never be split between lines.
function foldLine(line) {
  const parts = [];
  let current = "";
  let bytes = 0;
  for (const character of line) {
    const size = encoder.encode(character).length;
    if (bytes + size > 75) {
      parts.push(current);
      current = " " + character;
      bytes = 1 + size;
    } else {
      current += character;
      bytes += size;
    }
  }
  parts.push(current);
  return parts.join("\r\n");
}

export function createTicketCalendar(event, booking) {
  if (!event?.title || !booking?.id || booking.seat_id == null)
    throw new Error("Incomplete ticket data");
  const lines = [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//SeatFlow//Ticket//RU",
    "CALSCALE:GREGORIAN",
    "METHOD:PUBLISH",
    "BEGIN:VEVENT",
    `UID:${escapeText(booking.id)}@seatflow.local`,
    `DTSTAMP:${calendarDate(booking.created_at)}`,
    `DTSTART:${calendarDate(event.starts_at)}`,
    `SUMMARY:${escapeText(event.title)}`,
    `LOCATION:${escapeText(event.venue || "")}`,
    `DESCRIPTION:${escapeText(`Место ${booking.seat_id}\nID билета: ${booking.id}`)}`,
    "STATUS:CONFIRMED",
    "END:VEVENT",
    "END:VCALENDAR",
  ];
  return lines.map(foldLine).join("\r\n") + "\r\n";
}
