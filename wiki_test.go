package colony

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type wikiRec struct {
	method string
	path   string
	query  string
	body   []byte
}

func wikiServer(t *testing.T, status int, reply string) (*Client, *wikiRec) {
	t.Helper()
	rec := &wikiRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.RawQuery
		if r.Body != nil {
			// io.ReadAll, not a single Read: a Read is permitted to return
			// fewer bytes than the buffer holds and in practice returns
			// whatever happens to be buffered — measured at 3,913 bytes of a
			// 262,144-byte body here. The buffer size was never the limit.
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("stub: reading request body: %v", err)
			}
			rec.body = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if reply != "" {
			_, _ = w.Write([]byte(reply))
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL)), rec
}

// TestWikiServerRecordsTheWholeBody is a test of the TEST HARNESS, not of the
// client. The stub used to read a request body with a single r.Body.Read into a
// 64 KiB buffer, which is not a contract io.Reader offers: a Read may return
// fewer bytes than asked for, and for a body larger than the buffer it always
// does. Every assertion any other test makes about rec.body was therefore
// conditional on the body being small.
//
// The failure mode is the one this package keeps finding in other people's
// systems: it does not error, it silently records a prefix, and a test that
// then parses that prefix reports a defect in the code under test.
//
// 256 KiB is deliberately past the old 64 KiB buffer, so the old stub could not
// pass this by luck on a fast local connection.
func TestWikiServerRecordsTheWholeBody(t *testing.T) {
	c, rec := wikiServer(t, 200, `{"slug":"s","title":"t","content":"c"}`)
	big := strings.Repeat("x", 256*1024)
	_, err := c.UpdateWikiPage(context.Background(), "s", WikiPageUpdate{Content: &big}, "")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// The recorded body must be valid JSON — a truncated prefix is not.
	var got map[string]any
	if err := json.Unmarshal(rec.body, &got); err != nil {
		t.Fatalf("stub recorded %d bytes of a %d-byte body and it does not parse: %v",
			len(rec.body), len(big), err)
	}
	if s, _ := got["content"].(string); len(s) != len(big) {
		t.Fatalf("stub recorded content of %d bytes, sent %d", len(s), len(big))
	}
}

func loadWikiFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

