package colony

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
)

// Idempotency outcomes (platform release 2026-11-15a): a keyed write's
// response says whether it was a replay (Idempotent-Replay: true), or was not
// stored (X-Colony-Idempotency-Skipped: <reason>), and a key reused with a
// different request is a 409 whose detail says what became of the first.

const idemColony = `{"id":"` + albumID + `","name":"my-study","display_name":"My Study","community_type":"private"}`

func TestLastResponseIdempotencyOutcomes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		header     map[string]string
		wantReplay bool
		wantSkip   string
	}{
		{"neither header: stored", nil, false, ""},
		{"a replay", map[string]string{"Idempotent-Replay": "true"}, true, ""},
		{"a replay, any case", map[string]string{"Idempotent-Replay": "True"}, true, ""},
		{"not a replay unless true", map[string]string{"Idempotent-Replay": "false"}, false, ""},
		{"skipped, too large", map[string]string{"X-Colony-Idempotency-Skipped": "response_too_large"},
			false, IdempotencySkippedResponseTooLarge},
		{"skipped, a reason this package does not know", map[string]string{"X-Colony-Idempotency-Skipped": "reason_added_later"},
			false, "reason_added_later"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reqs := projectServer(t, projectReply{201, idemColony, tc.header})
			if _, err := c.CreateColony(ctx, "my-study", "My Study",
				&CreateColonyOptions{CommunityType: "private", IdempotencyKey: "k-1"}); err != nil {
				t.Fatal(err)
			}
			if got := (*reqs)[0].header.Get("Idempotency-Key"); got != "k-1" {
				t.Fatalf("Idempotency-Key sent = %q", got)
			}
			if got := c.LastResponseIdempotentReplay(); got != tc.wantReplay {
				t.Errorf("LastResponseIdempotentReplay() = %v, want %v", got, tc.wantReplay)
			}
			if got := c.LastResponseIdempotencySkipped(); got != tc.wantSkip {
				t.Errorf("LastResponseIdempotencySkipped() = %q, want %q", got, tc.wantSkip)
			}
		})
	}
}

// An error response is never stored, and says so; the accessors read it off
// the failed call as off a successful one.
func TestLastResponseIdempotencySkippedOnAnError(t *testing.T) {
	c, _ := projectServer(t, projectReply{403, `{"detail":{"code":"KARMA_TOO_LOW","message":"no"}}`,
		map[string]string{"X-Colony-Idempotency-Skipped": "error_response"}})
	_, err := c.CreateColony(context.Background(), "my-study", "My Study", &CreateColonyOptions{IdempotencyKey: "k"})
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %#v, want an *AuthError", err)
	}
	if got := c.LastResponseIdempotencySkipped(); got != IdempotencySkippedErrorResponse {
		t.Errorf("LastResponseIdempotencySkipped() = %q, want %q", got, IdempotencySkippedErrorResponse)
	}
	if c.LastResponseIdempotentReplay() {
		t.Error("an error reported as a replay")
	}
}

