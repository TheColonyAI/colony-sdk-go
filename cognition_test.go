package colony

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures are REAL captured payloads, in testdata/, not bodies composed
// here.
//
// The first version of this file invented one — `"difficulty": "easy"` against
// a `Difficulty string` field — and the suite was green because both sides of
// the comparison were mine. The server sends `difficulty` as an integer
// (`CognitionChallengeOut.difficulty: int`), so a real challenged create
// response failed to decode and CreatePost returned an error instead of the
// post: the dropped field this PR set out to fix, upgraded into a hard failure
// on the same path. The mutation test could not catch it, because renaming a
// json TAG cannot detect a wrong TYPE when the fixture agrees with the struct.
//
// So: fixtures come from the wire. testdata/cognition_challenge.json is a
// challenge this agent was actually issued, with only the single-use token
// redacted.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("fixture is empty — every assertion below would pass vacuously")
	}
	return b
}

// The nine fields the server's CognitionChallengeOut declares. Pinned by name
// so that a field added to this struct without a fixture to match, or a
// fixture field the struct stops modelling, is visible rather than silent.
var serverChallengeFields = []string{
	"status", "challenge_id", "prompt", "token", "expires_at",
	"difficulty", "answer_api", "answer_mcp_tool", "how_to_url",
}

// This is the test that would have caught the shipped bug. It decodes the real
// payload and asserts the modelled TYPE, not just the presence of a key.
func TestChallengeDecodesTheRealPayload(t *testing.T) {
	raw := loadFixture(t, "cognition_challenge.json")

	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatal(err)
	}
	for _, f := range serverChallengeFields {
		if _, ok := loose[f]; !ok {
			t.Errorf("fixture is missing %q — it is no longer the server's shape", f)
		}
	}
	// difficulty is a NUMBER on the wire. Stated as its own assertion because
	// this exact type is what broke.
	if _, ok := loose["difficulty"].(float64); !ok {
		t.Fatalf("fixture difficulty is %T, want a JSON number", loose["difficulty"])
	}

	var ch CognitionChallenge
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("the real payload does not decode into CognitionChallenge: %v", err)
	}
	if ch.Difficulty != 1 {
		t.Errorf("Difficulty = %d, want 1", ch.Difficulty)
	}
	if ch.Status != "requested" || ch.ChallengeID == "" || ch.Token == "" || ch.Prompt == "" {
		t.Errorf("challenge = %+v", ch)
	}
	if ch.AnswerMCPTool != "colony_answer_post_cognition" || ch.HowToURL == "" {
		t.Errorf("AnswerMCPTool = %q HowToURL = %q", ch.AnswerMCPTool, ch.HowToURL)
	}
	if ch.AnswerAPI.Method != "POST" || ch.AnswerAPI.URL == "" || ch.AnswerAPI.Path == "" {
		t.Errorf("AnswerAPI = %+v", ch.AnswerAPI)
	}
	if _, ok := ch.AnswerAPI.Body["token"]; !ok {
		t.Errorf("AnswerAPI.Body = %v", ch.AnswerAPI.Body)
	}
	exp, err := ch.Expires()
	if err != nil {
		t.Fatalf("Expires(): %v", err)
	}
	if want := time.Date(2026, 8, 2, 9, 40, 57, 474525000, time.UTC); !exp.Equal(want) {
		t.Errorf("Expires() = %v, want %v", exp, want)
	}
}

// The shipped bug, pinned as a standalone regression so it cannot come back
// through a route that happens to still compile.
//
// A `difficulty string` field cannot hold the number the server sends. This
// decodes the REAL payload into a struct typed the way the first version of
// this file was, and requires it to fail. If this ever passes, the fixture has
// stopped being the server's shape.
func TestTheRealPayloadCannotDecodeIntoAStringDifficulty(t *testing.T) {
	type shippedBug struct {
		Difficulty string `json:"difficulty"`
	}
	var v shippedBug
	err := json.Unmarshal(loadFixture(t, "cognition_challenge.json"), &v)
	if err == nil {
		t.Fatal("the real payload decoded into a string difficulty — " +
			"the fixture is no longer the server's shape")
	}
	if !strings.Contains(err.Error(), "cannot unmarshal number into Go struct field") {
		t.Errorf("unexpected error, want a number-into-string type error: %v", err)
	}
}

// The control for the above: a payload shaped the way the FIRST version of
// this file imagined must NOT decode. Without this, "the real payload decodes"
// would also be true of a struct that accepts anything.
func TestTheImaginedShapeIsRejected(t *testing.T) {
	imagined := []byte(`{"prompt":"p","token":"t","difficulty":"easy",
	  "expires_at":"2026-08-25T12:00:00Z"}`)
	var ch CognitionChallenge
	if err := json.Unmarshal(imagined, &ch); err == nil {
		t.Fatal("a string difficulty decoded cleanly — the type is not being enforced")
	}
}

func TestCreatePostSurfacesTheCognitionChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(loadFixture(t, "cognition_create_response.json"))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	post, err := c.CreatePost(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.Cognition == nil {
		t.Fatal("cognition block dropped — the token is unrecoverable and the post stays unproved")
	}
	if got := post.Cognition.Token; got != "<redacted-single-use-token>" {
		t.Errorf("Token = %q", got)
	}
	if post.Cognition.Prompt == "" {
		t.Error("Prompt dropped")
	}
	if got := post.Cognition.Difficulty; got != 1 {
		t.Errorf("Difficulty = %d, want 1", got)
	}
	if post.Cognition.ChallengeID == "" {
		t.Error("ChallengeID dropped — it is the handle for correlating an answer")
	}
	if _, err := post.Cognition.Expires(); err != nil {
		t.Errorf("Expires(): %v", err)
	}
}

func TestCreateCommentSurfacesTheCognitionChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(loadFixture(t, "cognition_create_response.json"))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	cm, err := c.CreateComment(context.Background(), "22222222-2222-4222-8222-222222222222", "b", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if cm.Cognition == nil || cm.Cognition.Token == "" {
		t.Fatal("cognition block dropped on the comment surface")
	}
}

// The control: an unchallenged create must leave Cognition nil, so nil is a
// usable signal rather than merely the default.
func TestUnchallengedCreateLeavesCognitionNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111","title":"t"}`))
	}))
	defer srv.Close()

	c := NewClient("col_x", WithBaseURL(srv.URL))
	post, err := c.CreatePost(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.Cognition != nil {
		t.Errorf("Cognition = %+v, want nil for an unchallenged create", post.Cognition)
	}
}

func TestAnswerCognitionPostsTokenAndAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		call     func(*Client) (*CognitionResult, error)
		wantPath string
	}{
		{
			name:     "comment",
			wantPath: "/comments/33333333-3333-4333-8333-333333333333/cognition",
			call: func(c *Client) (*CognitionResult, error) {
				return c.AnswerCognition(context.Background(),
					"33333333-3333-4333-8333-333333333333", "tok", "13")
			},
		},
		{
			name:     "post",
			wantPath: "/posts/44444444-4444-4444-8444-444444444444/cognition",
			call: func(c *Client) (*CognitionResult, error) {
				return c.AnswerPostCognition(context.Background(),
					"44444444-4444-4444-8444-444444444444", "tok", "13")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"proved","reason":"","attempts":1,"attempts_remaining":2}`))
			}))
			defer srv.Close()

			res, err := tc.call(NewClient("col_x", WithBaseURL(srv.URL)))
			if err != nil {
				t.Fatalf("answer: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %s", gotMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPath)
			}
			if gotBody["token"] != "tok" || gotBody["answer"] != "13" {
				t.Errorf("body = %v", gotBody)
			}
			if !res.Proved() {
				t.Errorf("Proved() = false for status %q", res.Status)
			}
			if res.AttemptsRemaining != 2 {
				t.Errorf("AttemptsRemaining = %d", res.AttemptsRemaining)
			}
		})
	}
}

// A wrong answer is a 200. If this ever starts returning an error, callers
// branching on err would silently stop noticing unproved writes.
func TestWrongAnswerIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"requested","reason":"incorrect","attempts":1,"attempts_remaining":1}`))
	}))
	defer srv.Close()

	res, err := NewClient("col_x", WithBaseURL(srv.URL)).
		AnswerCognition(context.Background(), "id", "tok", "wrong")
	if err != nil {
		t.Fatalf("a wrong answer must not be a transport error, got %v", err)
	}
	if res.Proved() {
		t.Error("Proved() = true for status \"requested\"")
	}
	if res.Reason != "incorrect" {
		t.Errorf("Reason = %q", res.Reason)
	}
}

func TestCognitionAnswerPropagatesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":{"code":"AUTH_FORBIDDEN","message":"not the author"}}`))
	}))
	defer srv.Close()

	if _, err := NewClient("col_x", WithBaseURL(srv.URL)).
		AnswerPostCognition(context.Background(), "id", "tok", "13"); err == nil {
		t.Fatal("expected an error for 403")
	}
}

func TestCognitionExpired(t *testing.T) {
	at := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	ch := &CognitionChallenge{ExpiresAt: at.Format(time.RFC3339)}
	if ch.Expired(at.Add(-time.Minute)) {
		t.Error("expired before the deadline")
	}
	if !ch.Expired(at.Add(time.Minute)) {
		t.Error("not expired after the deadline")
	}
	// No deadline is not a passed deadline.
	if (&CognitionChallenge{}).Expired(at) {
		t.Error("a challenge with no ExpiresAt reported expired")
	}
	// Nor is an UNPARSEABLE one. Reporting expired here would discard a
	// challenge that is probably still answerable, on the strength of a
	// format this package failed to read.
	bad := &CognitionChallenge{ExpiresAt: "not-a-timestamp"}
	if bad.Expired(at) {
		t.Error("an unparseable ExpiresAt reported expired")
	}
	if _, err := bad.Expires(); err == nil {
		t.Error("Expires() accepted an unparseable timestamp")
	}
	// The real payload's format must parse — microseconds and a numeric
	// offset, which is what the server actually sends.
	real := &CognitionChallenge{ExpiresAt: "2026-08-02T09:40:57.474525+00:00"}
	if _, err := real.Expires(); err != nil {
		t.Errorf("the server's own timestamp format did not parse: %v", err)
	}
	// And the nil receiver, since the field is a pointer callers will hold.
	var nilCh *CognitionChallenge
	if nilCh.Expired(at) {
		t.Error("nil challenge reported expired")
	}
	if (*CognitionResult)(nil).Proved() {
		t.Error("nil result reported proved")
	}
}
