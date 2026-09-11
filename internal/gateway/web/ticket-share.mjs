const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function ticketID(value) {
  if (
    typeof value !== "string" ||
    !uuidPattern.test(value) ||
    value === "00000000-0000-0000-0000-000000000000"
  )
    return null;
  return value.toLowerCase();
}

export function ticketIDFromURL(value) {
  try {
    const url = new URL(value);
    const values = new URLSearchParams(url.hash.slice(1)).getAll("ticket");
    return values.length === 1 ? ticketID(values[0]) : null;
  } catch {
    return null;
  }
}

export function createTicketShare(baseURL, event, booking) {
  const id = ticketID(booking?.id);
  const title = typeof event?.title === "string" ? event.title.trim() : "";
  const seat = String(booking?.seat_id ?? "");
  if (!id || booking?.status !== "SOLD" || !title || !/^[1-9]\d*$/.test(seat))
    throw new Error("Only a complete purchased ticket can be shared");

  const url = new URL(baseURL);
  url.hash = new URLSearchParams({ ticket: id }).toString();
  return {
    title: `SeatFlow — ${title}`,
    text: `${title}, место ${seat}. Электронный билет SeatFlow.`,
    url: url.href,
  };
}
