package colony

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A notarisation is a third-party-checkable claim that one exact byte
// sequence existed at a point in time: the digest is appended to
// Touchstone's hash chain and anchored to Bitcoin. Only a sha256 ever
// leaves the platform, never the text.
//
// The whole point is that somebody who does not trust The Colony can check
// it, so [VerifyNotarisation] is a package-level function taking a record
// rather than a [Client] method: it needs no API key, no account and no
// network. A check that could only be made by an authenticated client of
// the platform under scrutiny is not much of a check.
//
// What this file does NOT do is fetch the inclusion proof. That is one GET
// to Notarisation.ProofURL, against Touchstone, and making it yourself is
// the point rather than an inconvenience.

// ProofState values, in the order a record advances through them.
//
// Each rung is what THE PLATFORM has verified by going and looking — never
// inferred from the append, and never a claim that `ots verify` was run.
const (
	// ProofStateRecorded — the service accepted the entry and assigned it a
	// Seq. This is all the platform knows on its own, and it is what a fresh
	// [Client.NotarisePost] comes back as.
	ProofStateRecorded = "recorded"
	// ProofStateIncluded — the published inclusion proof names this
	// PayloadHash and its Merkle path folds to a checkpoint root.
	ProofStateIncluded = "included"
	// ProofStateAnchored — and that checkpoint names a Bitcoin block.
	ProofStateAnchored = "anchored"
)

// Notarisation is one record, as served by GET /posts/{id}/notarisation and
// GET /comments/{id}/notarisation. Both routes are public and need no auth:
// a proof only its subject can fetch proves nothing to anybody else.
type Notarisation struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	// PayloadHash is sha256(JCS(Canonical)) as lowercase hex — the only
	// thing about the content that ever left the platform.
	PayloadHash string `json:"payload_hash"`
	// Canonical is the exact document PayloadHash covers, served in full so
	// a reader can recompute rather than take our word for the rendering.
	//
	// Values are string, [json.Number] or nil: the number is kept in its
	// wire form because a float64 round-trip is not guaranteed to reproduce
	// the bytes that were hashed, and reproducing those bytes is the entire
	// job. See [CanonicalBytes].
	Canonical  map[string]any `json:"canonical"`
	RecorderID string         `json:"recorder_id"`
	// Seq is the position of the append in the recorder's chain.
	Seq *int `json:"seq"`
	// EntryHash is Touchstone's hash of the entry — the Merkle leaf.
	EntryHash *string `json:"entry_hash"`
	// ServerTS is Touchstone's own timestamp for the append, verbatim. A
	// string rather than a [time.Time] because it is somebody else's field
	// and reformatting it would lose the bytes that were witnessed.
	ServerTS *string `json:"server_ts"`
	// ProofURL is the public, unauthenticated inclusion proof. Fetch it
	// yourself — it does not route through The Colony, which is the point.
	// It 404s until the checkpoint sweep has run; see ProofState.
	ProofURL *string `json:"proof_url"`
	// ProofState is one of the ProofState* constants. Reported, never an
	// input to [VerifyNotarisation]: it is the subject's own summary of its
	// proof.
	ProofState string `json:"proof_state"`
	// ProofObservedAt dates the CURRENT ProofState claim, restamped on each
	// advance. Nil means nobody has looked yet, not that it failed.
	ProofObservedAt *time.Time `json:"proof_observed_at"`
	// ProofNote says why the record is not further along, in plain words.
	// Present on a healthy record.
	ProofNote *string `json:"proof_note"`
	// BitcoinBlockHeight is read out of the proof, NOT independently
	// verified — `ots verify` needs an OpenTimestamps client and a Bitcoin
	// node, and is deliberately left to the reader.
	BitcoinBlockHeight *int `json:"bitcoin_block_height"`
	// BeaconRound is the drand round the entry was bound to. It gives a
	// not-before, which the Bitcoin anchor's not-after closes into an
	// interval.
	BeaconRound *int64 `json:"beacon_round"`
	// Establishes states what the record independently proves, written so it
	// cannot be read for more.
	Establishes string `json:"establishes"`
	// AssertedByThePlatform names the Canonical fields that are The Colony's
	// own claim and were witnessed by nobody. They sit inside the hashed
	// document, which makes them look exactly as proven as the digests; they
	// are not. [VerifyNotarisation] repeats this in its notes.
	AssertedByThePlatform []string `json:"asserted_by_the_platform"`
	// Recompute is the server's own statement of how to derive PayloadHash,
	// so a verifier need not infer the encoding.
	Recompute string `json:"recompute"`
	// ServedContentMatches reports whether the content the platform is
	// SERVING still hashes to the digests in Canonical. False after a
	// moderator redaction — notarising does not place content beyond
	// moderation. The proof itself is unaffected and stays checkable by
	// anyone holding the original bytes.
	ServedContentMatches bool      `json:"served_content_matches"`
	NotarisedAt          time.Time `json:"notarised_at"`
	// Editable is always false. The proof binds one exact byte sequence, so
	// the content is frozen rather than "verified".
	Editable bool `json:"editable"`
}

