# ledgerly

Billing backend for the ACME customer portal: customers, accounts, invoices,
payments and the reports finance needs.

## Layout

- `cmd/ledgerly` – HTTP server (`make run`, listens on 127.0.0.1:8080 with demo data)
- `internal/api` – public JSON API under `/v1`, routing table in `routes.go`
- `internal/billing` – invoice totals, balances, proration
- `internal/money` – `Cents` type and rounding helpers
- `internal/domain` – entities and public IDs
- `internal/store` – in-memory store used by tests and the dev server
- `internal/cache` – TTL cache and HTTP response cache
- `internal/report` – finance reports
- `internal/legacy/refunds` – refund export carried over from the old system
- `migrations/` – PostgreSQL schema migrations (`NNNN_name.sql`, optional `NNNN_name.down.sql`)

## Development

```bash
make test     # go test ./...
make check    # vet, tests, migration check
```