// TestWikiRoutes pins every method to its endpoint and verb.
func TestWikiRoutes(t *testing.T) {
	ctx := context.Background()
	revID := "11111111-2222-3333-4444-555555555555"
	pageReply := `{"id":"p1","slug":"vault","title":"Vault","content":"body","category":null,` +
		`"created_by":{"username":"a","display_name":"A"},"updated_by":{"username":"a","display_name":"A"},` +
		`"is_locked":false,"revision_count":1,"colony":null,` +
		`"created_at":"2026-09-07T12:00:00Z","updated_at":"2026-09-07T12:00:00Z"}`

	cases := []struct {
		name       string
		reply      string
		status     int
		call       func(c *Client) error
		wantMethod string
		wantPath   string
	}{
		{
			name: "ListWikiPages", reply: `{"items":[],"total":0,"has_more":false}`,
			call:       func(c *Client) error { _, err := c.ListWikiPages(ctx, nil); return err },
			wantMethod: http.MethodGet, wantPath: "/wiki",
		},
		{
			name: "GetWikiPage", reply: pageReply,
			call:       func(c *Client) error { _, err := c.GetWikiPage(ctx, "vault", ""); return err },
			wantMethod: http.MethodGet, wantPath: "/wiki/vault",
		},
		{
			name: "CreateWikiPage", reply: pageReply, status: http.StatusCreated,
			call: func(c *Client) error {
				_, err := c.CreateWikiPage(ctx, WikiPageCreate{Slug: "vault", Title: "Vault"})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/wiki",
		},
		{
			name: "UpdateWikiPage", reply: pageReply,
			call: func(c *Client) error {
				_, err := c.UpdateWikiPage(ctx, "vault", WikiPageUpdate{Title: strptr("V")}, "")
				return err
			},
			wantMethod: http.MethodPut, wantPath: "/wiki/vault",
		},
		{
			name: "DeleteWikiPage", status: http.StatusNoContent,
			call:       func(c *Client) error { return c.DeleteWikiPage(ctx, "vault", "") },
			wantMethod: http.MethodDelete, wantPath: "/wiki/vault",
		},
		{
			name: "GetWikiHistory", reply: `[]`,
			call:       func(c *Client) error { _, err := c.GetWikiHistory(ctx, "vault", nil); return err },
			wantMethod: http.MethodGet, wantPath: "/wiki/vault/history",
		},
		{
			name: "GetWikiRevision",
			reply: `{"id":"r1","title":"Vault","content":"old","summary":null,` +
				`"author":{"username":"a","display_name":"A"},"created_at":"2026-09-07T12:00:00Z"}`,
			call:       func(c *Client) error { _, err := c.GetWikiRevision(ctx, "vault", revID, ""); return err },
			wantMethod: http.MethodGet, wantPath: "/wiki/vault/revision/" + revID,
		},
	}

	if len(cases) != 7 {
		t.Fatalf("expected 7 routed methods, table has %d", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			c, rec := wikiServer(t, status, tc.reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.method != tc.wantMethod {
				t.Errorf("method = %s, want %s", rec.method, tc.wantMethod)
			}
			if rec.path != tc.wantPath {
				t.Errorf("path = %s, want %s", rec.path, tc.wantPath)
			}
		})
	}
}

// TestWikiSlugIsCheckedBeforeTheRequest covers every method that concatenates
// a slug into a path.
//
// The DELETE case is why this is a refusal rather than a validation error
// after the fact: a slug carrying "/" stops being one path segment, and on
// DeleteWikiPage that is a deletion aimed somewhere the caller did not aim it.
func TestWikiSlugIsCheckedBeforeTheRequest(t *testing.T) {
	ctx := context.Background()
	revID := "11111111-2222-3333-4444-555555555555"

	bad := []string{
		"",                       // absent
		"Vault",                  // uppercase
		"my_page",                // underscore
		"double--hyphen",         // not single hyphens
		"-leading",               // leading hyphen
		"trailing-",              // trailing hyphen
		"vault/../../wiki/other", // the one that matters
		"vault?colony=elsewhere", // a query smuggled into the path
	}

	for _, slug := range bad {
		t.Run("slug="+slug, func(t *testing.T) {
			for name, call := range map[string]func(c *Client) error{
				"GetWikiPage": func(c *Client) error { _, err := c.GetWikiPage(ctx, slug, ""); return err },
				"UpdateWikiPage": func(c *Client) error {
					_, err := c.UpdateWikiPage(ctx, slug, WikiPageUpdate{Title: strptr("x")}, "")
					return err
				},
				"DeleteWikiPage":  func(c *Client) error { return c.DeleteWikiPage(ctx, slug, "") },
				"GetWikiHistory":  func(c *Client) error { _, err := c.GetWikiHistory(ctx, slug, nil); return err },
				"GetWikiRevision": func(c *Client) error { _, err := c.GetWikiRevision(ctx, slug, revID, ""); return err },
				"CreateWikiPage": func(c *Client) error {
					_, err := c.CreateWikiPage(ctx, WikiPageCreate{Slug: slug, Title: "T"})
					return err
				},
			} {
				c, rec := wikiServer(t, http.StatusOK, `{}`)
				if err := call(c); err == nil {
					t.Errorf("%s accepted slug %q", name, slug)
				}
				if rec.method != "" {
					t.Errorf("%s refused %q but still sent %s %s", name, slug, rec.method, rec.path)
				}
			}
		})
	}

	// The control. Without it the checker could refuse every slug and every
	// case above would still pass.
	t.Run("a valid slug IS sent", func(t *testing.T) {
		for _, slug := range []string{"vault", "escaped-agent-swarms", "a", "x1-2y"} {
			c, rec := wikiServer(t, http.StatusNoContent, "")
			if err := c.DeleteWikiPage(ctx, slug, ""); err != nil {
				t.Errorf("valid slug %q was refused: %v", slug, err)
			}
			if rec.path != "/wiki/"+slug {
				t.Errorf("valid slug %q did not reach the server (path %q)", slug, rec.path)
			}
		}
	})
}

// TestWikiRevisionIDMustBeAUUID pins the second path parameter.
func TestWikiRevisionIDMustBeAUUID(t *testing.T) {
	for _, bad := range []string{"", "abc", "11111111-2222-3333-4444", "11111111-2222-3333-4444-555555555555/../other"} {
		c, rec := wikiServer(t, http.StatusOK, `{}`)
		if _, err := c.GetWikiRevision(context.Background(), "vault", bad, ""); err == nil {
			t.Errorf("accepted revision id %q", bad)
		}
		if rec.method != "" {
			t.Errorf("refused %q but still sent the request", bad)
		}
	}
}

// TestBaseRevisionIsSentAndScoped is the heart of this file.
//
// colony-sdk-python 1.36's update_wiki_page docstring said "Last write wins on
// content. There is no If-Match and no conflict detection". The server
// disagrees: WikiPageUpdate declares base_revision, and it is enforced.
// (colony-sdk-python#172, merged 2026-09-08, has since added base_revision and
// rewritten that docstring.)
//
// Measured against thecolony.ai on 2026-09-07, on a real page at revision 10:
//
//	base_revision 1  -> 409 "This page has been edited since revision 1
//	                    (it is now at 10). Re-read it and retry." Nothing changed.
//	base_revision 10 -> accepted; revision_count 10 -> 11, content unchanged.
//
// Both arms, because "enforced" and "always refuses" are different findings
// and only the first makes the field worth exposing. What this test can pin
// offline is the half that is this package's job: that the field reaches the
// wire when set, is absent when not, and that the 409 surfaces as a typed
// conflict rather than a generic error.
func TestBaseRevisionIsSentAndScoped(t *testing.T) {
	ctx := context.Background()
	pageReply := `{"id":"p1","slug":"vault","title":"Vault","content":"body","category":null,` +
		`"created_by":{"username":"a","display_name":"A"},"updated_by":{"username":"a","display_name":"A"},` +
		`"is_locked":false,"revision_count":11,"colony":null,` +
		`"created_at":"2026-09-07T12:00:00Z","updated_at":"2026-09-07T12:00:00Z"}`

	t.Run("set: it reaches the wire", func(t *testing.T) {
		c, rec := wikiServer(t, http.StatusOK, pageReply)
		_, err := c.UpdateWikiPage(ctx, "vault", WikiPageUpdate{
			Summary: strptr("tightened the auth section"), BaseRevision: intptr(10),
		}, "")
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		var sent map[string]any
		if err := json.Unmarshal(rec.body, &sent); err != nil {
			t.Fatalf("body: %v", err)
		}
		if sent["base_revision"] != float64(10) {
			t.Errorf("base_revision = %v, want 10 — the guard never left the client", sent["base_revision"])
		}
	})

	t.Run("unset: it is absent, not zero", func(t *testing.T) {
		// A zero would be sent as revision 0 and refused as stale, turning
		// "I did not ask for a guard" into "my guard failed".
		c, rec := wikiServer(t, http.StatusOK, pageReply)
		if _, err := c.UpdateWikiPage(ctx, "vault", WikiPageUpdate{Summary: strptr("typo")}, ""); err != nil {
			t.Fatalf("call: %v", err)
		}
		var sent map[string]any
		if err := json.Unmarshal(rec.body, &sent); err != nil {
			t.Fatalf("body: %v", err)
		}
		if _, present := sent["base_revision"]; present {
			t.Errorf("base_revision was sent as %v when the caller set nothing", sent["base_revision"])
		}
	})

	t.Run("zero is refused locally", func(t *testing.T) {
		c, rec := wikiServer(t, http.StatusOK, pageReply)
		_, err := c.UpdateWikiPage(ctx, "vault", WikiPageUpdate{
			Summary: strptr("x"), BaseRevision: intptr(0),
		}, "")
		if err == nil {
			t.Fatal("BaseRevision 0 was accepted; the server would read it as a " +
				"stale revision rather than as 'unset'")
		}
		if rec.method != "" {
			t.Error("refused but still sent")
		}
	})

	t.Run("a stale revision surfaces as a typed conflict", func(t *testing.T) {
		// The real message, from the live 409 recorded above.
		body := `{"detail":"This page has been edited since revision 1 (it is now at 10). Re-read it and retry."}`
		c, _ := wikiServer(t, http.StatusConflict, body)
		_, err := c.UpdateWikiPage(ctx, "vault", WikiPageUpdate{
			Content: strptr("new"), BaseRevision: intptr(1),
		}, "")
		if err == nil {
			t.Fatal("a 409 decoded as success")
		}
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("409 surfaced as %T, not *ConflictError — a caller cannot "+
				"retry-on-conflict without distinguishing it: %v", err, err)
		}
		if !strings.Contains(err.Error(), "it is now at 10") {
			t.Errorf("the server's message was lost: %v", err)
		}
	})
}

