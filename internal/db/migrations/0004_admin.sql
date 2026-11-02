-- 0004_admin: root names are unique (M8). The admin API refuses a
-- duplicate name with 409.

-- +goose Up

CREATE UNIQUE INDEX scan_roots_name ON scan_roots (name);

-- +goose Down

DROP INDEX scan_roots_name;
