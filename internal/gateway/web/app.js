import { BookingSession } from "/booking-session.mjs";

const $ = (id) => document.getElementById(id);
const state = {
  events: [],
  selected: null,
  current: null,
  busy: false,
  refreshing: false,
  epoch: 0,
  seatEvent: null,
  available: new Set(),
};
let session;

const titles = {
  RESERVED: "Место за вами",
  SOLD: "Билет ваш!",
  CANCELLED: "Бронь отменена",
  EXPIRED: "Время брони истекло",
};
const labels = {
  RESERVED: "ЗАБРОНИРОВАНО",
  SOLD: "ОПЛАЧЕНО",
  CANCELLED: "ОТМЕНЕНО",
  EXPIRED: "ВРЕМЯ ИСТЕКЛО",
};
const hints = {
  RESERVED: "Оплатите билет до окончания отсчёта или отмените бронь.",
  SOLD: "Сохраните ID — по нему можно снова открыть билет.",
  CANCELLED: "Место снова доступно. Можно выбрать другое событие или место.",
  EXPIRED: "Место освобождено. Если оно ещё доступно, создайте новую бронь.",
};
const money = (minor, currency) =>
  new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency,
    maximumFractionDigits: 2,
  }).format(Number(minor) / 100);
const date = (value) =>
  new Date(value).toLocaleString("ru-RU", {
    day: "numeric",
    month: "long",
    hour: "2-digit",
    minute: "2-digit",
  });

function note(text) {
  $("notice").textContent = text;
  $("notice").hidden = !text;
}
function connection(online) {
  $("connection").dataset.online = String(online);
  $("connection").textContent = online
    ? "Данные обновляются"
    : "Не удалось обновить данные";
}
function errorMessage(error) {
  if (error.status >= 500 || error.status === 408)
    return "Сервер временно недоступен. Повторите попытку — сохранённый запрос не создаст вторую бронь.";
  if (error.message === "seat is unavailable")
    return "Это место уже заняли. Выберите другое на обновлённой схеме.";
  if (error.message.includes("demo payment declined"))
    return "Тестовый платёж отклонён. Бронь сохранена, можно попробовать ещё раз.";
  if (error.message.startsWith("booking is "))
    return "Состояние брони уже изменилось. Обновляем информацию.";
  if (error.status === 404)
    return "Бронь или мероприятие не найдены. Проверьте ID и попробуйте ещё раз.";
  if (error.status === 400)
    return "Проверьте выбранное мероприятие, место и ID брони.";
  return error.message;
}

async function api(path, options = {}) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 8000);
  try {
    const response = await fetch("/api" + path, {
      ...options,
      signal: controller.signal,
      headers: { "Content-Type": "application/json", ...options.headers },
    });
    const data = await response.json().catch(() => null);
    if (!response.ok)
      throw Object.assign(new Error(data?.error || "Ошибка ответа сервера"), {
        status: response.status,
      });
    if (!data)
      throw new Error("Не удалось прочитать ответ сервера. Повторите попытку.");
    return data;
  } catch (error) {
    if (error.name === "AbortError" || error instanceof TypeError) {
      connection(false);
      throw new Error(
        "Нет ответа от сервера. Проверьте соединение и повторите попытку.",
      );
    }
    if (error.status >= 500) connection(false);
    throw error;
  } finally {
    clearTimeout(timeout);
  }
}

function controls() {
  const active = state.current?.status === "RESERVED";
  const pending = session?.pending;
  const locked = state.busy || Boolean(pending);
  $("event").disabled = locked || !state.events.length;
  for (const seat of $("seats").children) {
    seat.disabled = locked || active || !state.available.has(seat.dataset.id);
    seat.classList.toggle("selected", state.selected === seat.dataset.id);
    seat.classList.toggle(
      "own",
      state.current?.event_id === state.seatEvent &&
        state.current?.seat_id === seat.dataset.id &&
        ["RESERVED", "SOLD"].includes(state.current.status),
    );
    seat.setAttribute(
      "aria-pressed",
      String(state.selected === seat.dataset.id),
    );
  }
  $("reserve").disabled =
    !session ||
    locked ||
    active ||
    !state.selected ||
    !state.available.has(state.selected);
  $("reserve").textContent = state.busy ? "Подождите…" : "Забронировать";
  $("selection").textContent = active
    ? "Сначала завершите текущую бронь"
    : state.selected
      ? `Место ${state.selected}`
      : "Выберите место на схеме";
  for (const id of ["pay", "cancel"]) {
    $(id).hidden = !active;
    $(id).disabled = locked;
  }
  $("payment-demo").hidden = !active;
  $("decline").disabled = locked;
  $("restore-button").disabled = !session || locked || active;
  $("restore-id").disabled = locked || active;
  $("recovery").hidden = !pending;
  $("retry").disabled = state.busy;
  if (pending)
    $("recovery-details").textContent =
      `Место ${pending.body.seat_id}: результат запроса пока неизвестен. Проверим его с тем же ключом — вторая бронь не появится.`;
}

