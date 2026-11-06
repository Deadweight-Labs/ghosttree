CREATE TABLE invoices (
    id          BIGSERIAL PRIMARY KEY,
    account_id  BIGINT      NOT NULL REFERENCES accounts(id),
    customer_id BIGINT      NOT NULL REFERENCES customers(id),
    number      TEXT        NOT NULL UNIQUE,
    status      TEXT        NOT NULL,
    issued_at   TIMESTAMPTZ,
    due_at      TIMESTAMPTZ
);

CREATE TABLE invoice_lines (
    id           BIGSERIAL PRIMARY KEY,
    invoice_id   BIGINT  NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    position     INT     NOT NULL,
    description  TEXT    NOT NULL,
    quantity     BIGINT  NOT NULL,
    unit_price   BIGINT  NOT NULL, -- cents, net
    tax_rate_bp  INT     NOT NULL
);
