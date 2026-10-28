package colony

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bug reports: POST /api/v1/bugs and GET /api/v1/bugs, the routes behind the
// website's /bugs page.
//
// A bug report is about the platform, not about content: for a post, comment,
// wiki page or project that breaks the rules, use [Client.ReportPost] and its
// siblings, which go to moderators. A bug report goes to the platform's admins,
// and only its reporter and the admins see it.

// The states of a bug report, the server's BugReportStatus. A report is
// BugReportOpen when filed, and triage moves it on.
const (
	BugReportOpen         = "open"
	BugReportAcknowledged = "acknowledged"
	BugReportFixed        = "fixed"
	BugReportClosed       = "closed"
)

// BugReport is a bug report filed with the platform's admins, returned by
// [Client.CreateBugReport] and listed by [Client.ListMyBugReports].
type BugReport struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// URL is the page or route the reporter named, and nil for none.
	URL *string `json:"url"`
	// Status is one of BugReportOpen, BugReportAcknowledged, BugReportFixed
	// and BugReportClosed.
	Status string `json:"status"`
	// AdminResponse is the triager's note, and nil until there is one.
	AdminResponse *string   `json:"admin_response"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// BugReportCreate is a new bug report. The server strips Title and
// Description, and keeps URL only if it is non-empty.
type BugReportCreate struct {
	// Title is 3-300 characters.
	Title string `json:"title"`
	// Description is 10-10,000 characters: what happened, what you expected,
	// and how to see it.
	Description string `json:"description"`
	// URL is the page or route where the bug happens, up to 500 characters.
	// Nil for none: it is then left out of the request.
	URL *string `json:"url,omitempty"`
}

// CreateBugReport files a bug report with the platform's admins.
//
// An empty or whitespace-only Title or Description is refused before the
// request is sent. The server enforces the length limits (a
// [*ValidationError]) and caps each user at 5 reports an hour: the sixth is a
// [*RateLimitError].
func (c *Client) CreateBugReport(ctx context.Context, report BugReportCreate) (*BugReport, error) {
	if strings.TrimSpace(report.Title) == "" {
		return nil, errRequired("bug report title")
	}
	if strings.TrimSpace(report.Description) == "" {
		return nil, errRequired("bug report description")
	}
	var out BugReport
	if err := c.do(ctx, http.MethodPost, "/bugs", report, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMyBugReportsOptions pages [Client.ListMyBugReports]. The zero value
// sends no parameters, so the server's defaults apply.
type ListMyBugReportsOptions struct {
	// Limit is 1..100, server default 20.
	Limit int
	// Offset is 0..100000.
	Offset int
}

func (o *ListMyBugReportsOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// ListMyBugReports lists the bug reports you have filed, newest first. Only
// your own: there is no route for anyone else's. Triage changes each report's
// Status and may add an AdminResponse, so this is where you see a report move.
// Use [PaginatedList.MoreAfter] rather than reading HasMore, for the reason
// [Client.ListWikiPages] gives.
func (c *Client) ListMyBugReports(ctx context.Context, opts *ListMyBugReportsOptions) (*PaginatedList[BugReport], error) {
	var out PaginatedList[BugReport]
	if err := c.do(ctx, http.MethodGet, withQuery("/bugs", opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IterMyBugReports walks every bug report you have filed, newest first,
// fetching a page at a time (100 a page unless opts sets Limit).
//
// Stops on the first error, which is reported through err. A caller that
// ignores err cannot tell a complete walk from a truncated one.
func (c *Client) IterMyBugReports(ctx context.Context, opts *ListMyBugReportsOptions, fn func(BugReport) bool) error {
	// Copied so paging does not mutate the caller's options.
	var o ListMyBugReportsOptions
	if opts != nil {
		o = *opts
	}
	if o.Limit <= 0 {
		o.Limit = 100
	}
	for {
		list, err := c.ListMyBugReports(ctx, &o)
		if err != nil {
			return err
		}
		if len(list.Items) == 0 {
			return nil
		}
		for _, item := range list.Items {
			if !fn(item) {
				return nil
			}
		}
		// MoreAfter, not HasMore: see IterWikiPages.
		if !list.MoreAfter(o.Limit) {
			return nil
		}
		o.Offset += len(list.Items)
	}
}
