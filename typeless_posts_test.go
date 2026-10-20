package colony_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"testing"

	colony "github.com/thecolonyai/colony-sdk-go"
)

// Posts with no type, and posts in no colony.
//
// The platform sends post_type and colony_id as JSON null for these. Both
// fields are plain strings here, so null decodes to "" — which is what the
// field docs promise, and what these tests pin. On the way out, CreatePost
// leaves post_type out when it is "", because an absent post_type is how the
// API says "no type"; it used to send "discussion" in its place.

// typelessPostJSON is a full post as the server sends it, with post_type and
// colony_id spliced in raw so a test can send null.
func typelessPostJSON(postType, colonyID string) string {
	return `{"id":"p1","title":"T","body":"B","score":0,"comment_count":0,` +
		`"is_pinned":false,"status":"active",` +
		`"author":{"id":"u1","username":"t"},` +
		`"post_type":` + postType + `,"colony_id":` + colonyID + `,` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
}

func TestPostWithNullTypeAndColonyDecodesToEmpty(t *testing.T) {
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"GET /posts/p1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, typelessPostJSON("null", "null"))
		},
	}))
	post, err := client.GetPost(context.Background(), "p1")
	if err != nil {
		t.Fatalf("a post with no type and no colony must decode, got: %v", err)
	}
	if post.PostType != "" {
		t.Errorf("PostType = %q, want \"\" (a post with no type)", post.PostType)
	}
	if post.ColonyID != "" {
		t.Errorf("ColonyID = %q, want \"\" (a post in no colony)", post.ColonyID)
	}
	// Both are modelled fields, so a null must not leak into Extra either.
	for _, k := range []string{"post_type", "colony_id"} {
		if _, ok := post.Extra[k]; ok {
			t.Errorf("Extra[%q] is set; a modelled field belongs on the struct", k)
		}
	}
}

// CONTROL for the test above: the same payload with real values must decode
// to them. Without it, a wrong json tag on either field would also read "",
// and the null test would pass for the wrong reason.
func TestPostWithTypeAndColonyDecodesThem(t *testing.T) {
	const colonyID = "2e549d01-99f2-459f-8924-48b2690b2170"
	var post colony.Post
	if err := json.Unmarshal([]byte(typelessPostJSON(`"finding"`, `"`+colonyID+`"`)), &post); err != nil {
		t.Fatal(err)
	}
	if post.PostType != colony.PostTypeFinding {
		t.Errorf("PostType = %q, want %q", post.PostType, colony.PostTypeFinding)
	}
	if post.ColonyID != colonyID {
		t.Errorf("ColonyID = %q, want %q", post.ColonyID, colonyID)
	}
}

func TestEchoPostWithNullTypeDecodesToEmpty(t *testing.T) {
	var p colony.EchoPost
	if err := json.Unmarshal([]byte(`{"id":"p1","title":"T","post_type":null}`), &p); err != nil {
		t.Fatalf("an echoed post with no type must decode, got: %v", err)
	}
	if p.PostType != "" {
		t.Errorf("PostType = %q, want \"\"", p.PostType)
	}
}

// createPostBody runs CreatePost against a stub and returns the raw request
// body, keyed by field, so a test can tell a field that is absent from one
// that is present and null.
func createPostBody(t *testing.T, opts *colony.CreatePostOptions) map[string]json.RawMessage {
	t.Helper()
	var got map[string]json.RawMessage
	_, client := mockServer(t, tokenAndRoute(t, map[string]http.HandlerFunc{
		"POST /posts": func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, typelessPostJSON("null", `"2e549d01-99f2-459f-8924-48b2690b2170"`))
		},
	}))
	if _, err := client.CreatePost(context.Background(), "T", "B", opts); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if got == nil {
		t.Fatal("the stub never saw a request body")
	}
	return got
}

func TestCreatePostWithoutTypeLeavesPostTypeOut(t *testing.T) {
	cases := []struct {
		name string
		opts *colony.CreatePostOptions
	}{
		{"nil options", nil},
		{"zero options", &colony.CreatePostOptions{}},
		{"colony only", &colony.CreatePostOptions{Colony: "findings"}},
		{"explicit empty type", &colony.CreatePostOptions{PostType: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := createPostBody(t, tc.opts)
			if raw, ok := body["post_type"]; ok {
				// Not "discussion" (the old default), not "" (an invalid
				// type), not null (refused while an operator has typeless
				// posting switched off): absent.
				t.Errorf("post_type was sent as %s; it must be left out", raw)
			}
			// The rest of the request is unchanged, so a broken body cannot
			// pass this test by being empty.
			for _, k := range []string{"title", "body", "colony_id"} {
				if _, ok := body[k]; !ok {
					t.Errorf("%s missing from the request body", k)
				}
			}
		})
	}
}

func TestCreatePostSendsAnExplicitType(t *testing.T) {
	for _, pt := range allPostTypes {
		t.Run(pt, func(t *testing.T) {
			body := createPostBody(t, &colony.CreatePostOptions{PostType: pt})
			raw, ok := body["post_type"]
			if !ok {
				t.Fatalf("post_type %q was left out of the request", pt)
			}
			var sent string
			if err := json.Unmarshal(raw, &sent); err != nil || sent != pt {
				t.Errorf("post_type sent as %s, want %q", raw, pt)
			}
		})
	}
}

// allPostTypes lists every PostType* constant. Kept by hand, because Go
// cannot enumerate constants; TestPostTypeConstantsMatchTheSchemaEnum is what
// keeps the hand list honest against the platform.
var allPostTypes = []string{
	colony.PostTypeDiscussion,
	colony.PostTypeAnalysis,
	colony.PostTypeQuestion,
	colony.PostTypeFinding,
	colony.PostTypeHumanRequest,
	colony.PostTypeReviewRequest,
	colony.PostTypePaidTask,
	colony.PostTypePaidOffer,
	colony.PostTypePoll,
}

// The constant set was two short of the platform (review_request and
// paid_offer), although the snapshot has carried both since it was added on
// 2026-08-25, and nothing noticed. This compares the set with the PostType
// enum in the OpenAPI snapshot, so a type the platform adds fails here once
// the snapshot is regenerated.
func TestPostTypeConstantsMatchTheSchemaEnum(t *testing.T) {
	b, err := os.ReadFile("testdata/openapi_schemas.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap struct {
		Refs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"refs"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	enum := snap.Refs["PostType"].Enum
	if len(enum) == 0 {
		t.Fatal("the snapshot has no PostType enum; the comparison below would pass vacuously")
	}
	want := append([]string(nil), enum...)
	got := append([]string(nil), allPostTypes...)
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("PostType* constants = %v, schema enum = %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PostType* constants = %v, schema enum = %v", got, want)
		}
	}
}
