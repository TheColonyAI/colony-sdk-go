package colony

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Paging (platform release 2026-11-14d): GET /posts/{post_id}/context takes
// limit and offset for its comments, and GET /puzzles takes limit and offset
// and answers a real total and has_more. Each sends exactly what was set.

const ctxPostID = "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"

func TestGetPostContextPaging(t *testing.T) {
	ctx := context.Background()
	reply := projectReply{200, `{"post":{"id":"` + ctxPostID + `"},"comments":[],"comments_truncated":true}`, nil}

	t.Run("no options send no parameters", func(t *testing.T) {
		c, reqs := projectServer(t, reply)
		out, err := c.GetPostContext(ctx, ctxPostID)
		if err != nil {
			t.Fatal(err)
		}
		if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/posts/"+ctxPostID+"/context" || len(r.query) != 0 {
			t.Errorf("request = %s %s ?%v", r.method, r.raw, r.query)
		}
		if out["comments_truncated"] != true {
			t.Errorf("comments_truncated = %v", out["comments_truncated"])
		}
	})

	for name, opts := range map[string]*PostContextOptions{"nil options": nil, "zero options": {}} {
		t.Run(name+" send no parameters", func(t *testing.T) {
			c, reqs := projectServer(t, reply)
			if _, err := c.GetPostContext(ctx, ctxPostID, opts); err != nil {
				t.Fatal(err)
			}
			if q := (*reqs)[0].query; len(q) != 0 {
				t.Errorf("query = %v, want none", q)
			}
		})
	}

	t.Run("limit and offset", func(t *testing.T) {
		c, reqs := projectServer(t, reply)
		if _, err := c.GetPostContext(ctx, ctxPostID, &PostContextOptions{Limit: 20, Offset: 40}); err != nil {
			t.Fatal(err)
		}
		r := (*reqs)[0]
		want := url.Values{"limit": {"20"}, "offset": {"40"}}
		if r.raw != "/posts/"+ctxPostID+"/context" || !reflect.DeepEqual(r.query, want) {
			t.Errorf("request = %s ?%v, want ?%v", r.raw, r.query, want)
		}
	})

	t.Run("limit alone", func(t *testing.T) {
		c, reqs := projectServer(t, reply)
		if _, err := c.GetPostContext(ctx, ctxPostID, &PostContextOptions{Limit: 100}); err != nil {
			t.Fatal(err)
		}
		if q := (*reqs)[0].query; !reflect.DeepEqual(q, url.Values{"limit": {"100"}}) {
			t.Errorf("query = %v", q)
		}
	})

	t.Run("two options are refused before any request", func(t *testing.T) {
		c, reqs := projectServer(t, reply)
		_, err := c.GetPostContext(ctx, ctxPostID, &PostContextOptions{Limit: 1}, &PostContextOptions{Limit: 2})
		if err == nil || !strings.Contains(err.Error(), "at most one") {
			t.Errorf("error = %v", err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%d requests sent", len(*reqs))
		}
	})
}

// puzzleJSON is a puzzle by an agent in a colony, which nobody has solved and
// the caller has started; seededPuzzleJSON is a site-wide puzzle the platform
// seeded, solved by two accounts.
const (
	puzzleID         = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	seededPuzzleID   = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
	puzzleJSON       = `{"id":"` + puzzleID + `","slug":"river-crossing","title":"River crossing","description":"Get them all across.","puzzle_type":"logic","difficulty":3,"is_active":true,"created_at":"2026-11-14T08:00:00Z","author":{"username":"riddler","display_name":"The Riddler"},"colony_name":"puzzles","attempt_status":"in_progress","solver_count":0,"best_time":null}`
	seededPuzzleJSON = `{"id":"` + seededPuzzleID + `","slug":"caesar","title":"Caesar","description":"Shifted.","puzzle_type":"cipher","difficulty":1,"is_active":true,"created_at":"2026-01-01T00:00:00Z","author":null,"colony_name":null,"attempt_status":"solved","solver_count":2,"best_time":41.5}`
)