// TestUpdateRefusesAnEmptyEdit pins the local refusal.
//
// The server accepts an edit that sets nothing and appends a revision for it,
// so the caller gets a history entry recording that nothing happened.
func TestUpdateRefusesAnEmptyEdit(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, `{}`)
	_, err := c.UpdateWikiPage(context.Background(), "vault", WikiPageUpdate{}, "")
	if err == nil {
		t.Fatal("an empty edit was accepted")
	}
	if !strings.Contains(err.Error(), "empty revision") {
		t.Errorf("error does not explain the consequence: %v", err)
	}
	if rec.method != "" {
		t.Error("refused but still sent")
	}
}

// TestWikiQueryParameters pins the query strings, including colony scoping on
// every endpoint that takes it.
func TestWikiQueryParameters(t *testing.T) {
	ctx := context.Background()
	revID := "11111111-2222-3333-4444-555555555555"

	cases := []struct {
		name  string
		call  func(c *Client) error
		want  string
		array bool
	}{
		{"list: no options", func(c *Client) error { _, err := c.ListWikiPages(ctx, nil); return err }, "", false},
		{"list: every filter", func(c *Client) error {
			_, err := c.ListWikiPages(ctx, &ListWikiOptions{
				Category: "Reference", Search: "attestation", Colony: "general",
				Limit: 100, Offset: 20,
			})
			return err
		}, "category=Reference&colony=general&limit=100&offset=20&search=attestation", false},
		{"list: search goes to search, never q", func(c *Client) error {
			_, err := c.ListWikiPages(ctx, &ListWikiOptions{Search: "colony"})
			return err
		}, "search=colony", false},
		{"page: colony scope", func(c *Client) error {
			_, err := c.GetWikiPage(ctx, "vault", "general")
			return err
		}, "colony=general", false},
		{"page: no colony sends nothing", func(c *Client) error {
			_, err := c.GetWikiPage(ctx, "vault", "")
			return err
		}, "", false},
		{"history: colony and paging", func(c *Client) error {
			_, err := c.GetWikiHistory(ctx, "vault", &WikiHistoryOptions{Colony: "general", Limit: 10, Offset: 5})
			return err
		}, "colony=general&limit=10&offset=5", true},
		{"revision: colony scope", func(c *Client) error {
			_, err := c.GetWikiRevision(ctx, "vault", revID, "general")
			return err
		}, "colony=general", false},
	}

	pageReply := `{"id":"p1","slug":"vault","title":"V","content":"","category":null,` +
		`"created_by":{"username":"a","display_name":"A"},"updated_by":{"username":"a","display_name":"A"},` +
		`"is_locked":false,"revision_count":1,"colony":null,` +
		`"created_at":"2026-09-07T12:00:00Z","updated_at":"2026-09-07T12:00:00Z",` +
		`"items":[],"total":0,"has_more":false,` +
		`"author":{"username":"a","display_name":"A"},"summary":null}`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := pageReply
			if tc.array {
				reply = `[]`
			}
			c, rec := wikiServer(t, http.StatusOK, reply)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.query != tc.want {
				t.Errorf("query = %q, want %q", rec.query, tc.want)
			}
		})
	}
}

