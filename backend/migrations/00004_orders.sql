-- +goose Up
CREATE TABLE orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    confirmation text NOT NULL UNIQUE DEFAULT ('SIM-' || gen_random_uuid()::text),
    kind text NOT NULL DEFAULT 'simulated' CHECK (kind = 'simulated'),
    owner_issuer text NOT NULL CHECK (btrim(owner_issuer) <> ''),
    owner_subject text NOT NULL CHECK (btrim(owner_subject) <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    currency text NOT NULL CHECK (currency = 'USD'),
    total_cents bigint NOT NULL CHECK (total_cents >= 0),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint) = 32),
    UNIQUE (owner_issuer, owner_subject, idempotency_key)
);
CREATE TABLE order_items (
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    -- Deliberately no catalog FK: snapshots survive product deletion.
    product_id uuid NOT NULL,
    name text NOT NULL CHECK (btrim(name) <> ''),
    package_label text NOT NULL CHECK (btrim(package_label) <> ''),
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents >= 0),
    line_total_cents bigint NOT NULL CHECK (line_total_cents >= 0 AND line_total_cents::numeric = unit_price_cents::numeric * quantity),
    PRIMARY KEY (order_id, product_id)
);
-- Immutable confirmations and snapshots; no runtime update/delete operations.
-- +goose StatementBegin
CREATE FUNCTION reject_order_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'simulated order history is immutable' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER immutable_orders BEFORE UPDATE OR DELETE ON orders FOR EACH ROW EXECUTE FUNCTION reject_order_change();
CREATE TRIGGER immutable_order_items BEFORE UPDATE OR DELETE ON order_items FOR EACH ROW EXECUTE FUNCTION reject_order_change();
-- +goose Down
DROP TABLE order_items;
DROP TABLE orders;
DROP FUNCTION reject_order_change();
