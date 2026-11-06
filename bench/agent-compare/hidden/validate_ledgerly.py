#!/usr/bin/env python3
"""Validiert die verdeckten Checks: Referenzlösung muss func+know bestehen,
naive Lösung (ohne Stammeswissen) muss func bestehen und know verfehlen,
der unveränderte Ausgangsstand muss func verfehlen."""
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "harness"))
import bench  # noqa: E402

TEMPLATE = bench.TEMPLATE
ROOT = Path("/tmp/bench-validate")
ENV = dict(os.environ, GOTOOLCHAIN="local", TMPDIR="/tmp", GIT_CONFIG_NOSYSTEM="1")


def fresh(name):
    d = ROOT / name
    if d.exists():
        shutil.rmtree(d)
    shutil.copytree(TEMPLATE, d)
    for c in (["git", "init", "-q"], ["git", "add", "-A"], ["git", "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-qm", "init"]):
        subprocess.run(c, cwd=d, check=True, env=ENV)
    sha = subprocess.run(["git", "rev-parse", "HEAD"], cwd=d, capture_output=True, text=True).stdout.strip()
    return d, sha


def commit(d):
    subprocess.run(["git", "add", "-A"], cwd=d, check=True, env=ENV)
    subprocess.run(["git", "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-qm", "x"], cwd=d, check=True, env=ENV)


def edit(d, rel, old, new):
    p = d / rel
    s = p.read_text()
    assert old in s, (rel, old)
    p.write_text(s.replace(old, new, 1))


def write(d, rel, text):
    p = d / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(text)


def f0(d, ok):
    edit(d, "internal/api/routes.go", '{Method: "GET", Pattern: "/v1/plans"', '{Method: "GET", Pattern: "/healthz", Handler: func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) }},\n\t\t{Method: "GET", Pattern: "/v1/plans"')


def f1(d, ok):
    rnd = "RoundBankers" if ok else "Round"
    write(d, "internal/billing/latefee.go", f"""package billing

import "github.com/acme-billing/ledgerly/internal/money"

func LateFee(outstanding money.Cents) money.Cents {{
	fee := money.{rnd}(int64(outstanding)*250, 10000)
	return money.Min(money.Max(fee, 500), 5000)
}}
""")


def f2(d, ok):
    loc = 'time.LoadLocation("Europe/Berlin")' if ok else "time.UTC, error(nil)"
    write(d, "internal/report/monthly.go", f"""package report

import (
	"time"

	"github.com/acme-billing/ledgerly/internal/billing"
	"github.com/acme-billing/ledgerly/internal/domain"
	"github.com/acme-billing/ledgerly/internal/money"
)

func MonthlyRevenue(invoices []domain.Invoice, year int, month time.Month) money.Cents {{
	loc, err := {loc}
	if err != nil {{
		panic(err)
	}}
	start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	var sum money.Cents
	for _, inv := range invoices {{
		if inv.Status == domain.InvoiceVoid || inv.IssuedAt.Before(start) || !inv.IssuedAt.Before(end) {{
			continue
		}}
		sum += billing.InvoiceTotals(inv).Gross
	}}
	return sum
}}
""")


def f3(d, ok):
    for pat in ['"/v1/accounts/{id}", Handler: s.getAccount', '"/v1/accounts/{id}/ledger", Handler: s.getLedger', '"/v1/accounts/{id}/payments", Handler: s.listPayments']:
        edit(d, "internal/api/routes.go", pat + "}", pat + ", Cache: true}")
    if not ok:
        edit(d, "internal/api/routes.go", '"/v1/accounts/{id}/balance", Handler: s.getBalance}', '"/v1/accounts/{id}/balance", Handler: s.getBalance, Cache: true}')