// Each answer replaces the last: a replay or a skip is not carried into the
// next call's reading.
func TestLastResponseIdempotencyResetsPerRequest(t *testing.T) {
	ctx := context.Background()
	c, _ := projectServer(t,
		projectReply{200, idemColony, map[string]string{"Idempotent-Replay": "true"}},
		projectReply{200, idemColony, map[string]string{"X-Colony-Idempotency-Skipped": "unavailable"}},
		projectReply{200, `{"id":"p1"}`, nil},
	)
	opts := &CreateColonyOptions{IdempotencyKey: "k"}

	if _, err := c.CreateColony(ctx, "my-study", "My Study", opts); err != nil {
		t.Fatal(err)
	}
	if !c.LastResponseIdempotentReplay() || c.LastResponseIdempotencySkipped() != "" {
		t.Fatalf("first: replay %v, skipped %q", c.LastResponseIdempotentReplay(), c.LastResponseIdempotencySkipped())
	}
	if _, err := c.CreateColony(ctx, "my-study", "My Study", opts); err != nil {
		t.Fatal(err)
	}
	if c.LastResponseIdempotentReplay() || c.LastResponseIdempotencySkipped() != IdempotencySkippedUnavailable {
		t.Fatalf("second: replay %v, skipped %q", c.LastResponseIdempotentReplay(), c.LastResponseIdempotencySkipped())
	}
	if _, err := c.GetPost(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if c.LastResponseIdempotentReplay() || c.LastResponseIdempotencySkipped() != "" {
		t.Errorf("third: replay %v, skipped %q", c.LastResponseIdempotentReplay(), c.LastResponseIdempotencySkipped())
	}
}

func TestLastResponseIdempotencyBeforeAnyRequest(t *testing.T) {
	c := NewClient("col_x")
	if c.LastResponseIdempotentReplay() || c.LastResponseIdempotencySkipped() != "" {
		t.Error("a client that has sent nothing reported an outcome")
	}
}

// The accessors share LastResponseHeaders' lock; run under -race (as CI does)
// this fails if either reads unguarded.
func TestLastResponseIdempotencyIsSafeConcurrently(t *testing.T) {
	ctx := context.Background()
	c, _ := projectServer(t, projectReply{200, idemColony, map[string]string{"Idempotent-Replay": "true"}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = c.CreateColony(ctx, "my-study", "My Study", &CreateColonyOptions{IdempotencyKey: "k"})
		}()
		go func() {
			defer wg.Done()
			_ = c.LastResponseIdempotentReplay()
			_ = c.LastResponseIdempotencySkipped()
		}()
	}
	wg.Wait()
	if !c.LastResponseIdempotentReplay() {
		t.Error("every answer was a replay, and the last read says otherwise")
	}
}

// The six reasons the platform documents (app/middleware/idempotency.py,
// SKIP_REASONS, release 2026-11-15a), spelt exactly.
func TestIdempotencySkippedReasonsMatchThePlatform(t *testing.T) {
	got := []string{
		IdempotencySkippedErrorResponse,
		IdempotencySkippedResponseTooLarge,
		IdempotencySkippedRequestTooLarge,
		IdempotencySkippedRequestLengthUnknown,
		IdempotencySkippedNoBearerToken,
		IdempotencySkippedUnavailable,
	}
	sort.Strings(got)
	want := []string{"error_response", "no_bearer_token", "request_length_unknown",
		"request_too_large", "response_too_large", "unavailable"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reasons = %v, want %v", got, want)
		}
	}
	if FirstRequestCompleted != "completed" || FirstRequestInProgress != "in_progress" {
		t.Errorf("first_request values = %q, %q", FirstRequestCompleted, FirstRequestInProgress)
	}
}

func TestConflictErrorFirstRequest(t *testing.T) {
	cases := []struct {
		name, body, wantCode, want string
	}{
		{"completed",
			`{"detail":{"code":"idempotency_payload_mismatch","message":"Idempotency-Key reused with a different request payload.","first_request":"completed"}}`,
			"idempotency_payload_mismatch", FirstRequestCompleted},
		{"in progress",
			`{"detail":{"code":"idempotency_payload_mismatch","message":"Idempotency-Key reused with a different request payload.","first_request":"in_progress"}}`,
			"idempotency_payload_mismatch", FirstRequestInProgress},
		{"an older platform omits it",
			`{"detail":{"code":"idempotency_payload_mismatch","message":"Idempotency-Key reused with a different request payload."}}`,
			"idempotency_payload_mismatch", ""},
		{"another conflict",
			`{"detail":{"code":"idempotency_in_progress","message":"A request with this Idempotency-Key is still being processed."}}`,
			"idempotency_in_progress", ""},
		{"a string detail", `{"detail":"Already voted"}`, "", ""},
		{"not JSON", `conflict`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := projectServer(t, projectReply{409, tc.body, nil})
			_, err := c.CreateColony(context.Background(), "my-study", "My Study", &CreateColonyOptions{IdempotencyKey: "k"})
			var ce *ConflictError
			if !errors.As(err, &ce) {
				t.Fatalf("error = %#v, want a *ConflictError", err)
			}
			if ce.Code != tc.wantCode || ce.FirstRequest != tc.want {
				t.Errorf("Code %q, FirstRequest %q; want %q, %q", ce.Code, ce.FirstRequest, tc.wantCode, tc.want)
			}
			// Still an *APIError underneath, as every typed error is.
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
				t.Errorf("not an *APIError 409: %#v", err)
			}
		})
	}
}

// first_request is read from a 409 only: a 400 carrying the same key is not a
// conflict and has no FirstRequest to carry it.
func TestFirstRequestIsNotReadOffOtherStatuses(t *testing.T) {
	c, _ := projectServer(t, projectReply{400,
		`{"detail":{"code":"idempotency_key_invalid","message":"too long","first_request":"completed"}}`, nil})
	_, err := c.CreateColony(context.Background(), "my-study", "My Study", &CreateColonyOptions{IdempotencyKey: "k"})
	var ce *ConflictError
	if errors.As(err, &ce) {
		t.Fatalf("a 400 decoded as a *ConflictError: %#v", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %#v, want a *ValidationError", err)
	}
}
