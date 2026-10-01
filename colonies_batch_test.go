package colony

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// coloniesReq is one request the stub saw, with everything a test asserts on.
type coloniesReq struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

// coloniesServer answers each request with the next reply in the script,
// repeating the last one, and records every request — the auth exchange
// excepted. Sequences matter here: a retry, a slug lookup followed by a write.
func coloniesServer(t *testing.T, script ...coloniesReply) (*Client, *[]coloniesReq) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []coloniesReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		b, err := io.ReadAll(r.Body) // the whole body: see TestWikiServerRecordsTheWholeBody
		if err != nil {
			t.Errorf("stub: reading request body: %v", err)
		}
		mu.Lock()
		n := len(reqs)
		reqs = append(reqs, coloniesReq{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), b})
		mu.Unlock()
		reply := script[len(script)-1]
		if n < len(script) {
			reply = script[n]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	fast := DefaultRetry()
	fast.BaseDelay, fast.MaxDelay = time.Millisecond, time.Millisecond
	return NewClient("col_x", WithBaseURL(srv.URL), WithRetry(fast)), &reqs
}

type coloniesReply struct {
	status int
	body   string
}

func okReply(body string) coloniesReply { return coloniesReply{200, body} }

func boolPtr(b bool) *bool { return &b }

// --- member_colonies --------------------------------------------------------

// The false case is the one a truthiness check would drop, turning "posts
// outside my colonies" into every post. Each surface is checked for all three
// states, so replacing the nil check with `if *opts.MemberColonies` fails here.
func TestMemberColoniesIsSentInAllThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  *bool
		want string // "" = absent
	}{{"nil", nil, ""}, {"true", boolPtr(true), "true"}, {"false", boolPtr(false), "false"}} {
		t.Run("GetPosts/"+tc.name, func(t *testing.T) {
			c, reqs := coloniesServer(t, okReply(`{"items":[],"total":0}`))
			if _, err := c.GetPosts(context.Background(), &GetPostsOptions{MemberColonies: tc.val}); err != nil {
				t.Fatal(err)
			}
			assertParam(t, (*reqs)[0], "/posts", "member_colonies", tc.want)
		})
		t.Run("Search/"+tc.name, func(t *testing.T) {
			c, reqs := coloniesServer(t, okReply(`{"items":[],"total":0}`))
			if _, err := c.Search(context.Background(), "colony", &SearchOptions{MemberColonies: tc.val}); err != nil {
				t.Fatal(err)
			}
			assertParam(t, (*reqs)[0], "/search", "member_colonies", tc.want)
		})
		t.Run("ListColonies/"+tc.name, func(t *testing.T) {
			c, reqs := coloniesServer(t, okReply(`[]`))
			if _, err := c.ListColonies(context.Background(), &ListColoniesOptions{MemberColonies: tc.val}); err != nil {
				t.Fatal(err)
			}
			assertParam(t, (*reqs)[0], "/colonies", "member_colonies", tc.want)
			if got := (*reqs)[0].query.Get("limit"); got != "50" {
				t.Errorf("ListColonies default limit = %q, want 50", got)
			}
		})
	}
}

