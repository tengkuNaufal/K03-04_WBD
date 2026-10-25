-- Example data for the "shop" target database.

CREATE TABLE customers (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    city       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE customers IS 'Pelanggan toko';

CREATE TABLE products (
    id    SERIAL PRIMARY KEY,
    sku   TEXT NOT NULL UNIQUE,
    name  TEXT NOT NULL,
    price NUMERIC(12, 2) NOT NULL CHECK (price >= 0)
);

CREATE TABLE orders (
    id          SERIAL PRIMARY KEY,
    customer_id INT NOT NULL REFERENCES customers(id),
    ordered_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    status      TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'paid', 'shipped', 'cancelled'))
);
CREATE INDEX orders_customer_idx ON orders(customer_id);

CREATE TABLE order_items (
    order_id   INT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id INT NOT NULL REFERENCES products(id),
    quantity   INT NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (order_id, product_id)
);

CREATE VIEW order_totals AS
SELECT o.id AS order_id, c.name AS customer, sum(oi.quantity * p.price) AS total
FROM orders o
JOIN customers c ON c.id = o.customer_id
JOIN order_items oi ON oi.order_id = o.id
JOIN products p ON p.id = oi.product_id
GROUP BY o.id, c.name;

INSERT INTO customers (name, city)
SELECT 'Customer ' || g, (ARRAY['Bandung', 'Jakarta', 'Surabaya', 'Medan'])[1 + g % 4]
FROM generate_series(1, 120) g;

INSERT INTO products (sku, name, price) VALUES
    ('SKU-001', 'Keyboard', 350000),
    ('SKU-002', 'Mouse', 150000),
    ('SKU-003', 'Monitor 24"', 2100000),
    ('SKU-004', 'USB Hub', 99000);

INSERT INTO orders (customer_id, status)
SELECT 1 + (g % 120), (ARRAY['new', 'paid', 'shipped'])[1 + g % 3]
FROM generate_series(1, 300) g;

INSERT INTO order_items (order_id, product_id, quantity)
SELECT o.id, 1 + (o.id % 4), 1 + (o.id % 3) FROM orders o;
