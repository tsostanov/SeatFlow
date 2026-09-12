CREATE TABLE IF NOT EXISTS payments (
    booking_id uuid PRIMARY KEY,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency text NOT NULL CHECK (length(currency) = 3),
    status text NOT NULL CHECK (status IN ('SUCCEEDED', 'DECLINED', 'REFUNDED')),
    processed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
