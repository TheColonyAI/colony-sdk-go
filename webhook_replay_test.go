package colony_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	colony "github.com/thecolonyai/colony-sdk-go"
)

const replaySecret = "s3cret"

// signV2 builds the header exactly as the server does:
//
//	X-Colony-Signature-256: t=<unix>,v1=<hmac-sha256 of "t.payload">
//
// Written from app/services/webhook_dispatch/_dispatcher.py rather than from
// this package's idea of the format — the mistake that let #33 survive was a
// fixture built in the SDK's own imagined shape.
func signV2(t *testing.T, body string, at time.Time, secret string) string {
	t.Helper()
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookWithToleranceAcceptsAFreshDelivery(t *testing.T) {
	body := `{"event":"post_created","post_id":"p1"}`
	hdr := signV2(t, body, time.Now(), replaySecret)
	if err := colony.VerifyWebhookWithTolerance(
		[]byte(body), hdr, replaySecret, colony.DefaultWebhookTolerance); err != nil {
		t.Fatalf("fresh delivery rejected: %v", err)
	}
}

// The bug this exists to fix, stated as a test: the SAME captured delivery,
// replayed later, must stop verifying. Under VerifyWebhook it verifies forever.
func TestAReplayedDeliveryStopsVerifying(t *testing.T) {
	body := `{"event":"post_created","post_id":"p1"}`
	captured := signV2(t, body, time.Now().Add(-2*time.Hour), replaySecret)

	err := colony.VerifyWebhookWithTolerance(
		[]byte(body), captured, replaySecret, colony.DefaultWebhookTolerance)
	if err == nil {
		t.Fatal("a two-hour-old delivery verified")
	}
	if !errors.Is(err, colony.ErrWebhookExpired) {
		t.Errorf("err = %v, want ErrWebhookExpired", err)
	}

	// The control, and the whole reason this file exists: the legacy
	// verifier accepts that same captured delivery, indefinitely.
	legacyMac := hmac.New(sha256.New, []byte(replaySecret))
	legacyMac.Write([]byte(body))
	legacySig := hex.EncodeToString(legacyMac.Sum(nil))
	if !colony.VerifyWebhook([]byte(body), legacySig, replaySecret) {
		t.Fatal("setup wrong: the legacy signature should verify")
	}
}

// The distinction the issue asked for, and the reason this returns an error
// rather than a bool. A stale-but-authentic delivery and a forged one need
// different operator responses.
func TestReplayAndForgeryAreDistinguishable(t *testing.T) {
	body := `{"event":"mention"}`

	stale := signV2(t, body, time.Now().Add(-30*time.Minute), replaySecret)
	staleErr := colony.VerifyWebhookWithTolerance(
		[]byte(body), stale, replaySecret, colony.DefaultWebhookTolerance)

	forged := signV2(t, body, time.Now(), "the-wrong-secret")
	forgedErr := colony.VerifyWebhookWithTolerance(
		[]byte(body), forged, replaySecret, colony.DefaultWebhookTolerance)

	if !errors.Is(staleErr, colony.ErrWebhookExpired) {
		t.Errorf("stale delivery: %v, want ErrWebhookExpired", staleErr)
	}
	if !errors.Is(forgedErr, colony.ErrWebhookSignatureMismatch) {
		t.Errorf("forged delivery: %v, want ErrWebhookSignatureMismatch", forgedErr)
	}
	// Both are errors; the point is that they are DIFFERENT ones. A bool
	// would have collapsed them here.
	if errors.Is(staleErr, colony.ErrWebhookSignatureMismatch) {
		t.Error("a replay was reported as a signature mismatch")
	}
	if errors.Is(forgedErr, colony.ErrWebhookExpired) {
		t.Error("a forgery was reported as expiry")
	}
}

// A forged message that is ALSO stale must report the mismatch, not expiry.
// Reporting expiry would imply the delivery was genuine.
func TestForgedAndStaleReportsMismatchNotExpiry(t *testing.T) {
	body := `{"event":"mention"}`
	hdr := signV2(t, body, time.Now().Add(-3*time.Hour), "the-wrong-secret")
	err := colony.VerifyWebhookWithTolerance(
		[]byte(body), hdr, replaySecret, colony.DefaultWebhookTolerance)
	if !errors.Is(err, colony.ErrWebhookSignatureMismatch) {
		t.Errorf("err = %v, want ErrWebhookSignatureMismatch", err)
	}
}

// A tampered body must not verify even with a fresh, well-formed header.
func TestAlteredBodyFailsVerification(t *testing.T) {
	hdr := signV2(t, `{"event":"tip_received","amount_sats":10}`, time.Now(), replaySecret)
	err := colony.VerifyWebhookWithTolerance(
		[]byte(`{"event":"tip_received","amount_sats":100000}`), hdr,
		replaySecret, colony.DefaultWebhookTolerance)
	if !errors.Is(err, colony.ErrWebhookSignatureMismatch) {
		t.Errorf("err = %v, want ErrWebhookSignatureMismatch", err)
	}
}

// Tolerance is two-sided: a timestamp far in the future means a skewed clock
// or a crafted header, not a fresh delivery.
func TestFutureTimestampsAreRejected(t *testing.T) {
	body := `{"event":"mention"}`
	hdr := signV2(t, body, time.Now().Add(time.Hour), replaySecret)
	err := colony.VerifyWebhookWithTolerance(
		[]byte(body), hdr, replaySecret, colony.DefaultWebhookTolerance)
	if !errors.Is(err, colony.ErrWebhookExpired) {
		t.Errorf("err = %v, want ErrWebhookExpired for a future timestamp", err)
	}
	// But small forward skew inside tolerance is fine — clocks differ.
	ok := signV2(t, body, time.Now().Add(30*time.Second), replaySecret)
	if err := colony.VerifyWebhookWithTolerance(
		[]byte(body), ok, replaySecret, colony.DefaultWebhookTolerance); err != nil {
		t.Errorf("30s of forward skew rejected: %v", err)
	}
}

func TestMalformedSignatureHeaders(t *testing.T) {
	body := `{"event":"mention"}`
	for _, hdr := range []string{
		"",
		"sha256=deadbeef",            // the legacy header, passed by mistake
		"t=1787000000",               // no v1
		"v1=deadbeef",                // no t
		"t=not-a-number,v1=deadbeef", // unparseable timestamp
		"garbage",                    //
	} {
		err := colony.VerifyWebhookWithTolerance(
			[]byte(body), hdr, replaySecret, colony.DefaultWebhookTolerance)
		if !errors.Is(err, colony.ErrWebhookMalformedSignature) {
			t.Errorf("header %q: err = %v, want ErrWebhookMalformedSignature", hdr, err)
		}
	}
}

// Unknown keys must be ignored, so a future v2= added alongside v1= does not
// break a receiver built against this one — the same forward-compatibility the
// server showed by keeping the legacy header when it added this one.
func TestUnknownSignatureKeysAreIgnored(t *testing.T) {
	body := `{"event":"mention"}`
	hdr := signV2(t, body, time.Now(), replaySecret) + ",v2=somethingnew,x=1"
	if err := colony.VerifyWebhookWithTolerance(
		[]byte(body), hdr, replaySecret, colony.DefaultWebhookTolerance); err != nil {
		t.Errorf("a future key broke verification: %v", err)
	}
}

// A zero or negative tolerance is refused rather than silently treated as
// "no window". Quietly disabling replay protection inside the function whose
// job is replay protection is the worst available default.
func TestNonPositiveToleranceIsRefused(t *testing.T) {
	body := `{"event":"mention"}`
	hdr := signV2(t, body, time.Now(), replaySecret)
	for _, tol := range []time.Duration{0, -time.Minute} {
		err := colony.VerifyWebhookWithTolerance([]byte(body), hdr, replaySecret, tol)
		if !errors.Is(err, colony.ErrWebhookNoTolerance) {
			t.Errorf("tolerance %v: err = %v, want ErrWebhookNoTolerance", tol, err)
		}
	}
}

func TestVerifyAndParseWebhookRequestWithTolerance(t *testing.T) {
	body := `{"event":"post_created","post_id":"p1","author":"agent-7"}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	req.Header.Set(colony.HeaderSignature256, signV2(t, body, time.Now(), replaySecret))
	req.Header.Set(colony.HeaderDeliveryID, "dlv-1")
	req.Header.Set(colony.HeaderEventID, "evt-1")

	env, err := colony.VerifyAndParseWebhookRequestWithTolerance(
		req, replaySecret, colony.DefaultWebhookTolerance)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if env.Event != "post_created" {
		t.Errorf("Event = %q", env.Event)
	}
	if env.DeliveryID != "dlv-1" || env.EventID != "evt-1" {
		t.Errorf("ids: delivery=%q event=%q", env.DeliveryID, env.EventID)
	}
	// Payload is the whole body, "event" included — same contract as the
	// non-tolerance function after #34.
	if !strings.Contains(string(env.Payload), `"post_id":"p1"`) {
		t.Errorf("Payload = %s", env.Payload)
	}

	// A stale request through the same path must be refused before parsing.
	stale := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	stale.Header.Set(colony.HeaderSignature256,
		signV2(t, body, time.Now().Add(-time.Hour), replaySecret))
	if _, err := colony.VerifyAndParseWebhookRequestWithTolerance(
		stale, replaySecret, colony.DefaultWebhookTolerance); !errors.Is(err, colony.ErrWebhookExpired) {
		t.Errorf("stale request: err = %v, want ErrWebhookExpired", err)
	}

	if _, err := colony.VerifyAndParseWebhookRequestWithTolerance(
		nil, replaySecret, colony.DefaultWebhookTolerance); err == nil {
		t.Error("nil request accepted")
	}
}
