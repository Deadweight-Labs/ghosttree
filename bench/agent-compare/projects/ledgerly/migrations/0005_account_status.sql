ALTER TABLE accounts ADD COLUMN status TEXT DEFAULT 'active';
CREATE INDEX accounts_customer_idx ON accounts(customer_id);
