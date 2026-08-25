package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

func hmacHex(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// sigV2 builds the replay-resistant header the handler now verifies:
//
//	X-Colony-Signature-256: t=<unix>,v1=<hmac-sha256 of "t.payload">
//
// Signed at `at` so a test can produce a stale delivery on purpose.
func sigV2(body, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidSignature(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","author":"agent-7","title":"Hello","colony":"general","post_type":"discussion"}`
	sig := sigV2(body, "s3cret", time.Now())

	req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
	req.Header.Set(colony.HeaderSignature256, sig)
	req.Header.Set(colony.HeaderEventID, "evt-1")
	req.Header.Set(colony.HeaderDeliveryID, "dlv-1")

	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The tampered-body case is the one that matters: if someone strips the
// verification call the valid-signature test still passes, but this one catches it.
func TestTamperedBody(t *testing.T) {
	original := `{"event":"post_created","post_id":"p-1","title":"Hello"}`
	sig := sigV2(original, "s3cret", time.Now())

	tampered := `{"event":"post_created","post_id":"p-1","title":"HACKED"}`

	req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(tampered))
	req.Header.Set(colony.HeaderSignature256, sig)

	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for tampered body, got %d", rec.Code)
	}
}

// TestMissingSignatureHeader asserts on the response body so the test pins the
// early guard (missing header), not just the 401 that the HMAC check would
// also produce — both return 401, but only the guard returns "missing signature".
func TestMissingSignatureHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/colony-webhook",
		strings.NewReader(`{"event":"post_created"}`))
	// no X-Colony-Signature header

	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for missing header, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing signature") {
		t.Errorf("want body to contain \"missing signature\", got %q", rec.Body.String())
	}
}

func TestWrongSecret(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1"}`
	sig := sigV2(body, "wrong-secret", time.Now())

	req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
	req.Header.Set(colony.HeaderSignature256, sig)

	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for wrong secret, got %d", rec.Code)
	}
}

func TestDuplicateEventSkipped(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","title":"Hello","author":"agent-7","colony":"general","post_type":"discussion"}`
	sig := sigV2(body, "s3cret", time.Now())

	seen := &seenSet{m: map[string]bool{}}
	handler := webhookHandler("s3cret", seen)

	sendReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
		req.Header.Set(colony.HeaderSignature256, sig)
		req.Header.Set(colony.HeaderEventID, "evt-stable")
		req.Header.Set(colony.HeaderDeliveryID, "dlv-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := sendReq(); rec.Code != http.StatusOK {
		t.Fatalf("first delivery: want 200, got %d", rec.Code)
	}
	if rec := sendReq(); rec.Code != http.StatusOK {
		t.Fatalf("duplicate delivery: want 200 (skipped), got %d", rec.Code)
	}
}

// The race detector only proves absence of unsynchronised access. This proves
// the dedup is ATOMIC: 64 concurrent retries of one event must be admitted
// exactly once, which a lock-free check-then-set would fail.
func TestSeenSetAdmitsExactlyOnce(t *testing.T) {
	seen := &seenSet{m: map[string]bool{}}
	var admitted int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !seen.seenBefore("evt-shared") {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted %d times, want exactly 1", admitted)
	}
}

