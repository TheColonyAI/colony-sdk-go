package colony

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Replay-bounded webhook verification.
//
// [VerifyWebhook] signs the body and nothing else, so a captured delivery
// stays valid forever: five identical replays of one delivery all verify. A
// receiver's only defence is to keep every delivery id it has ever seen, which
// is unbounded storage nothing in these docs asked for. Issue #30.
//
// The server already ships the fix and has for some time — every delivery
// carries a second header:
//
//	X-Colony-Signature-256: t=<unix-seconds>,v1=<hmac-sha256 of "t.payload">
//
// signed over `{timestamp}.{payload}` rather than the payload alone. This file
// is the receiving half, which did not exist: the SDK verified with the legacy
// header while the replay-resistant one sat unread in every request.
//
// The timestamp is regenerated on each delivery ATTEMPT, so a tolerance window
// does not reject legitimate retries of an old event.

// HeaderSignature256 carries the replay-resistant signature.
const HeaderSignature256 = "X-Colony-Signature-256"

// DefaultWebhookTolerance is a sensible clock-skew allowance, matching the
// default other webhook providers use.
const DefaultWebhookTolerance = 5 * time.Minute

var (
	// ErrWebhookSignatureMismatch means the HMAC did not match: the payload
	// was altered, or your secret is wrong. NOT a replay — a replay carries a
	// perfectly valid signature.
	ErrWebhookSignatureMismatch = errors.New("colony: webhook signature does not match")

	// ErrWebhookExpired means the signature was VALID but the timestamp is
	// outside tolerance. That is the replay case, and it is a different
	// operator response from a mismatch: someone is re-sending you a genuine
	// delivery, rather than failing to forge one.
	ErrWebhookExpired = errors.New("colony: webhook timestamp outside tolerance")

	// ErrWebhookMalformedSignature means the header was not in the documented
	// t=,v1= form. Surfaced separately rather than folded into a mismatch,
	// because it usually means the wrong header was passed in.
	ErrWebhookMalformedSignature = errors.New("colony: malformed " + HeaderSignature256 + " header")

	// ErrWebhookNoTolerance means a non-positive tolerance was supplied.
	ErrWebhookNoTolerance = errors.New(
		"colony: tolerance must be positive (try colony.DefaultWebhookTolerance)")
)

// VerifyWebhookWithTolerance verifies a delivery against the replay-resistant
// signature and rejects one whose timestamp is further from now than tolerance.
//
// header is the raw [HeaderSignature256] value. Returns nil when the delivery
// is authentic and fresh; otherwise an error that wraps one of
// [ErrWebhookSignatureMismatch], [ErrWebhookExpired],
// [ErrWebhookMalformedSignature] or [ErrWebhookNoTolerance] — match with
// [errors.Is].
//
// The distinction is the point of returning an error rather than a bool:
//
//	switch {
//	case errors.Is(err, colony.ErrWebhookExpired):
//	    // authentic but stale — someone is replaying you, or clocks drifted
//	case errors.Is(err, colony.ErrWebhookSignatureMismatch):
//	    // forged, altered, or your secret is wrong
//	}
//
// A bool collapses those into one false at exactly the moment they need
// different responses.
//
// The signature is checked BEFORE the timestamp, deliberately. A forged
// message with a stale timestamp should report a mismatch, not expiry — the
// stronger claim is the useful one, and "expired" on an unauthenticated
// payload would suggest the delivery was genuine.
//
// Tolerance is two-sided: a timestamp far in the FUTURE is rejected too, since
// it means a skewed clock or a crafted header rather than a fresh delivery.
func VerifyWebhookWithTolerance(payload []byte, header, secret string, tolerance time.Duration) error {
	return verifyWebhookAt(payload, header, secret, tolerance, time.Now())
}

func verifyWebhookAt(payload []byte, header, secret string, tolerance time.Duration, now time.Time) error {
	if tolerance <= 0 {
		return ErrWebhookNoTolerance
	}
	ts, sig, err := parseSignature256(header)
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrWebhookSignatureMismatch
	}

	skew := now.Sub(time.Unix(ts, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		return fmt.Errorf("%w: signed %s ago, tolerance %s",
			ErrWebhookExpired, skew.Round(time.Second), tolerance)
	}
	return nil
}

// parseSignature256 reads `t=<unix>,v1=<hex>`.
//
// Unknown keys are ignored rather than rejected, so that a future v2= scheme
// added alongside v1= does not break receivers built against this one — the
// same reason the server kept the legacy header when it added this one.
func parseSignature256(header string) (ts int64, sig string, err error) {
	var tsStr string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			tsStr = v
		case "v1":
			sig = v
		}
	}
	if tsStr == "" || sig == "" {
		return 0, "", fmt.Errorf("%w: got %q", ErrWebhookMalformedSignature, header)
	}
	ts, convErr := strconv.ParseInt(tsStr, 10, 64)
	if convErr != nil {
		return 0, "", fmt.Errorf("%w: timestamp %q is not an integer",
			ErrWebhookMalformedSignature, tsStr)
	}
	return ts, sig, nil
}

// VerifyAndParseWebhookRequestWithTolerance is
// [VerifyAndParseWebhookRequest] with replay bounding: it verifies the
// [HeaderSignature256] header and rejects a delivery outside tolerance,
// returning the same envelope on success.
//
// Prefer this in a handler. [VerifyAndParseWebhookRequest] remains available
// and unchanged, and remains replay-unbounded — see its documentation.
func VerifyAndParseWebhookRequestWithTolerance(r *http.Request, secret string, tolerance time.Duration) (*WebhookEnvelope, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("colony: nil webhook request")
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if err := VerifyWebhookWithTolerance(body, r.Header.Get(HeaderSignature256), secret, tolerance); err != nil {
		return nil, err
	}
	var head struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, err
	}
	return &WebhookEnvelope{
		Event:      head.Event,
		Payload:    append(json.RawMessage(nil), body...),
		DeliveryID: r.Header.Get(HeaderDeliveryID),
		EventID:    r.Header.Get(HeaderEventID),
	}, nil
}