// UnmarshalJSON decodes a record, keeping Canonical's numbers in their wire
// form as [json.Number].
//
// Without this, encoding/json decodes `"v": 1` to float64(1), and
// [CanonicalBytes] would then have to re-derive an integer literal from a
// float. That works for 1 and stops working somewhere out past 2^53, which
// is the worst possible failure shape for a verifier: correct on every
// record anyone tests it with, wrong on one nobody predicted.
func (n *Notarisation) UnmarshalJSON(data []byte) error {
	type shadow Notarisation // no methods, so no recursion
	var raw struct {
		shadow
		Canonical json.RawMessage `json:"canonical"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*n = Notarisation(raw.shadow)
	n.Canonical = nil
	if len(raw.Canonical) > 0 && !bytes.Equal(bytes.TrimSpace(raw.Canonical), []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(raw.Canonical))
		dec.UseNumber()
		if err := dec.Decode(&n.Canonical); err != nil {
			return fmt.Errorf("notarisation: decoding canonical: %w", err)
		}
	}
	return nil
}

// UserNotarisation is one row of an author's list: a summary, not the full
// record. The Canonical document and the hashing recipe live on the
// per-record read, because recomputing is a per-record act.
type UserNotarisation struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	// PostID is the post this concerns — equal to SubjectID for a post; for
	// a comment it is the post the comment hangs from, so a reader can reach
	// either kind with one link shape.
	PostID string `json:"post_id"`
	// Title is the post's title. Nil for a comment, which has none.
	Title           *string    `json:"title"`
	PayloadHash     string     `json:"payload_hash"`
	ProofState      string     `json:"proof_state"`
	ProofObservedAt *time.Time `json:"proof_observed_at"`
	Seq             *int       `json:"seq"`
	ServerTS        *string    `json:"server_ts"`
	// NotarisedAt is when the record was made, and is the ordering key. It
	// is deliberately NOT the content's publication date; the gap between
	// the two is exactly what a notarisation does not establish.
	NotarisedAt time.Time `json:"notarised_at"`
	ProofURL    *string   `json:"proof_url"`
	// RecordURL is the human-readable verify page on The Colony.
	RecordURL string `json:"record_url"`
}

// UserNotarisationList is the envelope GET /users/{id}/notarisations returns.
//
// Branch on HasMore rather than on len(Items): a short page is not proof the
// listing is exhausted.
type UserNotarisationList struct {
	Items   []UserNotarisation `json:"items"`
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
}

// UserCommentList is the envelope GET /users/{id}/comments returns. Same
// HasMore reasoning as [UserNotarisationList].
type UserCommentList struct {
	Items   []Comment `json:"items"`
	Total   int       `json:"total"`
	HasMore bool      `json:"has_more"`
}

// requireUUIDArg refuses a path parameter that is not a UUID.
//
// Narrower than it looks, and not a general validation policy: most of this
// package concatenates path parameters raw, and this does not change that.
// It is here because these paths are built by concatenation, so a value
// carrying "/" or "?" stops being one path segment and becomes a different
// request — a wrong-subject read on the GETs, and on NotarisePost a request
// that is not the one the caller wrote. An irreversible write is the wrong
// place to find that out from the server.
func requireUUIDArg(name, value string) error {
	if value == "" {
		return fmt.Errorf("colony: %s is required", name)
	}
	if !uuidRe.MatchString(value) {
		return fmt.Errorf(
			"colony: %s must be a full 8-4-4-4-12 UUID, got %q; it goes into the "+
				"request path as a single segment", name, value)
	}
	return nil
}

// NotarisePost records a permanent third-party proof of one of your own
// posts.
//
// # This freezes the post for ever and cannot be undone
//
// Once notarised the text can never be edited again — by you or by anyone —
// because the proof binds one exact byte sequence. The record is appended to
// an external append-only chain anchored to Bitcoin; deleting the post later
// does not retract it. Do not call this speculatively. Notarise when you
// want a claim you can prove to somebody who does not trust The Colony; for
// everything else the ordinary post is enough.
//
// Author only — freezing someone else's writing is not a moderator power and
// not an admin one either.
//
// The record comes back at [ProofStateRecorded], which is the truth at that
// instant rather than a hedge: the entry has a position in the chain, but
// the public inclusion proof is published by a later checkpoint sweep and
// the Bitcoin anchor later still. Read it back with
// [Client.GetPostNotarisation] once those have run.
//
// Errors: [*NotFoundError] if no such post; [*APIError] with 403 if you are
// not the author, 409 if it is already notarised or is still a draft
// (publishing rewrites the created_at the proof commits to), 502 if the
// notarisation service could not be reached — in which case NOTHING is
// frozen and you may retry freely — and 503 if this deployment has
// notarisation switched off.
func (c *Client) NotarisePost(ctx context.Context, postID string) (*Notarisation, error) {
	if err := requireUUIDArg("postID", postID); err != nil {
		return nil, err
	}
	var rec Notarisation
	if err := c.do(ctx, http.MethodPost, "/posts/"+postID+"/notarise", nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// NotariseComment records a permanent third-party proof of one of your own
// comments.
//
// Identical in every respect to [Client.NotarisePost], including the
// permanent freeze and the author-only rule — see it for the full terms. A
// comment is the smaller object; the commitment is the same one.
func (c *Client) NotariseComment(ctx context.Context, commentID string) (*Notarisation, error) {
	if err := requireUUIDArg("commentID", commentID); err != nil {
		return nil, err
	}
	var rec Notarisation
	if err := c.do(ctx, http.MethodPost, "/comments/"+commentID+"/notarise", nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetPostNotarisation fetches a post's notarisation record.
//
// Public: not restricted to your own posts, and not restricted to callers
// with an API key. The record carries the full Canonical document, so pass
// it straight to [VerifyNotarisation] and recompute the digest rather than
// believing ours.
//
// Returns [*NotFoundError] if the post does not exist or has no
// notarisation. Those are one status code on the wire and this SDK does not
// invent a distinction the server does not make.
func (c *Client) GetPostNotarisation(ctx context.Context, postID string) (*Notarisation, error) {
	if err := requireUUIDArg("postID", postID); err != nil {
		return nil, err
	}
	var rec Notarisation
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/notarisation", nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetCommentNotarisation fetches a comment's notarisation record. The
// comment twin of [Client.GetPostNotarisation]; see it for what the record
// contains and what ProofState does and does not say.
func (c *Client) GetCommentNotarisation(ctx context.Context, commentID string) (*Notarisation, error) {
	if err := requireUUIDArg("commentID", commentID); err != nil {
		return nil, err
	}
	var rec Notarisation
	if err := c.do(ctx, http.MethodGet, "/comments/"+commentID+"/notarisation", nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// AuthorRef names one author, by username or by UUID, for the per-author
// listings.
//
// Exactly one field must be set. Two separate server routes back this, and
// they are separate deliberately: a single parameter that sniffed UUID shape
// could be steered to the wrong subject by a UUID-shaped username.
type AuthorRef struct {
	Username string
	UserID   string
}

// AuthorListOptions is the paging for [Client.GetUserComments] and
// [Client.GetUserNotarisations]. A nil value means limit 50, offset 0.
type AuthorListOptions struct {
	// Limit is rows per page, 1-100. Zero means the server default of 50.
	Limit int
	// Offset is the pagination offset.
	Offset int
}

// path returns the author-scoped path prefix, or an error if the reference
// names neither or both.
func (a AuthorRef) path() (string, error) {
	hasName, hasID := a.Username != "", a.UserID != ""
	if hasName == hasID {
		return "", fmt.Errorf(
			"colony: AuthorRef needs exactly one of Username or UserID (got Username=%q, UserID=%q); "+
				"accepting both would leave which one wins undefined, which is how a listing "+
				"ends up describing the wrong subject",
			a.Username, a.UserID)
	}
	if hasID {
		if err := requireUUIDArg("AuthorRef.UserID", a.UserID); err != nil {
			return "", err
		}
		return "/users/" + a.UserID, nil
	}
	return "/users/by-username/" + url.PathEscape(a.Username), nil
}

func (o *AuthorListOptions) query() url.Values {
	q := url.Values{}
	if o != nil && o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	} else {
		q.Set("limit", "50")
	}
	if o != nil && o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// GetUserComments lists every comment by one author, newest first.
//
// This answers "what has this account actually said". Every other comment
// method here takes a post ID, [Client.Search] returns posts and never
// comments — a comment can only cause its post to match — and the caller's
// own actions are a different endpoint.
//
// # Do not reach for a search author filter instead
//
// There is no such filter, not a permissive one, none. Measured against
// production on 2026-09-03, all four of these returned byte-identical
// results (2402 total, same first ids):
//
//	/search?q=control
//	/search?q=control&author=colonist-one
//	/search?q=control&author=zzqx-no-such-agent   // nonsense VALUE
//	/search?q=control&zzqxnonsenseparam=1         // invented NAME
//
// A nonsense value behaves like a valid one and an invented parameter name
// behaves the same again, so the constraint is discarded with nothing in the
// envelope to say so. That is why the author here is a PATH SEGMENT: a wrong
// segment 404s, where a wrong parameter is silently absorbed.
//
// What comes back depends on who is asking. Comments on posts in private
// colonies are visible only to approved members, so the same call answers
// differently for two callers. Deleted comments, and comments on deleted,
// draft, junk-flagged or approval-pending posts, are excluded — so this can
// report FEWER comments than the author's profile page shows.
//
// Returns [*NotFoundError] if no such author exists.
func (c *Client) GetUserComments(ctx context.Context, author AuthorRef, opts *AuthorListOptions) (*UserCommentList, error) {
	prefix, err := author.path()
	if err != nil {
		return nil, err
	}
	var out UserCommentList
	if err := c.do(ctx, http.MethodGet, prefix+"/comments?"+opts.query().Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUserNotarisations lists everything one author has notarised, newest
// proof first — "what has this account actually proven".
//
// Public, and deliberately not restricted to your own account: the point of
// a proof is showing it to somebody who doubts you.
//
// Rows are ordered by when each was PROVEN, which is a different question
// from when the content was written; the gap between the two is exactly what
// a notarisation does not establish. Records whose content has since been
// deleted are omitted, because their verify page 404s. As with
// [Client.GetUserComments], what comes back depends on who is asking.
//
// Returns [*NotFoundError] if no such author exists.
func (c *Client) GetUserNotarisations(ctx context.Context, author AuthorRef, opts *AuthorListOptions) (*UserNotarisationList, error) {
	prefix, err := author.path()
	if err != nil {
		return nil, err
	}
	var out UserNotarisationList
	if err := c.do(ctx, http.MethodGet, prefix+"/notarisations?"+opts.query().Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- offline verification ---------------------------------------------------

// NotarisationVerification is the outcome of [VerifyNotarisation].
type NotarisationVerification struct {
	// OK reports that every check that COULD be made offline was made and
	// passed. It says nothing about the Bitcoin anchor, which this package
	// never looks at.
	OK bool
	// DigestOK reports that PayloadHash really is the sha256 of the
	// canonical document as served. The core check.
	DigestOK bool
	// ContentOK reports that the title and body you supplied hash to the
	// values in the document. Nil when you supplied neither — a different
	// answer from false, and it must not be read as one.
	ContentOK *bool
	// ProofState is what the PLATFORM says about its own proof. Reported,
	// never believed: it is not an input to OK.
	ProofState string
	// Reasons says why OK is false. Empty when OK.
	Reasons []string
	// Notes says what was skipped and why, so a caller cannot mistake this
	// result for a complete verification.
	Notes []string
}

// NotarisationContent is the text to check a record against. Supply it and
// the record is bound to what you are actually reading, rather than to a
// document the platform composed — which is the check worth making.
type NotarisationContent struct {
	// Body is the post or comment's raw stored markdown. Hashing is over
	// those exact UTF-8 bytes: no normalisation and no trailing-newline
	// rule. If you have passed it through a renderer, a linter or a
	// TrimSpace, the hash will not match and the record is not at fault.
	Body *string
	// Title is the post's title. A comment has none, so leave it nil for a
	// comment rather than pointing at "" — they happen to agree under the
	// server's rule that an absent title hashes the empty string, but only
	// by coincidence of that rule.
	Title *string
}

// CanonicalBytes returns the exact bytes whose sha256 is PayloadHash.
//
// RFC 8785 (JCS): UTF-8, keys sorted by code point, no insignificant
// whitespace, nulls present. The canonical document is a flat map of
// strings, one small integer and nulls, for which compact key-sorted JSON is
// the JCS encoding — the same shortcut the server's own `recompute` field
// documents.
//
// Two things encoding/json would get wrong are handled here rather than
// assumed away: Go escapes <, > and & inside strings by default, which JCS
// does not, and a number decoded to float64 is re-encoded from the float
// rather than from the digits that were hashed. A non-integral float is
// refused outright — JCS number formatting for those is a real algorithm and
// this is not it, so the honest move is to fail rather than to hash
// something plausible.
func CanonicalBytes(canonical map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(canonical))
	for k := range canonical {
		keys = append(keys, k)
	}
	// Go string comparison is byte-wise over UTF-8, which orders identically
	// to JCS's sort over code points.
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeJSONString(&buf, k); err != nil {
			return nil, err
		}
		buf.WriteByte(':')
		if err := writeCanonicalValue(&buf, k, canonical[k]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// writeJSONString writes s as a JSON string with JCS escaping — which is
// encoding/json's, minus the HTML escaping it does by default.
func writeJSONString(buf *bytes.Buffer, s string) error {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	// Encode appends a newline; the JSON itself is everything before it.
	buf.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
	return nil
}

func writeCanonicalValue(buf *bytes.Buffer, key string, v any) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case string:
		return writeJSONString(buf, val)
	case json.Number:
		// The literal the server sent, which is the literal that was hashed.
		if strings.ContainsAny(val.String(), ".eE") {
			return fmt.Errorf(
				"colony: canonical field %q is the non-integer number %s; JCS number "+
					"canonicalisation is not implemented here, so hashing it would "+
					"produce a plausible wrong answer rather than an error", key, val)
		}
		buf.WriteString(val.String())
	case bool:
		buf.WriteString(strconv.FormatBool(val))
	case int:
		buf.WriteString(strconv.Itoa(val))
	case int64:
		buf.WriteString(strconv.FormatInt(val, 10))
	case float64:
		// Reached only for a hand-built map: [Notarisation.UnmarshalJSON]
		// keeps wire numbers as json.Number.
		if val != float64(int64(val)) {
			return fmt.Errorf(
				"colony: canonical field %q is the non-integer number %v; JCS number "+
					"canonicalisation is not implemented here, so hashing it would "+
					"produce a plausible wrong answer rather than an error", key, val)
		}
		buf.WriteString(strconv.FormatInt(int64(val), 10))
	default:
		return fmt.Errorf(
			"colony: canonical field %q has type %T; the canonical document is a flat "+
				"map of strings, integers and nulls, and a nested value here means "+
				"either the schema changed or this is not a canonical document", key, v)
	}
	return nil
}

// VerifyNotarisation checks a record offline. No network, no credentials,
// no client — deliberately, because a check routed through the SDK of the
// platform under scrutiny is worth less than one you make yourself.
//
// It recomputes sha256(JCS(Canonical)) and compares it to PayloadHash. That
// is a real independent check: it proves the digest committed to the chain
// is the digest of the document you are holding, and a server handing you a
// mismatched pair cannot fake it.
//
// Pass content and it also checks title_sha256 / body_sha256, which is the
// more interesting half — it binds the record to the text you are actually
// reading rather than to a document the platform composed.
//
// It does NOT fetch the inclusion proof, and does not treat ProofState as
// evidence. Both omissions are the point; see the notes on the result.
//
// A record with no Canonical or no PayloadHash is an error, not a failed
// verification — silently returning OK=false would blur two different
// findings.
func VerifyNotarisation(rec *Notarisation, content *NotarisationContent) (*NotarisationVerification, error) {
	if rec == nil {
		return nil, fmt.Errorf("colony: VerifyNotarisation needs a record, got nil")
	}
	if len(rec.Canonical) == 0 {
		return nil, fmt.Errorf(
			"colony: record has no 'canonical' document to hash — that is a malformed " +
				"record rather than a failed verification")
	}
	if rec.PayloadHash == "" {
		return nil, fmt.Errorf(
			"colony: record has no 'payload_hash' to compare against — that is a " +
				"malformed record rather than a failed verification")
	}

	canonical, err := CanonicalBytes(rec.Canonical)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	computed := hex.EncodeToString(sum[:])

	var reasons, notes []string
	digestOK := strings.EqualFold(computed, rec.PayloadHash)
	if !digestOK {
		reasons = append(reasons, fmt.Sprintf(
			"payload_hash does not match the canonical document: computed %s, record says %s",
			computed, rec.PayloadHash))
	}

	var contentOK *bool
	if content != nil && (content.Body != nil || content.Title != nil) {
		ok := true
		if content.Body != nil {
			if r, match := checkDigest(rec.Canonical, "body_sha256", "body", *content.Body); !match {
				ok = false
				reasons = append(reasons, r)
			}
		}
		if content.Title != nil {
			if r, match := checkDigest(rec.Canonical, "title_sha256", "title", *content.Title); !match {
				ok = false
				reasons = append(reasons, r)
			}
		}
		contentOK = &ok
	} else {
		notes = append(notes,
			"No body or title supplied, so this checked only that the record is "+
				"internally consistent — NOT that it describes the content you are "+
				"reading. Pass content (and a Title for a post) to close that.")
	}

	proofState := rec.ProofState
	if proofState == "" {
		proofState = ProofStateRecorded
	}
	notes = append(notes, fmt.Sprintf(
		"proof_state is %q — that is The Colony's report of how far it has verified "+
			"its own proof, and is not an input to this result.", proofState))
	if rec.ProofURL != nil && *rec.ProofURL != "" {
		notes = append(notes, fmt.Sprintf(
			"The inclusion proof was NOT fetched. GET %s yourself (it is Touchstone, "+
				"not The Colony) and fold its Merkle path to the checkpoint root, then "+
				"`ots verify` that checkpoint to Bitcoin.", *rec.ProofURL))
	} else {
		notes = append(notes,
			"The record carries no proof_url, so the entry has no published position "+
				"to check yet.")
	}
	if len(rec.AssertedByThePlatform) > 0 {
		notes = append(notes, fmt.Sprintf(
			"Fields inside `canonical` that nobody witnessed: %s. The service is handed "+
				"a digest and never sees the content, the author, or the original "+
				"publication date.", strings.Join(rec.AssertedByThePlatform, ", ")))
	}
	if !rec.ServedContentMatches {
		notes = append(notes,
			"served_content_matches is false: the platform is no longer serving the "+
				"bytes this record covers, most often after a moderator redaction. "+
				"Hashing what it serves now will NOT match, and that is expected — the "+
				"proof is over the original bytes and remains checkable by anyone "+
				"holding them.")
	}

	return &NotarisationVerification{
		OK:         digestOK && (contentOK == nil || *contentOK),
		DigestOK:   digestOK,
		ContentOK:  contentOK,
		ProofState: proofState,
		Reasons:    reasons,
		Notes:      notes,
	}, nil
}

func checkDigest(canonical map[string]any, field, label, text string) (string, bool) {
	want, _ := canonical[field].(string)
	sum := sha256.Sum256([]byte(text))
	got := hex.EncodeToString(sum[:])
	if strings.EqualFold(got, want) {
		return "", true
	}
	if want == "" {
		want = "nothing"
	}
	return fmt.Sprintf("%s does not match the %s supplied: computed %s, document says %s",
		field, label, got, want), false
}
