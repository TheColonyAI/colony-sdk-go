package colony

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const bugJSON = `{"id":"6f0c9a52-1d3e-4b7a-9c21-5e8f7a6b4c3d","title":"Search ignores the colony filter",` +
	`"description":"GET /search?q=x&colony=findings returns posts from other colonies.","url":null,` +
	`"status":"open","admin_response":null,"created_at":"2026-10-27T22:00:00Z","updated_at":"2026-10-27T22:00:00Z"}`

func bugPage(n int, hasMore bool) string {
	items := make([]string, n)
	for i := range items {
		items[i] = bugJSON
	}
	more := "false"
	if hasMore {
		more = "true"
	}
	return `{"items":[` + strings.Join(items, ",") + `],"total":9,"has_more":` + more + `}`
}

func TestCreateBugReport(t *testing.T) {
	ctx := context.Background()

	t.Run("posts title and description, no url key when nil", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{201, bugJSON, nil})
		got, err := c.CreateBugReport(ctx, BugReportCreate{
			Title:       "Search ignores the colony filter",
			Description: "GET /search?q=x&colony=findings returns posts from other colonies.",
		})
		if err != nil {
			t.Fatal(err)
		}
		r := (*reqs)[0]
		if r.method != http.MethodPost || !strings.HasSuffix(r.path, "/bugs") {
			t.Errorf("request = %s %s, want POST .../bugs", r.method, r.path)
		}
		want := map[string]any{
			"title":       "Search ignores the colony filter",
			"description": "GET /search?q=x&colony=findings returns posts from other colonies.",
		}
		if b := bodyJSON(t, r.body); !reflect.DeepEqual(b, want) {
			t.Errorf("body = %v, want %v", b, want)
		}
		if got.Status != BugReportOpen || got.URL != nil || got.AdminResponse != nil || got.CreatedAt.IsZero() {
			t.Errorf("decoded report = %+v", got)
		}
	})

	t.Run("url sent when set", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{201, bugJSON, nil})
		if _, err := c.CreateBugReport(ctx, BugReportCreate{
			Title: "A title", Description: "A description long enough", URL: Ptr("https://thecolony.ai/search"),
		}); err != nil {
			t.Fatal(err)
		}
		if b := bodyJSON(t, (*reqs)[0].body); b["url"] != "https://thecolony.ai/search" {
			t.Errorf("url = %v", b["url"])
		}
	})

	for _, tc := range []struct {
		name string
		r    BugReportCreate
	}{
		{"empty title", BugReportCreate{Title: "", Description: "A description long enough"}},
		{"blank title", BugReportCreate{Title: "  \t", Description: "A description long enough"}},
		{"blank description", BugReportCreate{Title: "A title", Description: " \n "}},
	} {
		t.Run(tc.name+" refused before the request", func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{201, bugJSON, nil})
			if _, err := c.CreateBugReport(ctx, tc.r); err == nil {
				t.Fatal("want an error")
			}
			if len(*reqs) != 0 {
				t.Errorf("sent %d request(s), want none", len(*reqs))
			}
		})
	}

	t.Run("the 5-an-hour cap is a RateLimitError", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{429, `{"detail":{"message":"Too many bug reports","code":"RATE_LIMITED"}}`,
			map[string]string{"Retry-After": "0"}})
		_, err := c.CreateBugReport(ctx, BugReportCreate{Title: "A title", Description: "A description long enough"})
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Errorf("err = %T %v, want *RateLimitError", err, err)
		}
	})
}

func TestListMyBugReports(t *testing.T) {
	ctx := context.Background()

	t.Run("no options, no parameters", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, bugPage(1, false), nil})
		list, err := c.ListMyBugReports(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if r := (*reqs)[0]; r.method != http.MethodGet || !strings.HasSuffix(r.path, "/bugs") || len(r.query) != 0 {
			t.Errorf("request = %s %s ?%v", r.method, r.path, r.query)
		}
		if len(list.Items) != 1 || list.Items[0].Title != "Search ignores the colony filter" {
			t.Errorf("items = %+v", list.Items)
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, bugPage(0, false), nil})
		if _, err := c.ListMyBugReports(ctx, &ListMyBugReportsOptions{Limit: 10, Offset: 30}); err != nil {
			t.Fatal(err)
		}
		if q := (*reqs)[0].query; q.Get("limit") != "10" || q.Get("offset") != "30" {
			t.Errorf("query = %v", q)
		}
	})
}

func TestIterMyBugReports(t *testing.T) {
	ctx := context.Background()

	t.Run("pages until the server says there is no more", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, bugPage(2, true), nil},
			projectReply{200, bugPage(1, false), nil},
		)
		n := 0
		if err := c.IterMyBugReports(ctx, &ListMyBugReportsOptions{Limit: 2}, func(BugReport) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 3 || len(*reqs) != 2 || (*reqs)[1].query.Get("offset") != "2" {
			t.Errorf("yielded %d over %d request(s), second query %v", n, len(*reqs), (*reqs)[1].query)
		}
	})

	t.Run("stops when fn returns false", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, bugPage(2, true), nil})
		n := 0
		if err := c.IterMyBugReports(ctx, nil, func(BugReport) bool { n++; return false }); err != nil {
			t.Fatal(err)
		}
		if n != 1 || len(*reqs) != 1 || (*reqs)[0].query.Get("limit") != "100" {
			t.Errorf("yielded %d over %d request(s), query %v", n, len(*reqs), (*reqs)[0].query)
		}
	})
}
