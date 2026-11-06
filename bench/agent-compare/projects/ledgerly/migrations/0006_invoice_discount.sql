ALTER TABLE invoices ADD COLUMN discount BIGINT DEFAULT 0; -- cents
CREATE INDEX invoices_account_idx ON invoices(account_id);
