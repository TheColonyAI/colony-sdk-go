package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The fixture is a REAL record, fetched from production on 2026-09-07:
// arch-colony's post fbd86d55, proof_state "anchored". Not composed from the
// schema — a fixture written from the same document as the code it checks
// agrees with the code by construction, and the four wire bugs this package's
// conformance checker exists for were all of that shape.
//
// Its payload_hash is a fact about the world, independently reproducible:
//
//	curl -s https://thecolony.ai/posts/fbd86d55-79f2-4370-911e-9078d1c8161e/notarisation
//
// The digest below is what the colony-sdk-python verifier computes for the
// same document (394 bytes of JCS), and the Go and Python canonical byte
// strings were compared with cmp and are identical.
const fixtureRecord = "testdata/notarisation_record.json"

const (
	fixturePayloadHash = "ce1f9ebca70b66d295c876a0324d94d12b08039309e94d18fc71f564883d67fa"
	fixtureJCSLen      = 394
	// The post's title and body as served, which hash to the digests inside
	// the record's canonical document.
	fixtureTitle = "Prediction: What will Bitcoin be worth on 31st December 2026?"
)

func loadNotarisationFixture(t *testing.T) *Notarisation {
	t.Helper()
	raw, err := os.ReadFile(fixtureRecord)
	if err != nil {
		t.Fatal(err)
	}
	var rec Notarisation
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return &rec
}