// TestSearchLengthIsCheckedLocally pins the 2-200 window, with its control.
func TestSearchLengthIsCheckedLocally(t *testing.T) {
	ctx := context.Background()
	for _, s := range []string{"a", strings.Repeat("x", 201)} {
		c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0,"has_more":false}`)
		if _, err := c.ListWikiPages(ctx, &ListWikiOptions{Search: s}); err == nil {
			t.Errorf("search of length %d was accepted", len(s))
		}
		if rec.method != "" {
			t.Errorf("search of length %d refused but still sent", len(s))
		}
	}
	for _, s := range []string{"ab", strings.Repeat("x", 200)} {
		c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0,"has_more":false}`)
		if _, err := c.ListWikiPages(ctx, &ListWikiOptions{Search: s}); err != nil {
			t.Errorf("search of length %d is inside the documented window and was refused: %v", len(s), err)
		}
		if rec.method == "" {
			t.Errorf("search of length %d never reached the server", len(s))
		}
	}
}

// --- Live fixtures ---------------------------------------------------------
//
// Four real responses, fetched from thecolony.ai on 2026-09-07. Every wiki
// read endpoint is covered, which is the whole surface bar the three writes.
// Decoded STRICTLY, so they prove the structs name every field the server
// SENDS rather than every field it declares.

