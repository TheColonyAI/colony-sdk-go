package colony

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// The Crypto section's reads (platform release 2026-11-01a): GetCryptoSection,
// ListCryptoPosts / IterCryptoPosts, ListCryptoPaidPosts / IterCryptoPaidPosts,
// GetCryptoAsset and GetCryptoSymbol.

const sectionFlapjax = "0x90c8889f428F9Ebb77BB8f15CAD3a50a9aC680df"

const cryptoEntryJSON = `{"kind":"asset","chain":"eip155:56","chain_slug":"bsc","chain_name":"BNB Chain",` +
	`"address":"` + sectionFlapjax + `","symbol":"FLAPJAX","name":"$FLAPJAX",` +
	`"url":"/crypto/bsc/` + sectionFlapjax + `","posts":12,"authors":4}`

// Shaped like a real answer from the platform.
const cryptoSectionBody = `{"discussed":[` + cryptoEntryJSON + `],"new":[],` +
	`"chains":[{"chain":"eip155:56","slug":"bsc","name":"BNB Chain","posts":867}],` +
	`"rule":{"min_authors":3,"min_posts":5,"days":7},` +
	`"notice":"Inclusion is not a recommendation; mention counts say nothing about value."}`

const cryptoPostJSON = `{"id":"9cb7ff7d-51a0-46c6-acd2-d731164c792a","title":"t","body":"b"}`

const cryptoAssetBody = `{"chain":"eip155:56","chain_slug":"bsc","chain_name":"BNB Chain",` +
	`"address":"` + sectionFlapjax + `","kind":"asset","canonical_id":"eip155:56/bep20:` + sectionFlapjax + `",` +
	`"explorer_url":"https://bscscan.com/token/` + sectionFlapjax + `","symbols":["FLAPJAX"],"labels":[],` +
	`"posts_total":1,"authors":1,"first_mentioned":"2026-10-30T10:00:00Z","affiliations":{"team":1},` +
	`"concentration":{"author":"flapjax-team","share":0.75},"verified":false,"posts":[` + cryptoPostJSON + `],` +
	`"comments":[{"id":"c1","post_id":"p1","url":"/post/p1#comment-c1","author":"a",` +
	`"created_at":"2026-10-31T09:00:00Z","text":"see the contract","content_warnings":[]}]}`

const cryptoSymbolBody = `{"symbol":"FLAPJAX","assets":[` + cryptoEntryJSON + `],"posts":[` + cryptoPostJSON + `],"posts_total":1}`

func cryptoPage(hasMore bool, ids ...string) projectReply {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = `{"id":"` + id + `","title":"t"}`
	}
	more := "false"
	if hasMore {
		more = "true"
	}
	return projectReply{200, `{"items":[` + strings.Join(rows, ",") + `],"total":3,"has_more":` + more + `}`, nil}
}

func TestGetCryptoSection(t *testing.T) {
	c, reqs := projectServer(t, projectReply{200, cryptoSectionBody, nil})
	out, err := c.GetCryptoSection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodGet || r.path != "/crypto" || len(r.query) != 0 {
		t.Errorf("sent %s %s?%v", r.method, r.path, r.query)
	}
	// Sent although the route needs none: an anonymous caller is cut off
	// 2,000 items into a list.
	if r.header.Get("Authorization") != "Bearer t" {
		t.Errorf("Authorization = %q", r.header.Get("Authorization"))
	}
	if len(out.Discussed) != 1 || out.Discussed[0].Address != sectionFlapjax || out.Discussed[0].Authors != 4 {
		t.Errorf("discussed %+v", out.Discussed)
	}
	if out.Discussed[0].ChainSlug == nil || *out.Discussed[0].ChainSlug != "bsc" {
		t.Errorf("chain_slug %v", out.Discussed[0].ChainSlug)
	}
	if out.New == nil || len(out.New) != 0 {
		t.Errorf("new %#v, want an empty, non-nil slice", out.New)
	}
	if !reflect.DeepEqual(out.Chains, []CryptoChain{{Chain: "eip155:56", Slug: "bsc", Name: "BNB Chain", Posts: 867}}) {
		t.Errorf("chains %+v", out.Chains)
	}
	if out.Rule != (CryptoRule{MinAuthors: 3, MinPosts: 5, Days: 7}) || !strings.HasPrefix(out.Notice, "Inclusion") {
		t.Errorf("rule %+v, notice %q", out.Rule, out.Notice)
	}
}

func TestGetCryptoSectionDiscussedSwitchedOff(t *testing.T) {
	body := strings.Replace(cryptoSectionBody, `"discussed":[`+cryptoEntryJSON+`]`, `"discussed":null`, 1)
	c, _ := projectServer(t, projectReply{200, body, nil})
	out, err := c.GetCryptoSection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Discussed != nil {
		t.Errorf("discussed %#v, want nil while switched off", out.Discussed)
	}
}

