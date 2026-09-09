CREATE TABLE IF NOT EXISTS events (
    id bigint PRIMARY KEY,
    title text NOT NULL,
    venue text NOT NULL,
    starts_at timestamptz NOT NULL,
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    currency text NOT NULL CHECK (length(currency) = 3)
);
CREATE TABLE IF NOT EXISTS seats (
    event_id bigint NOT NULL REFERENCES events(id),
    id bigint NOT NULL CHECK (id > 0),
    PRIMARY KEY (event_id, id)
);
CREATE TABLE IF NOT EXISTS bookings (
    id uuid PRIMARY KEY,
    idempotency_key uuid NOT NULL UNIQUE,
    event_id bigint NOT NULL,
    seat_id bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('RESERVED', 'SOLD', 'CANCELLED', 'EXPIRED')),
    expires_at timestamptz NOT NULL,
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    currency text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (event_id, seat_id) REFERENCES seats(event_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_booking_per_seat
    ON bookings(event_id, seat_id) WHERE status IN ('RESERVED', 'SOLD');
CREATE INDEX IF NOT EXISTS expiring_bookings
    ON bookings(expires_at) WHERE status = 'RESERVED';