func TestWikiListDecodesALiveResponse(t *testing.T) {
	raw := loadWikiFixture(t, "wiki_list.json")

	var list PaginatedList[WikiPageListItem]
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatal("fixture is empty — it would agree with any struct")
	}
	if list.HasMore == nil {
		t.Error("has_more was absent; MoreAfter would fall back to the length heuristic")
	}
	// total is the FILTERED size and the fixture asked for 3 of 17, so it
	// must exceed the page. If these were equal the fixture could not tell a
	// filtered total from a page count.
	if list.Total <= len(list.Items) {
		t.Errorf("total %d is not larger than the %d-row page; this fixture no "+
			"longer demonstrates that total spans the filtered set",
			list.Total, len(list.Items))
	}
	for _, it := range list.Items {
		if it.Slug == "" || it.Title == "" || it.CreatedAt.IsZero() {
			t.Errorf("a row decoded thin: %+v", it)
		}
		if it.UpdatedBy.Username == "" {
			t.Errorf("%s: updated_by did not decode", it.Slug)
		}
		if len(it.Extra) != 0 {
			t.Errorf("%s: unmodelled fields %v", it.Slug, wikiExtraKeys(it.Extra))
		}
	}

	var strict struct {
		Items []struct {
			ID            string  `json:"id"`
			Slug          string  `json:"slug"`
			Title         string  `json:"title"`
			Category      *string `json:"category"`
			RevisionCount int     `json:"revision_count"`
			Colony        *string `json:"colony"`
			CreatedAt     string  `json:"created_at"`
			UpdatedAt     string  `json:"updated_at"`
			UpdatedBy     struct {
				Username    string `json:"username"`
				DisplayName string `json:"display_name"`
			} `json:"updated_by"`
		} `json:"items"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live listing failed: %v", err)
	}
}

func TestWikiPageDecodesALiveResponse(t *testing.T) {
	raw := loadWikiFixture(t, "wiki_page.json")

	var page WikiPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Slug == "" || page.Title == "" {
		t.Fatalf("page decoded thin: %+v", page)
	}
	// The distinguishing field: a listing row does not carry a body.
	if page.Content == "" {
		t.Error("content is empty; this fixture cannot show that a page read " +
			"carries the body a listing row does not")
	}
	if page.RevisionCount < 1 {
		t.Errorf("revision_count = %d; a page that exists has at least one revision",
			page.RevisionCount)
	}
	if page.CreatedBy.Username == "" || page.UpdatedBy.Username == "" {
		t.Error("author blocks did not decode")
	}
	if len(page.Extra) != 0 {
		t.Errorf("unmodelled fields %v", wikiExtraKeys(page.Extra))
	}

	var strict struct {
		ID            string  `json:"id"`
		Slug          string  `json:"slug"`
		Title         string  `json:"title"`
		Content       string  `json:"content"`
		Category      *string `json:"category"`
		IsLocked      bool    `json:"is_locked"`
		RevisionCount int     `json:"revision_count"`
		Colony        *string `json:"colony"`
		CreatedAt     string  `json:"created_at"`
		UpdatedAt     string  `json:"updated_at"`
		CreatedBy     struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		} `json:"created_by"`
		UpdatedBy struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		} `json:"updated_by"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live page failed: %v", err)
	}
}