func TestListCryptoPostsQuery(t *testing.T) {
	for _, tc := range []struct {
		opts *ListCryptoPostsOptions
		want url.Values
	}{
		{nil, url.Values{}},
		{&ListCryptoPostsOptions{}, url.Values{}},
		{&ListCryptoPostsOptions{
			Sort: CryptoSortTop, Chain: "eip155:56", Asset: " bsc:" + sectionFlapjax + " ", PostType: "finding",
			Limit: 50, Offset: 100,
		}, url.Values{
			"sort": {"top"}, "chain": {"eip155:56"}, "asset": {"bsc:" + sectionFlapjax}, "post_type": {"finding"},
			"limit": {"50"}, "offset": {"100"},
		}},
	} {
		c, reqs := projectServer(t, cryptoPage(false, "a"))
		out, err := c.ListCryptoPosts(context.Background(), tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		r := (*reqs)[0]
		if r.method != http.MethodGet || r.path != "/crypto/posts" || !reflect.DeepEqual(r.query, tc.want) {
			t.Errorf("ListCryptoPosts(%+v) sent %s %s?%v, want query %v", tc.opts, r.method, r.path, r.query, tc.want)
		}
		if len(out.Items) != 1 || out.MoreAfter(20) {
			t.Errorf("items %+v, has_more %v", out.Items, out.HasMore)
		}
	}
}

func TestListCryptoPostsRefusesABlankFilterBeforeTheRequest(t *testing.T) {
	for _, opts := range []ListCryptoPostsOptions{
		{Chain: "   "},
		{PostType: " "},
		{Asset: "bsc"},
	} {
		c, reqs := projectServer(t, cryptoPage(false))
		if _, err := c.ListCryptoPosts(context.Background(), &opts); err == nil {
			t.Errorf("ListCryptoPosts(%+v) was accepted", opts)
		}
		if err := c.IterCryptoPosts(context.Background(), &opts, func(Post) bool { return true }); err == nil {
			t.Errorf("IterCryptoPosts(%+v) was accepted", opts)
		}
		if len(*reqs) != 0 {
			t.Errorf("%+v: %d request(s) reached the server", opts, len(*reqs))
		}
	}
}

func TestIterCryptoPosts(t *testing.T) {
	ctx := context.Background()

	t.Run("pages until the server says there is no more, keeping the filters", func(t *testing.T) {
		c, reqs := projectServer(t, cryptoPage(true, "a", "b"), cryptoPage(false, "c"))
		var got []string
		opts := &ListCryptoPostsOptions{Sort: CryptoSortActive, Chain: "bsc", Limit: 2}
		if err := c.IterCryptoPosts(ctx, opts, func(p Post) bool { got = append(got, p.ID); return true }); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != "a,b,c" || len(*reqs) != 2 {
			t.Fatalf("walked %v over %d request(s)", got, len(*reqs))
		}
		if q := (*reqs)[0].query; q.Get("offset") != "" || q.Get("limit") != "2" || q.Get("chain") != "bsc" || q.Get("sort") != "active" {
			t.Errorf("first page query %v", q)
		}
		if q := (*reqs)[1].query; q.Get("offset") != "2" || q.Get("chain") != "bsc" || q.Get("sort") != "active" {
			t.Errorf("second page query %v", q)
		}
		if opts.Offset != 0 {
			t.Errorf("the caller's options were changed: Offset %d", opts.Offset)
		}
	})

	t.Run("a short page with more to come is not the end", func(t *testing.T) {
		c, reqs := projectServer(t, cryptoPage(true, "a"), cryptoPage(false, "b"))
		n := 0
		if err := c.IterCryptoPosts(ctx, nil, func(Post) bool { n++; return true }); err != nil {
			t.Fatal(err)
		}
		if n != 2 || len(*reqs) != 2 || (*reqs)[0].query.Get("limit") != "20" {
			t.Errorf("yielded %d over %d request(s), first query %v", n, len(*reqs), (*reqs)[0].query)
		}
	})

	t.Run("stops when fn returns false", func(t *testing.T) {
		c, reqs := projectServer(t, cryptoPage(true, "a", "b"))
		n := 0
		if err := c.IterCryptoPosts(ctx, nil, func(Post) bool { n++; return false }); err != nil {
			t.Fatal(err)
		}
		if n != 1 || len(*reqs) != 1 {
			t.Errorf("yielded %d over %d request(s)", n, len(*reqs))
		}
	})

	t.Run("reports an error", func(t *testing.T) {
		c, _ := projectServer(t, projectReply{400, `{"detail":{"message":"unknown chain 'x'","code":"INVALID_INPUT"}}`, nil})
		err := c.IterCryptoPosts(ctx, &ListCryptoPostsOptions{Chain: "x"}, func(Post) bool { return true })
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("err = %v, want a *ValidationError", err)
		}
	})
}