// A delivery with no X-Colony-Event-Id must be processed and must NOT put an
// empty key in the dedup set — otherwise the FIRST such delivery poisons the
// set and every later one is skipped as a "duplicate" of it.
//
// #35 guarded the recording site explicitly for this. That guard became
// redundant when the set moved behind seenSet.seenBefore, because the call
// site short-circuits on EventID != "" and so never records "". Redundant is
// not the same as guaranteed: reorder that condition to
// `seen.seenBefore(id) && id != ""` and the property is gone with nothing to
// notice. So it is pinned here rather than left as an accident of evaluation
// order.
func TestMissingEventIDIsNotRecordedAsADuplicate(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","title":"Hello","author":"agent-7","colony":"general","post_type":"discussion"}`
	sig := sigV2(body, "s3cret", time.Now())

	seen := &seenSet{m: map[string]bool{}}
	handler := webhookHandler("s3cret", seen)

	send := func(deliveryID string) int {
		req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
		req.Header.Set(colony.HeaderSignature256, sig)
		req.Header.Set(colony.HeaderDeliveryID, deliveryID)
		// No HeaderEventID on purpose.
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("dlv-1"); code != http.StatusOK {
		t.Fatalf("first id-less delivery: want 200, got %d", code)
	}
	if _, poisoned := seen.m[""]; poisoned {
		t.Error(`an empty key was recorded — the next id-less delivery would be skipped as its duplicate`)
	}
	if code := send("dlv-2"); code != http.StatusOK {
		t.Fatalf("second id-less delivery: want 200, got %d", code)
	}
	if len(seen.m) != 0 {
		t.Errorf("dedup set holds %d keys after two id-less deliveries, want 0: %v", len(seen.m), seen.m)
	}
}

// Issue #30 opened with a measurement against this example: five identical
// replays of one delivery, all accepted, because the legacy signature covers
// the body and nothing else. This is that measurement, inverted.
func TestReplayedDeliveryIsRejected(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","title":"Hello","author":"agent-7","colony":"general","post_type":"discussion"}`
	// A delivery captured an hour ago and re-sent. The signature is genuine;
	// only the timestamp gives it away.
	captured := sigV2(body, "s3cret", time.Now().Add(-time.Hour))

	handler := webhookHandler("s3cret", &seenSet{m: map[string]bool{}})
	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
		req.Header.Set(colony.HeaderSignature256, captured)
		req.Header.Set(colony.HeaderEventID, "evt-1")
		req.Header.Set(colony.HeaderDeliveryID, "dlv-"+strconv.Itoa(i))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("replay #%d: got %d, want 401 — a captured delivery is still being accepted",
				i, rec.Code)
		}
	}
}

// The control for the test above: the same handler must accept the same body
// when it is freshly signed. Without this, "everything is rejected" would pass.
func TestFreshDeliveryIsStillAccepted(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","title":"Hello","author":"agent-7","colony":"general","post_type":"discussion"}`
	req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
	req.Header.Set(colony.HeaderSignature256, sigV2(body, "s3cret", time.Now()))
	req.Header.Set(colony.HeaderEventID, "evt-fresh")
	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh delivery got %d, want 200", rec.Code)
	}
}

// This example now verifies the timestamped header, so a delivery carrying
// ONLY the legacy X-Colony-Signature is refused.
//
// That is a behaviour change and worth being explicit about — but not a
// breaking one in practice: the server sends both headers on every delivery,
// so nothing real arrives with the legacy one alone. A receiver that must
// accept legacy-only deliveries should keep using
// colony.VerifyAndParseWebhookRequest and accept that it cannot bound replay.
func TestLegacyOnlySignatureIsNoLongerAccepted(t *testing.T) {
	body := `{"event":"post_created","post_id":"p-1","title":"Hello","author":"agent-7","colony":"general","post_type":"discussion"}`
	req := httptest.NewRequest(http.MethodPost, "/colony-webhook", strings.NewReader(body))
	req.Header.Set(colony.HeaderSignature, hmacHex(body, "s3cret")) // legacy only
	rec := httptest.NewRecorder()
	webhookHandler("s3cret", &seenSet{m: map[string]bool{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy-only delivery got %d, want 401", rec.Code)
	}

	// And the legacy signature itself is still valid — this is about which
	// header the example CHOOSES to verify, not about the old one breaking.
	if !colony.VerifyWebhook([]byte(body), hmacHex(body, "s3cret"), "s3cret") {
		t.Error("colony.VerifyWebhook stopped accepting a valid legacy signature")
	}
}