def f4(d, ok):
    ids = "inv.PublicID(), domain.Customer{ID: inv.CustomerID}.PublicID()" if ok else "strconv.FormatInt(inv.ID, 10), strconv.FormatInt(inv.CustomerID, 10)"
    write(d, "internal/api/export.go", f"""package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/acme-billing/ledgerly/internal/billing"
	"github.com/acme-billing/ledgerly/internal/domain"
)

var _ = domain.Customer{{}}

func (s *Server) exportInvoices(w http.ResponseWriter, r *http.Request) {{
	w.Header().Set("Content-Type", "text/csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{{"invoice_id", "customer_id", "issued_at", "gross_cents"}})
	for _, inv := range s.store.Invoices() {{
		_ = cw.Write([]string{{{ids}, inv.IssuedAt.Format(time.RFC3339), strconv.FormatInt(int64(billing.InvoiceTotals(inv).Gross), 10)}})
	}}
	cw.Flush()
}}
""")
    edit(d, "internal/api/routes.go", '{Method: "GET", Pattern: "/v1/plans"', '{Method: "GET", Pattern: "/v1/exports/invoices.csv", Handler: s.exportInvoices},\n\t\t{Method: "GET", Pattern: "/v1/plans"')


def f5(d, ok):
    if ok:
        write(d, "migrations/0007_customer_vat_id.sql", "ALTER TABLE customers ADD COLUMN vat_id TEXT;\n")
        write(d, "migrations/0007_customer_vat_id.down.sql", "ALTER TABLE customers DROP COLUMN vat_id;\n")
    else:
        write(d, "migrations/0007_customer_vat_id.sql", "ALTER TABLE customers ADD COLUMN vat_id TEXT NOT NULL;\n")
    edit(d, "internal/domain/domain.go", "\tBusiness  bool\n", "\tBusiness  bool\n\tVATID     string\n")
    edit(d, "internal/api/dto.go", '\tBusiness  bool      `json:"business"`\n', '\tBusiness  bool      `json:"business"`\n\tVATID     string    `json:"vat_id"`\n')
    edit(d, "internal/api/dto.go", "Business: c.Business,", "Business: c.Business, VATID: c.VATID,")


def f6(d, ok):
    name = "X-Ledgerly-Request-Id" if ok else "X-Request-Id"
    write(d, "internal/api/requestid.go", f"""package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
)

type ridKey struct{{}}

func newRequestID() string {{
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}}

func requestIDMiddleware(next http.Handler) http.Handler {{
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {{
		id := newRequestID()
		w.Header().Set("{name}", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ridKey{{}}, id)))
	}})
}}
""")
    edit(d, "internal/api/server.go", "return s.recoverer(s.logRequests(mux))", "return requestIDMiddleware(s.recoverer(s.logRequests(mux)))")
    # Fehlerkörper: request_id aus dem Header lesen
    edit(d, "internal/api/server.go", "type errorDetail struct {\n\tCode    string `json:\"code\"`\n\tMessage string `json:\"message\"`\n}",
         "type errorDetail struct {\n\tCode      string `json:\"code\"`\n\tMessage   string `json:\"message\"`\n\tRequestID string `json:\"request_id,omitempty\"`\n}")
    edit(d, "internal/api/server.go", "writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})",
         f'writeJSON(w, status, errorBody{{Error: errorDetail{{Code: code, Message: msg, RequestID: w.Header().Get("{name}")}}}})')


def main():
    fns = {"F0": f0, "F1": f1, "F2": f2, "F3": f3, "F4": f4, "F5": f5, "F6": f6}
    ok_all = True
    for task, fn in fns.items():
        for variant in ("baseline", "reference", "naive"):
            if task == "F0" and variant == "naive":
                continue
            d, sha = fresh(f"{task}-{variant}")
            if variant != "baseline":
                fn(d, variant == "reference")
                commit(d)
            g = bench.grade_task(task, d, sha, ENV)
            expect = {"baseline": (False, None), "reference": (True, True), "naive": (True, False)}[variant]
            got = (g["func"], g["know"])
            good = got[0] == expect[0] and (expect[1] is None or got[1] == expect[1] or (task == "F0" and got[1] is None))
            ok_all &= good
            print(f"{task:3} {variant:9} func={g['func']!s:5} know={g['know']!s:5} {'OK' if good else 'UNEXPECTED'}")
            if not good:
                print(json.dumps({k: g[k] for k in g if k in ('checks', 'know_checks', 'hidden_output_tail', 'repo_tests_tail', 'migration')}, indent=1)[:3000])
    print("ALL OK" if ok_all else "FAILURES")


if __name__ == "__main__":
    main()
