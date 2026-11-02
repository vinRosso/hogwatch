-- 0003_oidc: the one-time notice shown after a link or unlink (M6).
-- NULL means nothing to show; DELETE /me/notice clears it.

-- +goose Up

ALTER TABLE users ADD COLUMN notice TEXT CHECK (notice IN ('oidc_linked', 'oidc_unlinked'));

-- +goose Down

ALTER TABLE users DROP COLUMN notice;
