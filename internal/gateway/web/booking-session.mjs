export const SESSION_KEY = "seatflow-session-v1";

const isUUID = (value) =>
  typeof value === "string" &&
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    value,
  ) &&
  value !== "00000000-0000-0000-0000-000000000000";

// One storage write records the result and clears the pending request together.
// A lost response, reload or failed storage write must keep the same request key.
export class BookingSession {
  constructor(storage, request, newKey = () => crypto.randomUUID()) {
    this.storage = storage;
    this.request = request;
    this.newKey = newKey;
    this.state = { bookingID: null, pending: null };
    const saved = storage.getItem(SESSION_KEY);
    if (saved) {
      let value;
      try {
        value = JSON.parse(saved);
      } catch {
        value = null;
      }
      if (
        !value ||
        (value.bookingID !== null && !isUUID(value.bookingID)) ||
        (value.pending !== null && !this.validPending(value.pending))
      ) {
        throw new Error(
          "Сохранённые данные брони повреждены. Откройте новую вкладку и восстановите бронь по ID.",
        );
      }
      this.state = value;
    } else {
      const legacy = storage.getItem("booking-id");
      if (isUUID(legacy)) this.state.bookingID = legacy;
    }
  }

  validPending(value) {
    return (
      value &&
      isUUID(value.key) &&
      Number.isSafeInteger(value.body?.event_id) &&
      value.body.event_id > 0 &&
      Number.isSafeInteger(value.body?.seat_id) &&
      value.body.seat_id > 0
    );
  }

  get bookingID() {
    return this.state.bookingID;
  }
  get pending() {
    const value = this.state.pending;
    return value ? { key: value.key, body: { ...value.body } } : null;
  }

  save(next) {
    try {
      this.storage.setItem(SESSION_KEY, JSON.stringify(next));
    } catch {
      throw new Error(
        "Браузер не смог сохранить запрос. Разрешите хранение данных и повторите попытку.",
      );
    }
    this.state = next;
  }

  async create(eventID, seatID) {
    if (this.pending) {
      if (
        this.pending.body.event_id !== eventID ||
        this.pending.body.seat_id !== seatID
      ) {
        throw new Error(
          "Сначала проверьте результат предыдущего бронирования.",
        );
      }
    } else {
      const pending = {
        key: this.newKey(),
        body: { event_id: eventID, seat_id: seatID },
      };
      if (!this.validPending(pending))
        throw new Error("Выберите мероприятие и место.");
      this.save({ ...this.state, pending });
    }
    return this.retry();
  }

  async retry() {
    const pending = this.pending;
    if (!pending) return null;
    let booking;
    try {
      booking = await this.request("/bookings", {
        method: "POST",
        headers: { "Idempotency-Key": pending.key },
        body: JSON.stringify(pending.body),
      });
    } catch (error) {
      // These responses are definitive rejections from the booking API.
      if ([400, 404, 409].includes(error.status))
        this.save({ ...this.state, pending: null });
      throw error;
    }
    if (
      !isUUID(booking?.id) ||
      String(booking.event_id) !== String(pending.body.event_id) ||
      String(booking.seat_id) !== String(pending.body.seat_id)
    ) {
      throw new Error(
        "Не удалось проверить ответ сервера. Повторите проверку брони.",
      );
    }
    this.save({ bookingID: booking.id, pending: null });
    return booking;
  }

  async restore(id) {
    if (this.pending)
      throw new Error("Сначала проверьте результат предыдущего бронирования.");
    id = typeof id === "string" ? id.trim() : "";
    if (!isUUID(id)) throw new Error("Введите ID брони в формате UUID.");
    const booking = await this.request("/bookings/" + id);
    if (!isUUID(booking?.id) || booking.id.toLowerCase() !== id.toLowerCase())
      throw new Error(
        "Не удалось проверить ответ сервера. Сохранённая бронь не изменена.",
      );
    this.save({ bookingID: booking.id, pending: null });
    return booking;
  }
}
