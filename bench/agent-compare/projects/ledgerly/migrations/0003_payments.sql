CREATE TABLE payments (
    id          BIGSERIAL PRIMARY KEY,
    account_id  BIGINT      NOT NULL REFERENCES accounts(id),
    amount      BIGINT      NOT NULL CHECK (amount > 0), -- cents
    received_at TIMESTAMPTZ NOT NULL,
    reference   TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX payments_account_idx ON payments(account_id);
