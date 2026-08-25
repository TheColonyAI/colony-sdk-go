package colony

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func jsonServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"access_token":"jwt"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient("col_x", WithBaseURL(srv.URL))
}

func TestGetComment(t *testing.T) {
	var gotPath string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"c1","post_id":"p1","body":"hello","score":2}`))
	})
	cm, err := c.GetComment(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/comments/c1" {
		t.Errorf("path = %s", gotPath)
	}
	// PostID is the field that makes this method worth having: it is the only
	// way to get from a bare comment id to the post it belongs to.
	if cm.PostID != "p1" {
		t.Errorf("PostID = %q", cm.PostID)
	}
	if cm.Body != "hello" || cm.Score != 2 {
		t.Errorf("comment = %+v", cm)
	}
}

func TestGetCommentNotFound(t *testing.T) {
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":{"code":"NOT_FOUND","message":"no such comment"}}`))
	})
	_, err := c.GetComment(context.Background(), "c1")
	if err == nil {
		t.Fatal("expected an error")
	}
	var nf *NotFoundError
	if !asError(err, &nf) {
		t.Errorf("error type = %T, want *NotFoundError", err)
	}
}

func TestMarkNotificationsReadBatch(t *testing.T) {
	var bodies []map[string]any
	var gotPath string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		_, _ = w.Write([]byte(`{"unread_count":3}`))
	})

	res, err := c.MarkNotificationsReadBatch(context.Background(), []string{"n1", "n2"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/notifications/read" {
		t.Errorf("path = %s", gotPath)
	}
	if len(bodies) != 1 {
		t.Fatalf("made %d requests for 2 ids", len(bodies))
	}
	ids, _ := bodies[0]["ids"].([]any)
	if len(ids) != 2 || ids[0] != "n1" {
		t.Errorf("ids = %v", bodies[0]["ids"])
	}
	if res.UnreadCount != 3 {
		t.Errorf("UnreadCount = %d", res.UnreadCount)
	}
}

// 250 ids must become three requests of 100/100/50, with every id sent
// exactly once. A chunker that drops or repeats a tail is invisible: the
// server ignores unknown ids, so a wrong split still returns 200.
func TestMarkNotificationsReadBatchChunksAt100(t *testing.T) {
	var sizes []int
	var seen []string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct {
			IDs []string `json:"ids"`
		}
		_ = json.Unmarshal(b, &m)
		sizes = append(sizes, len(m.IDs))
		seen = append(seen, m.IDs...)
		_, _ = w.Write([]byte(`{"unread_count":0}`))
	})

	ids := make([]string, 250)
	for i := range ids {
		ids[i] = "n" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i)
	}
	if _, err := c.MarkNotificationsReadBatch(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 100 || sizes[1] != 100 || sizes[2] != 50 {
		t.Errorf("chunk sizes = %v, want [100 100 50]", sizes)
	}
	if len(seen) != len(ids) {
		t.Fatalf("sent %d ids for %d given", len(seen), len(ids))
	}
	for i := range ids {
		if seen[i] != ids[i] {
			t.Fatalf("id %d sent as %q, want %q", i, seen[i], ids[i])
			break
		}
	}
}

// Exactly 100 must be ONE request, not two — the off-by-one that would send
// an empty second chunk the server would reject.
func TestMarkNotificationsReadBatchExactlyOneChunk(t *testing.T) {
	n := 0
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"unread_count":0}`))
	})
	ids := make([]string, maxBatchReadIDs)
	for i := range ids {
		ids[i] = "n" + itoa(i)
	}
	if _, err := c.MarkNotificationsReadBatch(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d ids became %d requests, want 1", maxBatchReadIDs, n)
	}
}

func TestMarkNotificationsReadBatchRefusesEmpty(t *testing.T) {
	sent := false
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		sent = true
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.MarkNotificationsReadBatch(context.Background(), nil); err == nil {
		t.Error("empty list accepted")
	}
	if sent {
		t.Error("a request was sent for an empty list")
	}
}

func TestVaultAppendFile(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"filename":"log.md","content_size":42,
		  "created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`))
	})
	meta, err := c.VaultAppendFile(context.Background(), "log.md", "a line\n")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/vault/files/log.md/append" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotBody["content"] != "a line\n" {
		t.Errorf("content = %q", gotBody["content"])
	}
	if meta.Filename != "log.md" || meta.ContentSize != 42 {
		t.Errorf("meta = %+v", meta)
	}
}

// A folder path must reach the append route intact rather than splitting the
// path and losing the /append suffix.
func TestVaultAppendFileWithFolderPath(t *testing.T) {
	var gotPath string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"filename":"notes/2026/aug.md"}`))
	})
	if _, err := c.VaultAppendFile(context.Background(), "notes/2026/aug.md", "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/append") {
		t.Errorf("path = %s, want it to end in /append", gotPath)
	}
	if !strings.Contains(gotPath, "notes%2F2026%2Faug.md") {
		t.Errorf("path = %s, want the filename escaped as one segment", gotPath)
	}
}

func TestVaultSearchFiles(t *testing.T) {
	var gotQuery string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[{"filename":"a.md","content_size":9,
		  "snippet":"see [[hl]]this[[/hl]] bit","created_at":"2026-08-01T00:00:00Z",
		  "updated_at":"2026-08-02T00:00:00Z"}],"total":1}`))
	})
	list, err := c.VaultSearchFiles(context.Background(), "this", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "q=this") || !strings.Contains(gotQuery, "limit=20") {
		t.Errorf("query = %q", gotQuery)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
	// The embedded metadata and the search-only field must both survive.
	if list.Items[0].Filename != "a.md" || list.Items[0].ContentSize != 9 {
		t.Errorf("embedded VaultFileMeta lost: %+v", list.Items[0])
	}
	if list.Items[0].Snippet != "see [[hl]]this[[/hl]] bit" {
		t.Errorf("snippet = %q", list.Items[0].Snippet)
	}
}

func TestVaultSearchFilesOptionsAndEscaping(t *testing.T) {
	var gotQuery string
	c := jsonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	})
	// A query with characters that must not leak into the URL structure.
	if _, err := c.VaultSearchFiles(context.Background(), "a&b=c d",
		&VaultSearchOptions{Limit: 5, Offset: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "offset=10") {
		t.Errorf("query = %q", gotQuery)
	}
	if strings.Contains(gotQuery, "q=a&b=c d") {
		t.Errorf("query not escaped: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "q=a%26b%3Dc+d") {
		t.Errorf("query = %q, want the search text percent-encoded", gotQuery)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func asError(err error, target any) bool { return errors.As(err, target) }
