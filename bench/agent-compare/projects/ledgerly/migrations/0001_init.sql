CREATE TABLE customers (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT        NOT NULL,
    email       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE accounts (
    id          BIGSERIAL PRIMARY KEY,
    customer_id BIGINT      NOT NULL REFERENCES customers(id),
    plan        TEXT        NOT NULL,
    opened_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