// Both post iterators copy their options into GetPostsOptions field by field,
// so a field added to one copy and not the other is silently dropped by that
// iterator. Each is checked on its own.
func TestIterPostsPassesMemberColonies(t *testing.T) {
	ctx := context.Background()
	t.Run("IterPosts", func(t *testing.T) {
		c, reqs := coloniesServer(t, okReply(`{"items":[],"total":0,"has_more":false}`))
		for r := range c.IterPosts(ctx, &IterPostsOptions{MemberColonies: boolPtr(false)}) {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
		assertParam(t, (*reqs)[0], "/posts", "member_colonies", "false")
	})
	// IterPostsSeq is checked in colonies_batch_go123_test.go: it only
	// exists under go1.23.
}

func assertParam(t *testing.T, r coloniesReq, path, key, want string) {
	t.Helper()
	if r.path != path {
		t.Fatalf("path = %q, want %q", r.path, path)
	}
	_, present := r.query[key]
	switch {
	case want == "" && present:
		t.Errorf("%s sent as %q, want it absent", key, r.query.Get(key))
	case want != "" && r.query.Get(key) != want:
		t.Errorf("%s = %q (present=%v), want %q", key, r.query.Get(key), present, want)
	}
}

// --- CreateColony -----------------------------------------------------------

const createdColony = `{"id":"9f0c6c2e-5d1a-4f4e-8a35-0d6e7a1b2c3d","name":"my-study",
"display_name":"My Study","description":null,"member_count":1,"post_count":0,
"is_default":false,"is_sandbox":false,"community_type":"private",
"crowd_control_level":"off","rss_url":null,"report_reasons":null,"icon_url":null,
"icon_url_96":null,"icon_url_256":null,"posting_rules":{"min_karma":-20},
"wiki_start_page_slug":null,"wiki_start_page_url":null,
"created_at":"2026-10-01T09:00:00Z"}`

func TestCreateColonySendsExactlyTheDocumentedBody(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, reqs := coloniesServer(t, coloniesReply{201, createdColony})
		if _, err := c.CreateColony(context.Background(), "my-study", "My Study", nil); err != nil {
			t.Fatal(err)
		}
		r := (*reqs)[0]
		if r.method != http.MethodPost || r.path != "/colonies" {
			t.Fatalf("%s %s, want POST /colonies", r.method, r.path)
		}
		assertJSONBody(t, r.body, map[string]any{
			"name": "my-study", "display_name": "My Study", "community_type": "public",
		})
		if k := r.header.Get("Idempotency-Key"); k != "" {
			t.Errorf("Idempotency-Key = %q with no key set, want absent", k)
		}
	})
	t.Run("options", func(t *testing.T) {
		c, reqs := coloniesServer(t, coloniesReply{201, createdColony})
		made, err := c.CreateColony(context.Background(), "my-study", "My Study", &CreateColonyOptions{
			Description: "Claims their author cannot test alone.", CommunityType: "private",
		})
		if err != nil {
			t.Fatal(err)
		}
		assertJSONBody(t, (*reqs)[0].body, map[string]any{
			"name": "my-study", "display_name": "My Study", "community_type": "private",
			"description": "Claims their author cannot test alone.",
		})
		// The check the doc comment tells callers to make, on the field it names.
		if made.CommunityType != "private" {
			t.Errorf("CommunityType = %q, want private", made.CommunityType)
		}
		// posting_rules is deliberately unmodelled; it must still be reachable.
		if _, ok := made.Extra["posting_rules"]; !ok {
			t.Errorf("posting_rules not in Extra (Extra = %v)", made.Extra)
		}
		if _, ok := made.Extra["community_type"]; ok {
			t.Error("a modelled field leaked into Extra")
		}
	})
}

func TestCreateColonyRejectsBlankNamesBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct{ name, display string }{{"", "My Study"}, {"   ", "My Study"}, {"my-study", ""}, {"my-study", "\t"}} {
		c, reqs := coloniesServer(t, coloniesReply{201, createdColony})
		if _, err := c.CreateColony(context.Background(), tc.name, tc.display, nil); err == nil {
			t.Errorf("CreateColony(%q, %q) = nil error, want a blank-field error", tc.name, tc.display)
		}
		if len(*reqs) != 0 {
			t.Errorf("CreateColony(%q, %q) sent %d request(s) before rejecting", tc.name, tc.display, len(*reqs))
		}
	}
}

// The key has to be the SAME on every attempt, or it protects nothing. The
// stub answers 503 first, which this client retries, then 201.
func TestCreateColonySendsOneIdempotencyKeyOnEveryAttempt(t *testing.T) {
	c, reqs := coloniesServer(t, coloniesReply{503, `{"detail":"busy"}`}, coloniesReply{201, createdColony})
	if _, err := c.CreateColony(context.Background(), "my-study", "My Study",
		&CreateColonyOptions{IdempotencyKey: "k-123"}); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 2 {
		t.Fatalf("%d attempts, want 2 (one 503, one retry)", len(*reqs))
	}
	for i, r := range *reqs {
		if got := r.header.Get("Idempotency-Key"); got != "k-123" {
			t.Errorf("attempt %d Idempotency-Key = %q, want k-123", i+1, got)
		}
	}
}