async function action(fn) {
  if (state.busy) return;
  state.busy = true;
  state.epoch++;
  controls();
  note("");
  try {
    await fn();
  } catch (error) {
    note(errorMessage(error));
  } finally {
    state.busy = false;
    controls();
  }
}

function eventDetails() {
  const event = state.events.find((e) => e.id === $("event").value);
  $("details").textContent = event
    ? `${event.venue} · ${date(event.starts_at)}`
    : "";
  $("price").textContent = event
    ? money(event.price_minor, event.currency)
    : "";
}

async function catalog() {
  const epoch = state.epoch;
  const data = await api("/events");
  if (epoch !== state.epoch) return;
  state.events = data.events;
  $("event").replaceChildren();
  for (const event of state.events) {
    const option = document.createElement("option");
    option.value = event.id;
    option.textContent = event.title;
    $("event").append(option);
  }
  if (session?.pending)
    $("event").value = String(session.pending.body.event_id);
  eventDetails();
}

// Reuse seat elements so a background refresh does not remove keyboard focus.
function reconcileSeats(eventID, data) {
  const area = $("seats");
  if (state.seatEvent !== eventID) {
    area.replaceChildren();
    state.selected = null;
  }
  state.seatEvent = eventID;
  state.available = new Set(data.available_seat_ids);
  const ids = new Set(data.seat_ids);
  for (const child of [...area.children])
    if (!ids.has(child.dataset.id)) child.remove();
  const existing = new Map([...area.children].map((el) => [el.dataset.id, el]));
  data.seat_ids.forEach((id, index) => {
    let button = existing.get(id);
    if (!button) {
      button = document.createElement("button");
      button.className = "seat";
      button.dataset.id = id;
      button.textContent = id;
      button.onclick = () => {
        state.selected = id;
        controls();
      };
    }
    if (area.children[index] !== button)
      area.insertBefore(button, area.children[index] || null);
    button.setAttribute(
      "aria-label",
      `Место ${id}, ${state.available.has(id) ? "свободно" : "занято"}`,
    );
  });
  if (!state.available.has(state.selected)) state.selected = null;
  $("availability").textContent = data.seat_ids.length
    ? `Свободно ${data.available_seat_ids.length} из ${data.seat_ids.length} мест`
    : "Для этого мероприятия пока нет мест";
  controls();
}

async function loadSeats() {
  const eventID = $("event").value;
  if (!eventID) {
    $("availability").textContent = "Пока нет доступных мероприятий";
    return;
  }
  const epoch = state.epoch;
  const data = await api(`/events/${eventID}/seats`);
  if (epoch !== state.epoch || eventID !== $("event").value) return;
  reconcileSeats(eventID, data);
}

function renderBooking() {
  const booking = state.current;
  $("empty-booking").hidden = Boolean(booking);
  $("booking").hidden = !booking;
  $("booking-title").textContent = booking
    ? titles[booking.status]
    : "Здесь будет ваша бронь";
  $("booking-status").textContent = booking
    ? labels[booking.status]
    : "ЖДЁТ ВАШЕГО ВЫБОРА";
  $("booking-status").dataset.state = booking?.status || "";
  if (booking) {
    const event = state.events.find((e) => e.id === booking.event_id);
    $("booking-event").textContent =
      event?.title || `Мероприятие ${booking.event_id}`;
    $("booking-venue").textContent = event
      ? `${event.venue} · ${date(event.starts_at)}`
      : "";
    $("booking-seat").textContent = booking.seat_id;
    $("booking-price").textContent = money(
      booking.price_minor,
      booking.currency,
    );
    $("booking-hint").textContent = hints[booking.status];
    $("booking-id").textContent = booking.id;
    $("countdown").hidden = booking.status !== "RESERVED";
  }
  tick();
  controls();
}