func TestListPuzzles(t *testing.T) {
	ctx := context.Background()
	page := `{"items":[` + puzzleJSON + `,` + seededPuzzleJSON + `],"total":2,"has_more":false}`

	for name, opts := range map[string]*ListPuzzlesOptions{"nil options": nil, "zero options": {}} {
		t.Run(name+" send no parameters", func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{200, page, nil})
			list, err := c.ListPuzzles(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/puzzles" || len(r.query) != 0 || len(r.body) != 0 {
				t.Errorf("request = %s %s ?%v %q", r.method, r.raw, r.query, r.body)
			}
			if list.Total != 2 || len(list.Items) != 2 || list.HasMore == nil || *list.HasMore {
				t.Fatalf("list = %+v", list)
			}
			p, s := list.Items[0], list.Items[1]
			if p.ID != puzzleID || p.Slug != "river-crossing" || p.Title != "River crossing" ||
				p.Description != "Get them all across." || p.PuzzleType != "logic" || p.Difficulty != 3 ||
				!p.IsActive || !p.CreatedAt.Equal(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)) ||
				p.Author == nil || p.Author.Username != "riddler" || p.Author.DisplayName != "The Riddler" ||
				p.ColonyName == nil || *p.ColonyName != "puzzles" ||
				p.AttemptStatus == nil || *p.AttemptStatus != "in_progress" || p.SolverCount != 0 || p.BestTime != nil {
				t.Errorf("first puzzle = %+v", p)
			}
			if s.ID != seededPuzzleID || s.PuzzleType != "cipher" || s.Author != nil || s.ColonyName != nil ||
				s.AttemptStatus == nil || *s.AttemptStatus != "solved" || s.SolverCount != 2 ||
				s.BestTime == nil || *s.BestTime != 41.5 {
				t.Errorf("seeded puzzle = %+v", s)
			}
		})
	}

	t.Run("limit and offset", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, `{"items":[],"total":7,"has_more":false}`, nil})
		list, err := c.ListPuzzles(ctx, &ListPuzzlesOptions{Limit: 5, Offset: 5})
		if err != nil {
			t.Fatal(err)
		}
		want := url.Values{"limit": {"5"}, "offset": {"5"}}
		if q := (*reqs)[0].query; !reflect.DeepEqual(q, want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		if list.Total != 7 || list.Items == nil || len(list.Items) != 0 {
			t.Errorf("list = %+v", list)
		}
	})
}

// puzzlePage is a GET /puzzles answer of n puzzles out of total, with
// has_more as the platform computes it from offset.
func puzzlePage(n, offset, total int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = puzzleJSON
	}
	more := strconv.FormatBool(offset+n < total)
	return `{"items":[` + strings.Join(items, ",") + `],"total":` + strconv.Itoa(total) + `,"has_more":` + more + `}`
}

func TestIterPuzzles(t *testing.T) {
	ctx := context.Background()

	t.Run("pages while has_more, 100 a page, without touching the options", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, puzzlePage(100, 0, 103), nil},
			projectReply{200, puzzlePage(3, 100, 103), nil})
		opts := &ListPuzzlesOptions{}
		n := 0
		if err := c.IterPuzzles(ctx, opts, func(PuzzleListItem) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 103 || len(*reqs) != 2 {
			t.Errorf("walked %d puzzles in %d requests, want 103 in 2", n, len(*reqs))
		}
		if q := (*reqs)[0].query; q.Get("limit") != "100" || q.Get("offset") != "" {
			t.Errorf("first page query = %v", q)
		}
		if q := (*reqs)[1].query; q.Get("limit") != "100" || q.Get("offset") != "100" {
			t.Errorf("second page query = %v", q)
		}
		if *opts != (ListPuzzlesOptions{}) {
			t.Errorf("options changed to %+v", *opts)
		}
	})

	t.Run("starts at the caller's offset and limit", func(t *testing.T) {
		c, reqs := projectServer(t,
			projectReply{200, puzzlePage(10, 20, 35), nil},
			projectReply{200, puzzlePage(5, 30, 35), nil})
		n := 0
		if err := c.IterPuzzles(ctx, &ListPuzzlesOptions{Limit: 10, Offset: 20}, func(PuzzleListItem) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 15 || len(*reqs) != 2 {
			t.Errorf("walked %d in %d requests, want 15 in 2", n, len(*reqs))
		}
		if q := (*reqs)[1].query; q.Get("limit") != "10" || q.Get("offset") != "30" {
			t.Errorf("second page query = %v", q)
		}
	})

	// A platform before 2026-11-14d ignored limit and answered every active
	// puzzle with has_more false: one request, every puzzle.
	t.Run("an older platform's single page", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, puzzlePage(130, 0, 130), nil})
		n := 0
		if err := c.IterPuzzles(ctx, nil, func(PuzzleListItem) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 130 || len(*reqs) != 1 {
			t.Errorf("walked %d in %d requests, want 130 in 1", n, len(*reqs))
		}
	})

	t.Run("stops when fn says so", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, puzzlePage(3, 0, 90), nil})
		n := 0
		if err := c.IterPuzzles(ctx, &ListPuzzlesOptions{Limit: 3}, func(PuzzleListItem) bool { n++; return n < 2 }); err != nil {
			t.Fatal(err)
		}
		if n != 2 || len(*reqs) != 1 {
			t.Errorf("walked %d in %d requests, want 2 in 1", n, len(*reqs))
		}
	})

	t.Run("stops at an empty page", func(t *testing.T) {
		c, reqs := projectServer(t, projectReply{200, `{"items":[],"total":4,"has_more":true}`, nil})
		if err := c.IterPuzzles(ctx, nil, func(PuzzleListItem) bool { t.Error("fn called"); return true }); err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 1 {
			t.Errorf("%d requests, want 1", len(*reqs))
		}
	})

	t.Run("reports an error", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{401, `{"detail":{"code":"AUTH_REQUIRED_PAST_DEPTH","message":"sign in"}}`, nil})
		err := c.IterPuzzles(ctx, nil, func(PuzzleListItem) bool { return true })
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != "AUTH_REQUIRED_PAST_DEPTH" {
			t.Errorf("error = %#v, want an *AuthError AUTH_REQUIRED_PAST_DEPTH", err)
		}
	})
}
