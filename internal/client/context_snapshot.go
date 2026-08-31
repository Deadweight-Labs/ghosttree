package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

const contextSnapshotClientPageSize = 100

type contextSnapshotHeadResponse struct {
	Snapshot snapshot.Head    `json:"snapshot"`
	Counts   map[string]int64 `json:"counts"`
}

type contextSnapshotCreateResponse struct {
	Snapshot snapshot.Head      `json:"snapshot"`
	Counts   map[string]int64   `json:"counts"`
	Created  bool               `json:"created"`
	Warnings []snapshot.Warning `json:"warnings"`
}

func (c *Client) CreateContextSnapshot(ctx context.Context, in snapshot.CreateInput) (snapshot.CreateResult, error) {
	var response contextSnapshotCreateResponse
	if err := c.doContext(ctx, http.MethodPost, "/api/context-snapshots", nil, in, &response); err != nil {
		return snapshot.CreateResult{}, err
	}
	if response.Counts != nil {
		response.Snapshot.Counts = response.Counts
	}
	return snapshot.CreateResult{Snapshot: response.Snapshot, Created: response.Created, Warnings: response.Warnings}, nil
}

func (c *Client) ContextSnapshots(ctx context.Context, filter snapshot.ListFilter) (snapshot.SnapshotPage, error) {
	query := url.Values{"project": {filter.Project}}
	if filter.Cursor != "" {
		query.Set("cursor", filter.Cursor)
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}
	var page snapshot.SnapshotPage
	err := c.doContext(ctx, http.MethodGet, "/api/context-snapshots", query, nil, &page)
	return page, err
}

func (c *Client) ContextSnapshot(ctx context.Context, project, name string) (snapshot.Head, map[string]int64, error) {
	query := url.Values{"project": {project}}
	var response contextSnapshotHeadResponse
	err := c.doContext(ctx, http.MethodGet, contextSnapshotPath(name), query, nil, &response)
	if err != nil {
		return snapshot.Head{}, nil, err
	}
	response.Snapshot.Counts = response.Counts
	return response.Snapshot, response.Counts, nil
}

func (c *Client) ContextSnapshotEntries(ctx context.Context, project, name string, filter snapshot.EntryFilter) (snapshot.EntryPage, error) {
	query := url.Values{"project": {project}}
	if filter.Domain != "" {
		query.Set("domain", filter.Domain)
	}
	if filter.Key != "" {
		query.Set("key", filter.Key)
	}
	if filter.Cursor != "" {
		query.Set("cursor", filter.Cursor)
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}
	var page snapshot.EntryPage
	err := c.doContext(ctx, http.MethodGet, contextSnapshotPath(name)+"/entries", query, nil, &page)
	return page, err
}

func (c *Client) ExportContextSnapshot(ctx context.Context, project, name string, filter *snapshot.ExportFilter, dst io.Writer) error {
	if err := validateContextSnapshotExportFilter(filter); err != nil {
		return err
	}
	head, counts, err := c.ContextSnapshot(ctx, project, name)
	if err != nil {
		return err
	}
	entries, err := c.allContextSnapshotEntries(ctx, project, name, filter)
	if err != nil {
		return err
	}
	return snapshot.WriteExport(dst, head, counts, entries, filter)
}

func (c *Client) VerifyContextSnapshot(ctx context.Context, project, name string) (snapshot.Verification, error) {
	var exported bytes.Buffer
	if err := c.ExportContextSnapshot(ctx, project, name, nil, &exported); err != nil {
		return snapshot.Verification{}, err
	}
	return snapshot.VerifyExport(&exported)
}

func (c *Client) allContextSnapshotEntries(ctx context.Context, project, name string, filter *snapshot.ExportFilter) ([]snapshot.Entry, error) {
	if filter != nil && filter.Key != nil {
		page, err := c.ContextSnapshotEntries(ctx, project, name, snapshot.EntryFilter{Domain: filter.Domain, Key: *filter.Key})
		if err != nil {
			return nil, err
		}
		if page.Exact == nil {
			return nil, fmt.Errorf("context snapshot exact entry response omitted payload")
		}
		if page.Exact.Domain != filter.Domain || page.Exact.Key != *filter.Key {
			return nil, fmt.Errorf("context snapshot exact entry does not match requested key %s/%s", filter.Domain, *filter.Key)
		}
		return []snapshot.Entry{*page.Exact}, nil
	}

	domain := ""
	if filter != nil {
		domain = filter.Domain
	}
	var entries []snapshot.Entry
	cursor := ""
	seen := map[string]struct{}{cursor: {}}
	for {
		page, err := c.ContextSnapshotEntries(ctx, project, name, snapshot.EntryFilter{Domain: domain, Cursor: cursor, Limit: contextSnapshotClientPageSize})
		if err != nil {
			return nil, err
		}
		for _, summary := range page.Entries {
			exact, err := c.ContextSnapshotEntries(ctx, project, name, snapshot.EntryFilter{Domain: summary.Domain, Key: summary.Key})
			if err != nil {
				return nil, err
			}
			if exact.Exact == nil {
				return nil, fmt.Errorf("context snapshot exact entry response omitted payload for %s/%s", summary.Domain, summary.Key)
			}
			if !contextSnapshotEntryMatchesSummary(*exact.Exact, summary) {
				return nil, fmt.Errorf("context snapshot exact entry does not match summary for %s/%s", summary.Domain, summary.Key)
			}
			entries = append(entries, *exact.Exact)
		}
		if page.NextCursor == "" {
			return entries, nil
		}
		if _, duplicate := seen[page.NextCursor]; duplicate {
			return nil, fmt.Errorf("context snapshot entries returned a repeated cursor")
		}
		cursor = page.NextCursor
		seen[cursor] = struct{}{}
	}
}

func contextSnapshotEntryMatchesSummary(entry snapshot.Entry, summary snapshot.EntrySummary) bool {
	return entry.Domain == summary.Domain && entry.Key == summary.Key &&
		entry.PayloadDigest == summary.PayloadDigest && entry.PayloadSize == summary.PayloadSize
}

func validateContextSnapshotExportFilter(filter *snapshot.ExportFilter) error {
	if filter == nil {
		return nil
	}
	if filter.Key != nil && (filter.Domain == "" || *filter.Key == "") {
		return &snapshot.RuleError{Code: "snapshot_invalid_filter"}
	}
	return nil
}

func contextSnapshotPath(name string) string {
	segment := url.PathEscape(name)
	segment = strings.ReplaceAll(segment, "+", "%2B")
	return "/api/context-snapshots/" + segment
}
