INSERT INTO events(id, title, venue, starts_at, price_minor, currency) VALUES
    (1, 'Go Backend Meetup', 'Moscow / Loft Hall', now() + interval '30 days', 150000, 'RUB'),
    (2, 'Jazz Evening', 'Saint Petersburg / Blue Hall', now() + interval '45 days', 250000, 'RUB'),
    (3, 'Distributed Systems Workshop', 'Kazan / IT Park', now() + interval '60 days', 390000, 'RUB')
ON CONFLICT DO NOTHING;
INSERT INTO seats(event_id, id)
    SELECT e.id, n FROM events e CROSS JOIN generate_series(1, 32) n
ON CONFLICT DO NOTHING;