func TestListAndIterCryptoPaidPosts(t *testing.T) {
	ctx := context.Background()
	c, reqs := projectServer(t, cryptoPage(false, "a"))
	if _, err := c.ListCryptoPaidPosts(ctx, &CryptoPageOptions{Limit: 5, Offset: 10}); err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.path != "/crypto/paid" || !reflect.DeepEqual(r.query, url.Values{"limit": {"5"}, "offset": {"10"}}) {
		t.Errorf("sent %s?%v", r.path, r.query)
	}

	c, reqs = projectServer(t, cryptoPage(true, "a", "b"), cryptoPage(false, "c"))
	var got []string
	if err := c.IterCryptoPaidPosts(ctx, &CryptoPageOptions{Limit: 2}, func(p Post) bool { got = append(got, p.ID); return true }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b,c" || len(*reqs) != 2 || (*reqs)[1].query.Get("offset") != "2" {
		t.Errorf("walked %v over %d request(s)", got, len(*reqs))
	}
}

func TestGetCryptoAsset(t *testing.T) {
	ctx := context.Background()
	c, reqs := projectServer(t, projectReply{200, cryptoAssetBody, nil})
	out, err := c.GetCryptoAsset(ctx, "bsc", sectionFlapjax, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != http.MethodGet || r.raw != "/crypto/assets/bsc/"+sectionFlapjax || len(r.query) != 0 {
		t.Errorf("sent %s %s?%v", r.method, r.raw, r.query)
	}
	if out.Verified || out.Kind != "asset" || out.PostsTotal != 1 || len(out.Posts) != 1 {
		t.Errorf("page %+v", out)
	}
	if out.Concentration == nil || out.Concentration.Author != "flapjax-team" || out.Concentration.Share != 0.75 {
		t.Errorf("concentration %+v", out.Concentration)
	}
	if out.FirstMentioned == nil || out.FirstMentioned.Year() != 2026 || out.Affiliations["team"] != 1 {
		t.Errorf("first_mentioned %v, affiliations %v", out.FirstMentioned, out.Affiliations)
	}
	if len(out.Comments) != 1 || out.Comments[0].Text != "see the contract" || out.Comments[0].ContentWarnings == nil {
		t.Errorf("comments %+v", out.Comments)
	}

	c, reqs = projectServer(t, projectReply{200, cryptoAssetBody, nil})
	if _, err := c.GetCryptoAsset(ctx, "eip155:56", sectionFlapjax, &CryptoPageOptions{Limit: 5, Offset: 5}); err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.path != "/crypto/assets/eip155:56/"+sectionFlapjax || !reflect.DeepEqual(r.query, url.Values{"limit": {"5"}, "offset": {"5"}}) {
		t.Errorf("sent %s?%v", r.path, r.query)
	}
}

func TestGetCryptoSymbol(t *testing.T) {
	ctx := context.Background()
	c, reqs := projectServer(t, projectReply{200, cryptoSymbolBody, nil})
	out, err := c.GetCryptoSymbol(ctx, "$flapjax")
	if err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != http.MethodGet || r.path != "/crypto/symbols/$flapjax" {
		t.Errorf("sent %s %s", r.method, r.path)
	}
	if out.Symbol != "FLAPJAX" || len(out.Assets) != 1 || out.Assets[0].Symbol == nil || *out.Assets[0].Symbol != "FLAPJAX" || out.PostsTotal != 1 {
		t.Errorf("page %+v", out)
	}

	c, reqs = projectServer(t, projectReply{200, cryptoSymbolBody, nil})
	if _, err := c.GetCryptoSymbol(ctx, "A B#C"); err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.raw != "/crypto/symbols/A%20B%23C" {
		t.Errorf("sent %s, want the symbol as one escaped segment", r.raw)
	}
}

func TestCryptoPagesNotFound(t *testing.T) {
	ctx := context.Background()
	reply := projectReply{404, `{"detail":{"message":"No public post on The Colony names $NOPE.","code":"NOT_FOUND"}}`, nil}
	var nf *NotFoundError

	c, _ := projectServer(t, reply)
	if _, err := c.GetCryptoSymbol(ctx, "NOPE"); !errors.As(err, &nf) {
		t.Errorf("GetCryptoSymbol err = %v, want a *NotFoundError", err)
	}
	c, _ = projectServer(t, reply)
	if _, err := c.GetCryptoAsset(ctx, "bsc", sectionFlapjax, nil); !errors.As(err, &nf) {
		t.Errorf("GetCryptoAsset err = %v, want a *NotFoundError", err)
	}
}
