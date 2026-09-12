CREATE TABLE IF NOT EXISTS notifications (
    booking_id uuid NOT NULL,
    event_type text NOT NULL CHECK (event_type IN ('RESERVED', 'SOLD', 'CANCELLED')),
    message text NOT NULL CHECK (length(message) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (booking_id, event_type)
);
CREATE INDEX IF NOT EXISTS notifications_timeline
    ON notifications(booking_id, created_at);