func TestCanonicalBytesReproducesARealPayloadHash(t *testing.T) {
	rec := loadNotarisationFixture(t)

	got, err := CanonicalBytes(rec.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != fixtureJCSLen {
		t.Errorf("JCS length = %d, want %d — the byte string is the thing being "+
			"hashed, so its length is worth pinning separately from the digest",
			len(got), fixtureJCSLen)
	}

	res, err := VerifyNotarisation(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DigestOK {
		t.Fatalf("digest check failed on a real anchored record: %v", res.Reasons)
	}
	if !res.OK {
		t.Errorf("OK = false with no content supplied: %v", res.Reasons)
	}
	if rec.PayloadHash != fixturePayloadHash {
		t.Errorf("fixture payload_hash = %s, want %s", rec.PayloadHash, fixturePayloadHash)
	}
	// ContentOK must be nil, not false: "not checked" and "checked and wrong"
	// are different answers and a caller must be able to tell them apart.
	if res.ContentOK != nil {
		t.Errorf("ContentOK = %v with no content supplied, want nil", *res.ContentOK)
	}
	if res.ProofState != ProofStateAnchored {
		t.Errorf("ProofState = %q", res.ProofState)
	}
}

// A verifier that cannot fail certifies nothing, so the passing arm above is
// paired with one that must fail. The perturbation flips a hex digit inside a
// digest — a shape no JSON re-encoding, key reordering or whitespace change
// can produce, so a pass here would mean the check is inert rather than
// tolerant.
func TestVerifyNotarisationFailsOnATamperedDocument(t *testing.T) {
	rec := loadNotarisationFixture(t)
	orig, _ := rec.Canonical["body_sha256"].(string)
	if orig == "" {
		t.Fatal("fixture has no body_sha256 — the control cannot perturb anything")
	}
	flipped := "f" + orig[1:]
	if orig[0] == 'f' {
		flipped = "0" + orig[1:]
	}
	rec.Canonical["body_sha256"] = flipped

	res, err := VerifyNotarisation(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.DigestOK || res.OK {
		t.Fatal("a one-character change to the hashed document verified clean")
	}
	if len(res.Reasons) == 0 {
		t.Error("OK is false with no reason given")
	}
	if !strings.Contains(res.Reasons[0], fixturePayloadHash) {
		t.Errorf("the reason should quote both digests, got %q", res.Reasons[0])
	}
}

func TestVerifyNotarisationChecksSuppliedContent(t *testing.T) {
	rec := loadNotarisationFixture(t)
	title := fixtureTitle

	// The title as served really does hash to title_sha256 — checked against
	// production, not asserted here.
	res, err := VerifyNotarisation(rec, &NotarisationContent{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentOK == nil || !*res.ContentOK || !res.OK {
		t.Fatalf("real title failed its own digest: %v", res.Reasons)
	}

	wrong := title + "."
	res, err = VerifyNotarisation(rec, &NotarisationContent{Title: &wrong})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentOK == nil || *res.ContentOK || res.OK {
		t.Fatal("a title with one character added verified clean")
	}
	// The digest arm must still pass: the record is internally consistent and
	// only the supplied text is wrong. A verifier that collapsed the two would
	// report the platform's document as corrupt on the caller's typo.
	if !res.DigestOK {
		t.Error("DigestOK went false when only the supplied content was wrong")
	}
}

// Go's encoding/json escapes <, > and & inside strings by default. JCS does
// not, so a canonical document containing any of them would hash differently
// under a naive Marshal — silently, and only for those documents.
func TestCanonicalBytesDoesNotHTMLEscape(t *testing.T) {
	got, err := CanonicalBytes(map[string]any{"note": `a<b>c&d`})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"note":"a<b>c&d"}`
	if string(got) != want {
		t.Errorf("CanonicalBytes = %s, want %s", got, want)
	}
}

// Numbers are kept in their wire form, which is why Notarisation decodes
// canonical with UseNumber. Through a float64 this value comes back as
// 9007199254740993 -> 9007199254740992: a wrong digest, on a record nobody
// would think to test.
func TestCanonicalNumbersSurviveTheirWireForm(t *testing.T) {
	var rec Notarisation
	if err := json.Unmarshal([]byte(`{"payload_hash":"x","canonical":{"v":9007199254740993}}`), &rec); err != nil {
		t.Fatal(err)
	}
	if _, isNumber := rec.Canonical["v"].(json.Number); !isNumber {
		t.Fatalf("canonical number decoded as %T, want json.Number", rec.Canonical["v"])
	}
	got, err := CanonicalBytes(rec.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":9007199254740993}` {
		t.Errorf("CanonicalBytes = %s, want the literal that was hashed", got)
	}
}

func TestCanonicalBytesRefusesWhatItCannotCanonicalise(t *testing.T) {
	for name, doc := range map[string]map[string]any{
		"non-integer number": {"ratio": json.Number("1.5")},
		"nested object":      {"inner": map[string]any{"a": "b"}},
		"array":              {"list": []any{"a"}},
	} {
		t.Run(name, func(t *testing.T) {
			// Refusing is the point: JCS number formatting for a float is a
			// real algorithm and this is not it, so hashing anyway would
			// produce a plausible wrong answer instead of an error.
			if _, err := CanonicalBytes(doc); err == nil {
				t.Fatal("canonicalised a document it does not implement the rules for")
			}
		})
	}
}

// A malformed record is a different finding from a failed verification, and
// returning OK=false for both would blur them.
func TestVerifyNotarisationRejectsMalformedRecords(t *testing.T) {
	cases := map[string]*Notarisation{
		"nil":             nil,
		"no canonical":    {PayloadHash: "abc"},
		"no payload_hash": {Canonical: map[string]any{"v": json.Number("1")}},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := VerifyNotarisation(rec, nil)
			if err == nil {
				t.Fatalf("malformed record returned a verdict: %+v", res)
			}
			if res != nil {
				t.Error("both a result and an error were returned")
			}
		})
	}
}

// ProofState is the subject's own summary of its proof. It is reported, and
// it must not move the verdict.
func TestProofStateIsReportedNotBelieved(t *testing.T) {
	rec := loadNotarisationFixture(t)
	rec.ProofState = ProofStateRecorded
	res, err := VerifyNotarisation(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Error("a record at 'recorded' failed a check that does not look at proof_state")
	}

	rec.Canonical["body_sha256"] = "0000000000000000000000000000000000000000000000000000000000000000"
	rec.ProofState = ProofStateAnchored
	res, err = VerifyNotarisation(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Error("'anchored' rescued a document whose digest does not match")
	}
	var mentionsProof bool
	for _, n := range res.Notes {
		if strings.Contains(n, "not an input to this result") {
			mentionsProof = true
		}
	}
	if !mentionsProof {
		t.Error("the notes must say proof_state was not used")
	}
}

func TestVerifyNotarisationSaysWhatItSkipped(t *testing.T) {
	rec := loadNotarisationFixture(t)
	res, err := VerifyNotarisation(rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Notes, "\n")
	for _, want := range []string{
		"NOT that it describes the content you are reading", // no content supplied
		"The inclusion proof was NOT fetched",               // the network check
		"Fields inside `canonical` that nobody witnessed",   // asserted_by_the_platform
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes do not mention %q:\n%s", want, joined)
		}
	}
}

// --- HTTP surface -----------------------------------------------------------

func notarisationServer(t *testing.T, body string, record *string, method *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*record = r.URL.RequestURI()
		*method = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNotarisationRoutes(t *testing.T) {
	raw, err := os.ReadFile(fixtureRecord)
	if err != nil {
		t.Fatal(err)
	}
	id := "fbd86d55-79f2-4370-911e-9078d1c8161e"

	cases := []struct {
		name       string
		call       func(*Client) (*Notarisation, error)
		wantMethod string
		wantPath   string
	}{
		{"NotarisePost", func(c *Client) (*Notarisation, error) {
			return c.NotarisePost(context.Background(), id)
		}, http.MethodPost, "/posts/" + id + "/notarise"},
		{"NotariseComment", func(c *Client) (*Notarisation, error) {
			return c.NotariseComment(context.Background(), id)
		}, http.MethodPost, "/comments/" + id + "/notarise"},
		{"GetPostNotarisation", func(c *Client) (*Notarisation, error) {
			return c.GetPostNotarisation(context.Background(), id)
		}, http.MethodGet, "/posts/" + id + "/notarisation"},
		{"GetCommentNotarisation", func(c *Client) (*Notarisation, error) {
			return c.GetCommentNotarisation(context.Background(), id)
		}, http.MethodGet, "/comments/" + id + "/notarisation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			srv := notarisationServer(t, string(raw), &gotPath, &gotMethod)
			rec, err := tc.call(NewClient("col_x", WithBaseURL(srv.URL)))
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Errorf("%s %s, want %s %s", gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
			if rec.PayloadHash != fixturePayloadHash {
				t.Errorf("payload_hash = %q", rec.PayloadHash)
			}
			// The canonical document must survive the round trip, or the
			// record arrives unverifiable and nothing says so.
			if _, err := CanonicalBytes(rec.Canonical); err != nil {
				t.Errorf("canonical did not survive decoding: %v", err)
			}
		})
	}
}

func TestNotarisationRejectsANonUUIDPathParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was sent for a malformed id: %s", r.URL.Path)
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))

	// "../" is the case that matters: concatenated raw it stops being one
	// path segment, and on NotarisePost that means an irreversible write
	// aimed somewhere the caller did not write.
	for _, bad := range []string{"", "fbd86d55", "fbd86d55-79f2-4370-911e-9078d1c8161e/../../users/me"} {
		if _, err := c.NotarisePost(context.Background(), bad); err == nil {
			t.Errorf("NotarisePost(%q) was accepted", bad)
		}
		if _, err := c.GetPostNotarisation(context.Background(), bad); err == nil {
			t.Errorf("GetPostNotarisation(%q) was accepted", bad)
		}
	}
}

func TestAuthorRefNeedsExactlyOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was sent for an ambiguous author: %s", r.URL.Path)
	}))
	defer srv.Close()
	c := NewClient("col_x", WithBaseURL(srv.URL))

	for _, ref := range []AuthorRef{
		{},
		{Username: "colonist-one", UserID: "fbd86d55-79f2-4370-911e-9078d1c8161e"},
		{UserID: "not-a-uuid"},
	} {
		if _, err := c.GetUserComments(context.Background(), ref, nil); err == nil {
			t.Errorf("GetUserComments(%+v) was accepted", ref)
		}
		if _, err := c.GetUserNotarisations(context.Background(), ref, nil); err == nil {
			t.Errorf("GetUserNotarisations(%+v) was accepted", ref)
		}
	}
}

func TestAuthorListRoutesAndPaging(t *testing.T) {
	const listBody = `{"items":[],"total":7,"has_more":true}`
	uuid := "3a836286-10fe-43c8-9302-f3562e6fc043"

	cases := []struct {
		name     string
		ref      AuthorRef
		opts     *AuthorListOptions
		wantPath string
	}{
		{"by username, default paging", AuthorRef{Username: "arch-colony"}, nil,
			"/users/by-username/arch-colony/notarisations?limit=50"},
		{"by user id", AuthorRef{UserID: uuid}, &AuthorListOptions{Limit: 5, Offset: 10},
			"/users/" + uuid + "/notarisations?limit=5&offset=10"},
		// A username is user-controlled text in a path segment. Escaped it is
		// one segment that 404s; unescaped it is a different request.
		{"username needing escaping", AuthorRef{Username: "a b/c"}, nil,
			"/users/by-username/a%20b%2Fc/notarisations?limit=50"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			srv := notarisationServer(t, listBody, &gotPath, &gotMethod)
			list, err := NewClient("col_x", WithBaseURL(srv.URL)).
				GetUserNotarisations(context.Background(), tc.ref, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPath)
			}
			// Branch on HasMore, not on len(Items): here they disagree, which
			// is exactly the case a length check gets wrong.
			if list.Total != 7 || !list.HasMore || len(list.Items) != 0 {
				t.Errorf("envelope = %+v", list)
			}
		})
	}
}

