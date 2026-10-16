-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- +goose Down
-- Deliberately retain this database-wide extension: it may predate Cartside or
-- have other dependents. An administrator may remove it after auditing usage.
SELECT 1;
