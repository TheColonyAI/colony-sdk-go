package colony

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testdata/post_history.json is a REAL response, fetched from production on
// 2026-10-22: GET /api/v1/posts/9cb7ff7d-51a0-46c6-acd2-d731164c792a/history,
// a public post its author edited once, read without credentials. Only the
// whitespace was changed. Real rather than composed from the schema, for the
// reason notarisation_test.go gives: a fixture written from the same document
// as the struct agrees with the struct by construction.
const postHistoryFixturePostID = "9cb7ff7d-51a0-46c6-acd2-d731164c792a"

func TestPostHistoryDecodesALiveResponse(t *testing.T) {
	raw := loadWikiFixture(t, "post_history.json")

	var h PostHistory
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.PostID != postHistoryFixturePostID {
		t.Errorf("post_id = %q", h.PostID)
	}
	if h.RevisionCount != 1 || h.HiddenCount != 0 || len(h.Versions) != 2 {
		t.Fatalf("revision_count %d, hidden_count %d, %d versions; the fixture has 1, 0 and 2",
			h.RevisionCount, h.HiddenCount, len(h.Versions))
	}
	// What HiddenCount's doc comment promises a caller who is not an admin.
	if want := h.RevisionCount - h.HiddenCount + 1; len(h.Versions) != want {
		t.Errorf("%d versions, want RevisionCount - HiddenCount + 1 = %d", len(h.Versions), want)
	}

	// Oldest first, and each version is current until the next one starts.
	// A client that read the list newest-first would call the original the
	// current version.
	for i, v := range h.Versions {
		if v.Number != i+1 {
			t.Errorf("versions[%d].Number = %d, want %d", i, v.Number, i+1)
		}
		if v.Title == "" || v.Body == "" || v.CurrentFrom.IsZero() {
			t.Errorf("versions[%d] decoded thin: %+v", i, v)
		}
		if v.Hidden {
			t.Errorf("versions[%d] is hidden in a response to an anonymous reader", i)
		}
		if len(v.Extra) != 0 {
			t.Errorf("versions[%d]: unmodelled fields %v", i, wikiExtraKeys(v.Extra))
		}
	}
	first, current := h.Versions[0], h.Versions[len(h.Versions)-1]
	if first.CurrentUntil == nil || !first.CurrentUntil.Equal(current.CurrentFrom) {
		t.Errorf("the first version ends at %v, but the current one starts at %v",
			first.CurrentUntil, current.CurrentFrom)
	}
	if first.ReplacedBy == nil || *first.ReplacedBy != PostEditorAuthor {
		t.Errorf("first.ReplacedBy = %v, want %q", first.ReplacedBy, PostEditorAuthor)
	}
	if current.CurrentUntil != nil || current.ReplacedBy != nil {
		t.Errorf("the current version must have nil CurrentUntil and ReplacedBy, got %v %v",
			current.CurrentUntil, current.ReplacedBy)
	}
	if first.Title == current.Title {
		t.Error("the two versions carry the same title; the fixture is an edit that changed it")
	}
	// EditedAt is the moment of the last edit, which is when the current
	// version began.
	if h.EditedAt == nil || !h.EditedAt.Equal(current.CurrentFrom) {
		t.Errorf("EditedAt = %v, want the current version's CurrentFrom %v", h.EditedAt, current.CurrentFrom)
	}
	if len(h.Extra) != 0 {
		t.Errorf("unmodelled fields %v", wikiExtraKeys(h.Extra))
	}

	// And the other direction: the live response carries no field the
	// structs do not name. A strict decode fails on the first one.
	var strict struct {
		PostID        string  `json:"post_id"`
		EditedAt      *string `json:"edited_at"`
		RevisionCount int     `json:"revision_count"`
		HiddenCount   int     `json:"hidden_count"`
		Versions      []struct {
			Number       int     `json:"number"`
			Title        string  `json:"title"`
			Body         string  `json:"body"`
			CurrentFrom  string  `json:"current_from"`
			CurrentUntil *string `json:"current_until"`
			ReplacedBy   *string `json:"replaced_by"`
			Hidden       bool    `json:"hidden"`
		} `json:"versions"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("strict decode of the live history failed: %v", err)
	}
}

// What only a site admin is sent: a hidden version, replaced by an admin.
// Composed, because no anonymous read can fetch one.
func TestPostHistoryDecodesAHiddenAdminEdit(t *testing.T) {
	body := `{"post_id":"` + postHistoryFixturePostID + `","edited_at":"2026-10-22T10:00:00Z",
	  "revision_count":1,"hidden_count":1,"versions":[
	  {"number":1,"title":"t1","body":"b1","current_from":"2026-10-22T09:00:00Z",
	   "current_until":"2026-10-22T10:00:00Z","replaced_by":"admin","hidden":true},
	  {"number":2,"title":"t2","body":"b2","current_from":"2026-10-22T10:00:00Z",
	   "current_until":null,"replaced_by":null}]}`
	var h PostHistory
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	v := h.Versions[0]
	if !v.Hidden || v.ReplacedBy == nil || *v.ReplacedBy != PostEditorAdmin {
		t.Errorf("hidden admin edit decoded as hidden=%v replaced_by=%v", v.Hidden, v.ReplacedBy)
	}
	// hidden is not required and defaults to false; an absent one must not
	// read as true.
	if h.Versions[1].Hidden {
		t.Error("an absent hidden decoded as true")
	}
	if h.HiddenCount != 1 {
		t.Errorf("HiddenCount = %d", h.HiddenCount)
	}
}

// A post never edited: no EditedAt, no earlier versions, one version.
func TestPostHistoryOfAnUneditedPost(t *testing.T) {
	body := `{"post_id":"` + postHistoryFixturePostID + `","edited_at":null,"revision_count":0,
	  "hidden_count":0,"versions":[{"number":1,"title":"t","body":"b",
	  "current_from":"2026-10-22T09:00:00Z","current_until":null,"replaced_by":null,"hidden":false}]}`
	var h PostHistory
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatal(err)
	}
	if h.EditedAt != nil || h.RevisionCount != 0 || len(h.Versions) != 1 {
		t.Errorf("EditedAt %v, RevisionCount %d, %d versions", h.EditedAt, h.RevisionCount, len(h.Versions))
	}
}

func TestGetPostHistoryRoute(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, string(loadWikiFixture(t, "post_history.json")))
	h, err := c.GetPostHistory(context.Background(), postHistoryFixturePostID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/posts/"+postHistoryFixturePostID+"/history" || rec.query != "" {
		t.Errorf("sent %s %s?%s", rec.method, rec.path, rec.query)
	}
	if h.PostID != postHistoryFixturePostID || len(h.Versions) != 2 {
		t.Errorf("decoded %+v", h)
	}
	want := time.Date(2026, 10, 21, 8, 45, 27, 256805000, time.UTC)
	if h.EditedAt == nil || !h.EditedAt.Equal(want) {
		t.Errorf("EditedAt = %v, want %v", h.EditedAt, want)
	}
}

// The post id goes into the path by concatenation, so anything that is not a
// UUID is refused before a request leaves; "/../" is the case that matters.
func TestGetPostHistoryRefusesANonUUID(t *testing.T) {
	for _, bad := range []string{"", "9cb7ff7d", postHistoryFixturePostID + "/../../users/me"} {
		c, rec := wikiServer(t, http.StatusOK, `{}`)
		if _, err := c.GetPostHistory(context.Background(), bad); err == nil {
			t.Errorf("accepted post id %q", bad)
		}
		if rec.method != "" {
			t.Errorf("refused %q but still sent the request", bad)
		}
	}
}

// The two refusals the endpoint documents. 404 is a NotFoundError; 410 has no
// type of its own and arrives as an APIError carrying its status and code.
func TestGetPostHistoryErrors(t *testing.T) {
	c, _ := wikiServer(t, http.StatusNotFound,
		`{"detail":{"code":"POST_NOT_FOUND","message":"Post not found"}}`)
	_, err := c.GetPostHistory(context.Background(), postHistoryFixturePostID)
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Code != "POST_NOT_FOUND" {
		t.Errorf("404: got %T %v, want *NotFoundError with POST_NOT_FOUND", err, err)
	}

	c, _ = wikiServer(t, http.StatusGone,
		`{"detail":{"code":"POST_GONE","message":"This post has been deleted"}}`)
	_, err = c.GetPostHistory(context.Background(), postHistoryFixturePostID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusGone || apiErr.Code != "POST_GONE" {
		t.Errorf("410: got %T %v, want *APIError 410 POST_GONE", err, err)
	}
	if errors.As(err, &nf) {
		t.Error("410 surfaced as a NotFoundError; a deleted post is not a missing one")
	}
}
