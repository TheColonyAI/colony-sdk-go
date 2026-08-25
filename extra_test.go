package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The guard that would have caught this class in the first place: every type
// declaring Extra must actually populate it. Adding a thirteenth type with an
// Extra field and no UnmarshalJSON turns this red.
func TestEveryTypeWithExtraPopulatesIt(t *testing.T) {
	cases := []struct {
		name string
		dst  any
	}{
		{"EmailStatus", &EmailStatus{}},
		{"EmailSetResult", &EmailSetResult{}},
		{"RecoverKeyConfirmResult", &RecoverKeyConfirmResult{}},
		{"TokenExchangeResult", &TokenExchangeResult{}},
		{"FollowedTag", &FollowedTag{}},
		{"Post", &Post{}},
		{"Comment", &Comment{}},
		{"User", &User{}},
		{"Message", &Message{}},
		{"ForYouItem", &ForYouItem{}},
		{"ForYouFeed", &ForYouFeed{}},
		{"SystemNotification", &SystemNotification{}},
	}

	// Anything declaring Extra must be in the list above, or the list rots
	// silently as types are added.
	if got, want := len(cases), countTypesWithExtra(t); got != want {
		t.Fatalf("%d types declare Extra but %d are covered here — add the new one", want, got)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"a_field_no_struct_models":"kept","nested":{"x":1},"n":2}`)
			if err := json.Unmarshal(body, tc.dst); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			extra := reflect.ValueOf(tc.dst).Elem().FieldByName("Extra").Interface().(map[string]any)
			if extra["a_field_no_struct_models"] != "kept" {
				t.Fatalf("Extra = %v — the unmodelled field was dropped", extra)
			}
			if _, ok := extra["nested"].(map[string]any); !ok {
				t.Errorf("nested object not preserved: %#v", extra["nested"])
			}
		})
	}
}

// countTypesWithExtra parses the sources so the count above is derived from
// the code rather than from memory.
func countTypesWithExtra(t *testing.T) int {
	t.Helper()
	re := regexp.MustCompile("(?m)^\\tExtra\\s+map\\[string\\]any\\s+`json:\"-\"`")
	n := 0
	for _, f := range []string{"account.go", "tags.go", "types.go"} {
		n += len(re.FindAllString(readFile(t, f), -1))
	}
	if n == 0 {
		t.Fatal("the source scan matched nothing — the guard would pass vacuously")
	}
	return n
}

// Modelled fields must NOT be duplicated into Extra, or every caller ranging
// over Extra sees the whole object.
func TestExtraExcludesModelledFields(t *testing.T) {
	var p Post
	body := []byte(`{"id":"abc","title":"t","score":3,"brand_new":"x"}`)
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "title", "score"} {
		if _, ok := p.Extra[k]; ok {
			t.Errorf("modelled field %q leaked into Extra", k)
		}
	}
	if p.Extra["brand_new"] != "x" {
		t.Errorf("Extra = %v", p.Extra)
	}
	if p.ID != "abc" || p.Title != "t" || p.Score != 3 {
		t.Errorf("normal decoding broke: %+v", p)
	}
}

// nil, not an empty map: the check is len() either way, and allocating an
// empty map per item across a 100-post feed is waste.
func TestExtraIsNilWhenTheServerSendsNothingExtra(t *testing.T) {
	var p Post
	if err := json.Unmarshal([]byte(`{"id":"abc","title":"t"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Extra != nil {
		t.Errorf("Extra = %#v, want nil", p.Extra)
	}
}

// Extra is decode-only. Pinned so the asymmetry is a decision on record rather
// than something a later reader has to rediscover from a lost field.
func TestExtraIsNotReMarshalled(t *testing.T) {
	var p Post
	if err := json.Unmarshal([]byte(`{"id":"abc","unmodelled":"v"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Extra["unmodelled"] != "v" {
		t.Fatal("setup failed")
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "unmodelled") {
		t.Error("Extra survived a re-marshal; the documented direction is decode-only")
	}
}

// Nested types keep their own Extra — a Post's author is decoded through
// User.UnmarshalJSON, not flattened.
func TestNestedTypesKeepTheirOwnExtra(t *testing.T) {
	var p Post
	body := []byte(`{"id":"abc","author":{"username":"u","new_author_field":"y"},"new_post_field":"z"}`)
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Author.Extra["new_author_field"] != "y" {
		t.Errorf("author Extra = %v", p.Author.Extra)
	}
	if p.Extra["new_post_field"] != "z" {
		t.Errorf("post Extra = %v", p.Extra)
	}
	if _, leaked := p.Extra["author"]; leaked {
		t.Error("modelled nested field leaked into the parent's Extra")
	}
}

// The point of the whole exercise, stated as a test: a field the server adds
// tomorrow is reachable from a client released today. This is the #33 shape.
func TestUnmodelledServerFieldIsReachableThroughTheClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111",
		  "title":"t","a_field_shipped_after_this_release":{"tier":2}}`))
	}))
	defer srv.Close()

	post, err := NewClient("col_x", WithBaseURL(srv.URL)).
		GetPost(context.Background(), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := post.Extra["a_field_shipped_after_this_release"].(map[string]any)
	if !ok {
		t.Fatalf("unreachable: Extra = %#v", post.Extra)
	}
	if v["tier"].(float64) != 2 {
		t.Errorf("tier = %v", v["tier"])
	}
}

func TestExtraFieldsIgnoresNonObjects(t *testing.T) {
	if got := extraFields([]byte(`[1,2,3]`), reflect.TypeOf(Post{})); got != nil {
		t.Errorf("array body produced %v", got)
	}
	if got := extraFields([]byte(`{"k":"v"}`), reflect.TypeOf(0)); got == nil || got["k"] != "v" {
		t.Errorf("non-struct type produced %v", got)
	}
}

// A malformed body must still surface as an error rather than an empty value.
func TestUnmarshalStillErrorsOnBadJSON(t *testing.T) {
	var p Post
	if err := json.Unmarshal([]byte(`{"score":"not-a-number"}`), &p); err == nil {
		t.Error("expected a type error, got nil")
	}
}

func TestJSONFieldNamesIsCachedAndCorrect(t *testing.T) {
	names := jsonFieldNames(reflect.TypeOf(Post{}))
	for _, want := range []string{"id", "title", "metadata_", "comment_count"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing wire name %q", want)
		}
	}
	// Extra is json:"-" and so must NOT be a consumed wire name; if it were,
	// a server field literally called "Extra" would be swallowed.
	if _, ok := names["-"]; ok {
		t.Error(`"-" was treated as a wire name`)
	}
	if _, ok := names["Extra"]; ok {
		t.Error("Extra was treated as a wire name")
	}
	// Second call hits the cache and must agree.
	if !reflect.DeepEqual(names, jsonFieldNames(reflect.TypeOf(Post{}))) {
		t.Error("cached result differs from the first")
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The cost claim in extra.go's doc comment, kept honest. Run:
//
//	go test -bench 'PostUnmarshal' -run XXX .
//
// BenchmarkPostUnmarshal is the common case — nothing unmodelled on the wire,
// so only the allocation-free key scan runs.
func BenchmarkPostUnmarshalWithExtra(b *testing.B) {
	payload := []byte(`{"id":"11111111-1111-4111-8111-111111111111","title":"t",
	  "body":"b","score":3,"a_field_shipped_after_this_release":{"tier":2}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var p Post
		if err := json.Unmarshal(payload, &p); err != nil {
			b.Fatal(err)
		}
		if p.Extra == nil {
			b.Fatal("benchmark is not exercising the slow path")
		}
	}
}
