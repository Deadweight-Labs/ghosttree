package api

import "net/http"

// route is one entry of the routing table. Cache enables the shared response
// cache (60 s) for the route; only GET routes may set it.
type route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
	Cache   bool
}

func (s *Server) routes() []route {
	return []route{
		{Method: "GET", Pattern: "/v1/plans", Handler: s.listPlans, Cache: true},

		{Method: "GET", Pattern: "/v1/customers", Handler: s.listCustomers},
		{Method: "GET", Pattern: "/v1/customers/{id}", Handler: s.getCustomer},
		{Method: "GET", Pattern: "/v1/customers/{id}/accounts", Handler: s.listCustomerAccounts},

		{Method: "GET", Pattern: "/v1/accounts/{id}", Handler: s.getAccount},
		{Method: "GET", Pattern: "/v1/accounts/{id}/balance", Handler: s.getBalance},
		{Method: "GET", Pattern: "/v1/accounts/{id}/ledger", Handler: s.getLedger},
		{Method: "GET", Pattern: "/v1/accounts/{id}/payments", Handler: s.listPayments},
		{Method: "POST", Pattern: "/v1/accounts/{id}/payments", Handler: s.createPayment},

		{Method: "GET", Pattern: "/v1/invoices/{id}", Handler: s.getInvoice},
		{Method: "POST", Pattern: "/v1/invoices/{id}/issue", Handler: s.issueInvoice},
	}
}
