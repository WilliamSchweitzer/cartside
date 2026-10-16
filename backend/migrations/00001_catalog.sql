-- +goose Up
CREATE TABLE products (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (btrim(name) <> ''),
    description text NOT NULL CHECK (btrim(description) <> ''),
    category text NOT NULL CHECK (category IN ('produce', 'dairy', 'meat', 'bakery', 'pantry', 'frozen')),
    package_label text NOT NULL CHECK (btrim(package_label) <> ''),
    image_reference text NOT NULL CHECK (btrim(image_reference) <> ''),
    price_cents integer NOT NULL CHECK (price_cents >= 0),
    currency text NOT NULL CHECK (currency = 'USD'),
    available boolean NOT NULL DEFAULT true
);

-- +goose Down
DROP TABLE products;
