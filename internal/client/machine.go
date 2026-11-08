package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Machines lists the machines that concern the caller: its own, those of its
// organizations and those shared with them.
func (c *Client) Machines() ([]store.Machine, error) {
	var out []store.Machine
	err := c.do("GET", "/api/machines", nil, nil, &out)
	return out, err
}

// MachinePage is the page of one machine; a machine the caller has no part in
// answers like one that does not exist.
func (c *Client) MachinePage(name string) (store.Machine, error) {
	var out store.Machine
	err := c.do("GET", "/api/machines/"+url.PathEscape(name), nil, nil, &out)
	return out, err
}

// ShareEntry shares an entry with a machine scope (kind knowledge or runbook)
// with an organization for which a person has already shared the machine; with
// share false it takes the sharing back. It answers with the organizations the
// entry is shared with now.
func (c *Client) ShareEntry(kind string, id int64, org string, share bool) ([]string, error) {
	method := "PUT"
	if !share {
		method = "DELETE"
	}
	path := "/api/knowledge/"
	if kind == store.EntryRunbook {
		path = "/api/runbooks/"
	}
	var out struct {
		SharedWith []string `json:"shared_with"`
	}
	err := c.do(method, path+strconv.FormatInt(id, 10)+"/shares/"+url.PathEscape(org), nil, nil, &out)
	return out.SharedWith, err
}