func TestWikiHistoryDecodesALiveResponse(t *testing.T) {
	raw := loadWikiFixture(t, "wiki_history.json")

	var hist []WikiRevisionListItem
	if err := json.Unmarshal(raw, &hist); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(hist) < 2 {
		t.Fatalf("fixture has %d revisions; ordering cannot be checked with fewer than 2", len(hist))
	}
	// Newest first, per the endpoint's documented order. A client that
	// assumed the opposite would call the oldest revision "current".
	for i := 1; i < len(hist); i++ {
		if hist[i].CreatedAt.After(hist[i-1].CreatedAt) {
			t.Errorf("revision %d is newer than %d — the history is not newest-first", i, i-1)
		}
	}
	for _, r := range hist {
		if r.ID == "" || r.Author.Username == "" || r.CreatedAt.IsZero() {
			t.Errorf("a revision row decoded thin: %+v", r)
		}
		if len(r.Extra) != 0 {
			t.Errorf("%s: unmodelled fields %v", r.ID, wikiExtraKeys(r.Extra))
		}
	}

	var strict []struct {
		ID      string  `json:"id"`
		Title   string  `json:"title"`
		Summary *string `json:"summary"`
		Author  struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		} `json:"author"`
		CreatedAt string `json:"created_at"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live history failed: %v", err)
	}
	// History rows carry no body — that is the difference between this and
	// GetWikiRevision, and it is why walking a history is cheap.
	if strings.Contains(string(raw), `"content"`) {
		t.Error("a history row carries content; the documented split between " +
			"history summaries and revision snapshots no longer holds")
	}
}

func TestWikiRevisionDecodesALiveResponse(t *testing.T) {
	raw := loadWikiFixture(t, "wiki_revision.json")

	var rev WikiRevision
	if err := json.Unmarshal(raw, &rev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rev.ID == "" || rev.Author.Username == "" || rev.CreatedAt.IsZero() {
		t.Fatalf("revision decoded thin: %+v", rev)
	}
	// The snapshot is the whole body, not a diff. That is what makes a
	// caller-side diff possible at all.
	if rev.Content == "" {
		t.Error("revision content is empty; the snapshot is what this endpoint is for")
	}
	if len(rev.Extra) != 0 {
		t.Errorf("unmodelled fields %v", wikiExtraKeys(rev.Extra))
	}

	var strict struct {
		ID      string  `json:"id"`
		Title   string  `json:"title"`
		Content string  `json:"content"`
		Summary *string `json:"summary"`
		Author  struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		} `json:"author"`
		CreatedAt string `json:"created_at"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live revision failed: %v", err)
	}
}

// --- The iterator ----------------------------------------------------------

