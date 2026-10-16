-- +goose Up
CREATE TABLE carts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_issuer text NOT NULL CHECK (btrim(owner_issuer) <> ''),
    owner_subject text NOT NULL CHECK (btrim(owner_subject) <> ''),
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    UNIQUE (owner_issuer, owner_subject)
);

CREATE TABLE cart_items (
    cart_id uuid NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    -- Keep referenced products: deletion must never silently remove cart contents.
    product_id uuid NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    PRIMARY KEY (cart_id, product_id)
);

-- +goose Down
DROP TABLE cart_items;
DROP TABLE carts;
