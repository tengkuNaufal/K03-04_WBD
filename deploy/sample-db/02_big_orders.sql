CREATE TABLE big_orders (
    id          BIGSERIAL PRIMARY KEY,
    customer_id INT NOT NULL,
    amount      NUMERIC(12, 2) NOT NULL CHECK (amount >= 0),
    status      TEXT NOT NULL DEFAULT 'new',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO big_orders (customer_id, amount, status, created_at)
SELECT 1 + (g % 5000),
       round((random() * 500)::numeric, 2),
       (ARRAY['new', 'paid', 'shipped'])[1 + g % 3],
       now() - (g || ' minutes')::interval
FROM generate_series(1, 2000000) g;

ANALYZE big_orders;