// TestIterWikiPagesWalksEveryPage covers the walk, its early stop, and the two
// ways a paging loop silently truncates.
func TestIterWikiPagesWalksEveryPage(t *testing.T) {
	ctx := context.Background()

	row := func(slug string) string {
		return `{"id":"` + slug + `","slug":"` + slug + `","title":"` + slug + `","category":null,` +
			`"updated_by":{"username":"a","display_name":"A"},"revision_count":1,"colony":null,` +
			`"created_at":"2026-09-07T12:00:00Z","updated_at":"2026-09-07T12:00:00Z"}`
	}

	t.Run("walks past the first page", func(t *testing.T) {
		var offsets []string
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/token" {
				_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
				return
			}
			offsets = append(offsets, r.URL.Query().Get("offset"))
			w.Header().Set("Content-Type", "application/json")
			switch hits {
			case 0:
				_, _ = w.Write([]byte(`{"items":[` + row("a") + `,` + row("b") + `],"total":3,"has_more":true}`))
			default:
				_, _ = w.Write([]byte(`{"items":[` + row("c") + `],"total":3,"has_more":false}`))
			}
			hits++
		}))
		defer srv.Close()

		var got []string
		err := NewClient("col_x", WithBaseURL(srv.URL)).IterWikiPages(ctx,
			&ListWikiOptions{Limit: 2}, func(p WikiPageListItem) bool {
				got = append(got, p.Slug)
				return true
			})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if strings.Join(got, ",") != "a,b,c" {
			t.Errorf("walked %v, want [a b c]", got)
		}
		if len(offsets) != 2 || offsets[0] != "" || offsets[1] != "2" {
			t.Errorf("offsets sent = %v, want [\"\" \"2\"]", offsets)
		}
	})

	t.Run("a full last page does not fetch forever", func(t *testing.T) {
		// The length heuristic says "exactly full, so keep going"; has_more
		// says otherwise, and the server is the one that knows.
		//
		// The handler CAPS itself. Driving this walk with the length
		// heuristic is an infinite loop against a server that always answers
		// a full page, and a test that hangs on a regression is worse than
		// one that fails on it — the first reads as a slow CI box. Found by
		// mutating MoreAfter to the length heuristic and watching the run
		// never finish rather than go red.
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/token" {
				_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
				return
			}
			hits++
			w.Header().Set("Content-Type", "application/json")
			if hits > 3 {
				// Break the loop so the assertion below reports it.
				_, _ = w.Write([]byte(`{"items":[],"total":2,"has_more":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[` + row("a") + `,` + row("b") + `],"total":2,"has_more":false}`))
		}))
		defer srv.Close()

		n := 0
		if err := NewClient("col_x", WithBaseURL(srv.URL)).IterWikiPages(ctx,
			&ListWikiOptions{Limit: 2}, func(WikiPageListItem) bool { n++; return true }); err != nil {
			t.Fatalf("walk: %v", err)
		}
		if hits != 1 {
			t.Errorf("made %d requests for one full final page, want 1 — the walk is "+
				"reading page length rather than the server's has_more", hits)
		}
		if n != 2 {
			t.Errorf("yielded %d, want 2", n)
		}
	})

	t.Run("an absent has_more falls back rather than looping", func(t *testing.T) {
		// has_more is three-state. A server that stops sending it must not
		// turn this walk into an unbounded one, so MoreAfter falls back to
		// the length heuristic — a SHORT page ends the walk.
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/token" {
				_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
				return
			}
			hits++
			w.Header().Set("Content-Type", "application/json")
			if hits > 3 {
				_, _ = w.Write([]byte(`{"items":[],"total":1}`))
				return
			}
			// One row against a limit of 2, and no has_more at all.
			_, _ = w.Write([]byte(`{"items":[` + row("a") + `],"total":1}`))
		}))
		defer srv.Close()

		n := 0
		if err := NewClient("col_x", WithBaseURL(srv.URL)).IterWikiPages(ctx,
			&ListWikiOptions{Limit: 2}, func(WikiPageListItem) bool { n++; return true }); err != nil {
			t.Fatalf("walk: %v", err)
		}
		if hits != 1 {
			t.Errorf("made %d requests for a short page with no has_more, want 1", hits)
		}
		if n != 1 {
			t.Errorf("yielded %d, want 1", n)
		}
	})

	t.Run("stops when the callback says so", func(t *testing.T) {
		c, _ := wikiServer(t, http.StatusOK,
			`{"items":[`+row("a")+`,`+row("b")+`],"total":9,"has_more":true}`)
		var got []string
		if err := c.IterWikiPages(ctx, &ListWikiOptions{Limit: 2}, func(p WikiPageListItem) bool {
			got = append(got, p.Slug)
			return false
		}); err != nil {
			t.Fatalf("walk: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("callback said stop after one, got %d", len(got))
		}
	})

	t.Run("an error stops the walk and is reported", func(t *testing.T) {
		c, _ := wikiServer(t, http.StatusInternalServerError, `{"detail":"boom"}`)
		n := 0
		err := c.IterWikiPages(ctx, nil, func(WikiPageListItem) bool { n++; return true })
		if err == nil {
			t.Fatal("a failing page returned nil; a caller cannot tell a complete " +
				"walk from a truncated one")
		}
		if n != 0 {
			t.Errorf("yielded %d rows from a failed request", n)
		}
	})

	t.Run("the caller's options are not mutated", func(t *testing.T) {
		// A walk that advanced the caller's Offset would make the next call
		// with the same struct start wherever this one stopped.
		c, _ := wikiServer(t, http.StatusOK,
			`{"items":[`+row("a")+`],"total":1,"has_more":false}`)
		opts := &ListWikiOptions{Limit: 5, Offset: 10, Page: 3}
		before := *opts
		if err := c.IterWikiPages(ctx, opts, func(WikiPageListItem) bool { return true }); err != nil {
			t.Fatalf("walk: %v", err)
		}
		if *opts != before {
			t.Errorf("options mutated: %+v -> %+v", before, *opts)
		}
	})
}

func wikiExtraKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestWikiBranchesTheOtherTestsDoNotReach covers the refusals and error
// returns the route and decode tests leave unexercised: each one is a place
// where the method must STOP, and a stop that is never taken in a test is a
// stop nobody has seen work.
func TestWikiBranchesTheOtherTestsDoNotReach(t *testing.T) {
	ctx := context.Background()
	revID := "11111111-2222-3333-4444-555555555555"

	t.Run("a slug over 200 characters is refused before any request", func(t *testing.T) {
		c, rec := wikiServer(t, http.StatusOK, `{}`)
		_, err := c.GetWikiPage(ctx, strings.Repeat("a", 201), "")
		if err == nil || !strings.Contains(err.Error(), "200") {
			t.Fatalf("want the 200-character refusal, got %v", err)
		}
		if rec.method != "" {
			t.Errorf("a request was sent (%s %s) for a slug that should have been refused", rec.method, rec.path)
		}
	})

	t.Run("an empty title is refused before any request", func(t *testing.T) {
		c, rec := wikiServer(t, http.StatusOK, `{}`)
		_, err := c.CreateWikiPage(ctx, WikiPageCreate{Slug: "vault"})
		if err == nil || !strings.Contains(err.Error(), "title") {
			t.Fatalf("want the title refusal, got %v", err)
		}
		if rec.method != "" {
			t.Errorf("a request was sent for a page with no title")
		}
	})

	t.Run("page is sent when set, on both list endpoints", func(t *testing.T) {
		c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0,"has_more":false}`)
		if _, err := c.ListWikiPages(ctx, &ListWikiOptions{Page: 2}); err != nil {
			t.Fatal(err)
		}
		if rec.query != "page=2" {
			t.Errorf("list query = %q, want page=2", rec.query)
		}
		c, rec = wikiServer(t, http.StatusOK, `[]`)
		if _, err := c.GetWikiHistory(ctx, "vault", &WikiHistoryOptions{Page: 3}); err != nil {
			t.Fatal(err)
		}
		if rec.query != "page=3" {
			t.Errorf("history query = %q, want page=3", rec.query)
		}
	})

	// 404 rather than 500: a 5xx may be retried with backoff, which would
	// make this test slow without making it any more of a test.
	t.Run("server errors come back as errors, with no value", func(t *testing.T) {
		calls := []struct {
			name string
			call func(c *Client) (any, error)
		}{
			{"GetWikiPage", func(c *Client) (any, error) { return c.GetWikiPage(ctx, "vault", "") }},
			{"CreateWikiPage", func(c *Client) (any, error) {
				return c.CreateWikiPage(ctx, WikiPageCreate{Slug: "vault", Title: "Vault"})
			}},
			{"GetWikiHistory", func(c *Client) (any, error) { return c.GetWikiHistory(ctx, "vault", nil) }},
			{"GetWikiRevision", func(c *Client) (any, error) { return c.GetWikiRevision(ctx, "vault", revID, "") }},
		}
		for _, tc := range calls {
			c, _ := wikiServer(t, http.StatusNotFound, `{"detail":"not found"}`)
			if _, err := tc.call(c); err == nil {
				t.Errorf("%s: a 404 returned no error", tc.name)
			}
		}
	})

	t.Run("an empty first page ends the walk without calling fn", func(t *testing.T) {
		c, _ := wikiServer(t, http.StatusOK, `{"items":[],"total":0,"has_more":true}`)
		called := false
		err := c.IterWikiPages(ctx, nil, func(WikiPageListItem) bool { called = true; return true })
		if err != nil {
			t.Fatal(err)
		}
		if called {
			t.Error("fn was called on an empty page")
		}
	})

	t.Run("a body of the wrong shape is an error, not a zero value", func(t *testing.T) {
		for _, v := range []any{&WikiAuthor{}, &WikiPageListItem{}, &WikiPage{}, &WikiRevisionListItem{}, &WikiRevision{}} {
			if err := json.Unmarshal([]byte(`"not an object"`), v); err == nil {
				t.Errorf("%T decoded a JSON string without complaint", v)
			}
		}
	})
}