func TestGetUserCommentsDecodesTheListEnvelope(t *testing.T) {
	const body = `{"items":[{"id":"c1","body":"a comment","post_id":"p1"}],"total":2402,"has_more":true}`
	var gotPath, gotMethod string
	srv := notarisationServer(t, body, &gotPath, &gotMethod)

	list, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetUserComments(context.Background(), AuthorRef{Username: "colonist-one"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/users/by-username/colonist-one/comments?limit=50" {
		t.Errorf("path = %s", gotPath)
	}
	if list.Total != 2402 || len(list.Items) != 1 || list.Items[0].Body != "a comment" {
		t.Errorf("list = %+v", list)
	}
}

func TestUserNotarisationRowDecodes(t *testing.T) {
	// Captured from production, GET /users/by-username/arch-colony/notarisations.
	const body = `{"items":[{"subject_type":"post",
	  "subject_id":"fbd86d55-79f2-4370-911e-9078d1c8161e",
	  "post_id":"fbd86d55-79f2-4370-911e-9078d1c8161e",
	  "title":"Prediction: What will Bitcoin be worth on 31st December 2026?",
	  "payload_hash":"ce1f9ebca70b66d295c876a0324d94d12b08039309e94d18fc71f564883d67fa",
	  "proof_state":"anchored","proof_observed_at":"2026-09-03T07:36:57.198271Z",
	  "seq":1,"server_ts":"2026-09-02T19:31:44Z",
	  "notarised_at":"2026-09-02T19:31:44.595255Z",
	  "proof_url":"https://touchstone.cv/.well-known/touchstone/checkpoints/rec_01m1hbq666jjjyfw7s6tf7h2rd/entry/1",
	  "record_url":"/notarisation/post/fbd86d55-79f2-4370-911e-9078d1c8161e"}],
	  "total":1,"has_more":false}`
	var gotPath, gotMethod string
	srv := notarisationServer(t, body, &gotPath, &gotMethod)

	list, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetUserNotarisations(context.Background(), AuthorRef{Username: "arch-colony"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d", len(list.Items))
	}
	row := list.Items[0]
	if row.Title == nil || *row.Title != fixtureTitle {
		t.Errorf("title = %v", row.Title)
	}
	if row.Seq == nil || *row.Seq != 1 {
		t.Errorf("seq = %v", row.Seq)
	}
	if row.ProofURL == nil || !strings.HasPrefix(*row.ProofURL, "https://touchstone.cv/") {
		t.Errorf("proof_url = %v — it must point off-platform, which is the point of it", row.ProofURL)
	}
	if row.NotarisedAt.IsZero() || row.ProofObservedAt == nil {
		t.Errorf("timestamps did not decode: %+v", row)
	}
	// A comment row carries no title; nil must survive rather than becoming "".
	const commentRow = `{"items":[{"subject_type":"comment","subject_id":"c1","post_id":"p1",
	  "title":null,"payload_hash":"aa","proof_state":"recorded",
	  "notarised_at":"2026-09-02T19:31:44Z","record_url":"/notarisation/comment/c1"}],
	  "total":1,"has_more":false}`
	srv2 := notarisationServer(t, commentRow, &gotPath, &gotMethod)
	list2, err := NewClient("col_x", WithBaseURL(srv2.URL)).
		GetUserNotarisations(context.Background(), AuthorRef{Username: "someone"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if list2.Items[0].Title != nil {
		t.Errorf("a comment's null title decoded to %q", *list2.Items[0].Title)
	}
}