// The header path must not leak: a later call on the same client, with a
// fresh context, carries no key.
func TestIdempotencyKeyDoesNotLeakIntoLaterCalls(t *testing.T) {
	c, reqs := coloniesServer(t, coloniesReply{201, createdColony}, okReply(`[]`))
	ctx := context.Background()
	if _, err := c.CreateColony(ctx, "my-study", "My Study", &CreateColonyOptions{IdempotencyKey: "k-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListColonies(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if k := (*reqs)[1].header.Get("Idempotency-Key"); k != "" {
		t.Errorf("Idempotency-Key = %q on a later ListColonies, want absent", k)
	}
}

// The slug cache is filled once and never refreshed. A colony created after
// that fill must still be nameable, without another GET /colonies.
func TestCreatedColonyIsNameableAtOnce(t *testing.T) {
	c, reqs := coloniesServer(t,
		okReply(`[{"id":"11111111-1111-4111-8111-111111111111","name":"lobby","display_name":"Lobby",
		     "description":null,"member_count":3,"is_default":false,"created_at":"2026-09-01T00:00:00Z"}]`),
		okReply(`{"post_id":"p","from_colony_id":"11111111-1111-4111-8111-111111111111","to_colony_id":null,"moved":true}`),
		coloniesReply{201, createdColony},
		okReply(`{"post_id":"p","from_colony_id":"9f0c6c2e-5d1a-4f4e-8a35-0d6e7a1b2c3d","to_colony_id":"2e549d01-99f2-459f-8924-48b2690b2170","moved":true}`),
	)
	ctx := context.Background()
	post := "4b4b4b4b-4b4b-4b4b-8b4b-4b4b4b4b4b4b"
	// 1. Naming an unmapped slug fills the cache with one GET /colonies.
	if _, err := c.MovePostOutOfColony(ctx, post, "lobby"); err != nil {
		t.Fatal(err)
	}
	// 2. Create a colony after the fill.
	if _, err := c.CreateColony(ctx, "my-study", "My Study", nil); err != nil {
		t.Fatal(err)
	}
	// 3. Name it. Without the cache insert this fails with "unknown colony".
	if _, err := c.MovePostOutOfColony(ctx, post, "my-study"); err != nil {
		t.Fatalf("the new slug did not resolve: %v", err)
	}
	gets := 0
	for _, r := range *reqs {
		if r.method == http.MethodGet && r.path == "/colonies" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("%d GET /colonies, want 1 (the new slug must come from the cache)", gets)
	}
	last := (*reqs)[len(*reqs)-1]
	if want := "/colonies/9f0c6c2e-5d1a-4f4e-8a35-0d6e7a1b2c3d/posts/" + post + "/move-out"; last.path != want {
		t.Errorf("path = %q, want %q", last.path, want)
	}
}

// --- MovePostOutOfColony ----------------------------------------------------

func TestMovePostOutOfColony(t *testing.T) {
	post := "4b4b4b4b-4b4b-4b4b-8b4b-4b4b4b4b4b4b"
	t.Run("route, and a known slug resolves locally", func(t *testing.T) {
		c, reqs := coloniesServer(t, okReply(`{"post_id":"`+post+`","from_colony_id":"bbe6be09-da95-4983-b23d-1dd980479a7e",
			"to_colony_id":"2e549d01-99f2-459f-8924-48b2690b2170","moved":true}`))
		res, err := c.MovePostOutOfColony(context.Background(), post, "findings")
		if err != nil {
			t.Fatal(err)
		}
		r := (*reqs)[0]
		want := "/colonies/" + Colonies["findings"] + "/posts/" + post + "/move-out"
		if len(*reqs) != 1 || r.method != http.MethodPost || r.path != want {
			t.Fatalf("%d request(s); first %s %s, want one POST %s", len(*reqs), r.method, r.path, want)
		}
		if len(r.body) != 0 {
			t.Errorf("sent a body %q; the route takes none", r.body)
		}
		if !res.Moved || res.ToColonyID != Colonies["general"] {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("out of general into no colony", func(t *testing.T) {
		c, _ := coloniesServer(t, okReply(`{"post_id":"`+post+`","from_colony_id":"2e549d01-99f2-459f-8924-48b2690b2170",
			"to_colony_id":null,"moved":true}`))
		res, err := c.MovePostOutOfColony(context.Background(), post, "general")
		if err != nil {
			t.Fatal(err)
		}
		if res.ToColonyID != "" || !res.Moved {
			t.Errorf("result = %+v, want Moved with an empty ToColonyID", res)
		}
	})
	t.Run("swapped arguments are rejected before any request", func(t *testing.T) {
		c, reqs := coloniesServer(t, okReply(`{}`))
		_, err := c.MovePostOutOfColony(context.Background(), "general", post)
		if err == nil || !strings.Contains(err.Error(), "MovePostToColony") {
			t.Errorf("err = %v, want the argument-order error", err)
		}
		if len(*reqs) != 0 {
			t.Errorf("sent %d request(s) first", len(*reqs))
		}
	})
	t.Run("a server refusal surfaces as a typed error", func(t *testing.T) {
		c, _ := coloniesServer(t, coloniesReply{404, `{"detail":"Post not found in this colony"}`})
		_, err := c.MovePostOutOfColony(context.Background(), post, "findings")
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("err = %T %v, want *NotFoundError", err, err)
		}
	})
}

func assertJSONBody(t *testing.T, body []byte, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	if len(got) != len(want) {
		t.Errorf("body has %d field(s) %v, want exactly %v", len(got), got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, got[k], v)
		}
	}
}
