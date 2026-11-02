-- 0005_prefs: per-account display preferences. A JSON object whose
-- keys the API validates; absent keys take the defaults.

-- +goose Up

ALTER TABLE users ADD COLUMN prefs TEXT NOT NULL DEFAULT '{}';

-- +goose Down

ALTER TABLE users DROP COLUMN prefs;