function acceptBooking(booking, selectEvent = false) {
  state.current = booking;
  if (selectEvent && state.events.some((e) => e.id === booking.event_id)) {
    $("event").value = booking.event_id;
    eventDetails();
  }
  renderBooking();
}

function tick() {
  if (state.current?.status !== "RESERVED") return;
  const remaining = Math.max(
    0,
    Date.parse(state.current.expires_at) - Date.now(),
  );
  const duration =
    Date.parse(state.current.expires_at) - Date.parse(state.current.created_at);
  const seconds = Math.ceil(remaining / 1000);
  $("timer").textContent =
    `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  $("time-progress").value =
    duration > 0 ? Math.min(100, (remaining / duration) * 100) : 0;
  $("countdown").classList.toggle("urgent", seconds < 60);
  if (!seconds)
    $("booking-hint").textContent = "Проверяем состояние брони на сервере…";
}

async function refresh() {
  if (state.busy || state.refreshing || document.hidden) return;
  state.refreshing = true;
  const epoch = state.epoch;
  try {
    if (!state.events.length) await catalog();
    const id = state.current?.id;
    if (id) {
      const booking = await api("/bookings/" + id);
      if (epoch === state.epoch && state.current?.id === id)
        acceptBooking(booking);
    }
    if (epoch === state.epoch) await loadSeats();
    connection(true);
  } catch {
    connection(false);
  } finally {
    state.refreshing = false;
  }
}

$("event").onchange = () =>
  action(async () => {
    state.selected = null;
    eventDetails();
    // Old seats must not remain selectable if the new event fails to load.
    state.available.clear();
    $("seats").replaceChildren();
    controls();
    await loadSeats();
  });
$("reserve").onclick = () =>
  action(async () => {
    try {
      acceptBooking(
        await session.create(Number($("event").value), Number(state.selected)),
      );
    } finally {
      if (!session.pending) await loadSeats();
    }
  });
$("retry").onclick = () =>
  action(async () => {
    try {
      const booking = await session.retry();
      if (booking) acceptBooking(booking, true);
    } finally {
      if (!session.pending) await loadSeats();
    }
  });
for (const [id, result] of [
  ["pay", "success"],
  ["decline", "fail"],
]) {
  $(id).onclick = () =>
    action(async () => {
      try {
        acceptBooking(
          await api(`/bookings/${state.current.id}/checkout`, {
            method: "POST",
            body: JSON.stringify({ payment_result: result }),
          }),
        );
      } catch (error) {
        if (error.status === 409)
          acceptBooking(await api("/bookings/" + state.current.id));
        throw error;
      } finally {
        await loadSeats();
      }
    });
}
$("cancel").onclick = () =>
  action(async () => {
    try {
      acceptBooking(
        await api("/bookings/" + state.current.id, { method: "DELETE" }),
      );
    } catch (error) {
      if (error.status === 409)
        acceptBooking(await api("/bookings/" + state.current.id));
      throw error;
    } finally {
      await loadSeats();
    }
  });
$("restore-form").onsubmit = (event) => {
  event.preventDefault();
  action(async () => {
    acceptBooking(await session.restore($("restore-id").value), true);
    await loadSeats();
  });
};
$("copy").onclick = async () => {
  try {
    await navigator.clipboard.writeText(state.current.id);
    note("ID брони скопирован.");
  } catch {
    note(
      "Не удалось скопировать автоматически. Выделите ID в карточке билета и скопируйте вручную.",
    );
  }
};

action(async () => {
  try {
    session = new BookingSession(sessionStorage, api);
  } catch (error) {
    note(error.message);
  }
  await catalog();
  // An unavailable saved booking must not hide the event's seat map.
  await loadSeats();
  if (session?.pending) {
    acceptBooking(await session.retry(), true);
    await loadSeats();
  } else if (session?.bookingID) {
    acceptBooking(await api("/bookings/" + session.bookingID), true);
    await loadSeats();
  }
  connection(true);
});
setInterval(tick, 1000);
setInterval(refresh, 5000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
window.addEventListener("online", refresh);
window.addEventListener("offline", () => connection(false));
