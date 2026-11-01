package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Crypto references on posts (platform 2026-11): the CryptoAsset /
// CryptoAddress / CryptoTx builders refuse a shape the chain cannot have
// before any request; CreatePostOptions.Crypto sends the declaration;
// SetPostCrypto sends only what was set to PATCH /posts/{id}/crypto;
// GetPosts and IterPosts take Crypto and Asset; NewTokenReward and
// NewNativeReward build a paid listing's reward; Post.Crypto decodes.

const (
	cryptoPostID   = "9cb7ff7d-51a0-46c6-acd2-d731164c792a"
	cryptoColonyID = "2e549d01-99f2-459f-8924-48b2690b2170"
	cryptoFlapjax  = "0x90c8889f428F9Ebb77BB8f15CAD3a50a9aC680df"
	cryptoTreasury = "0x7C34E9e21eE28A49Ff0b84B61774119E6633359f"
	cryptoWSOL     = "So11111111111111111111111111111111111111112"
	cryptoPost     = `{"id":"` + cryptoPostID + `","title":"t","body":"b",` +
		`"crypto":{"filed":true,"source":"author","author_says":true,"affiliation":"team",` +
		`"refs":[{"kind":"asset","chain":"eip155:56","chain_slug":"bsc","address":"` + cryptoFlapjax + `",` +
		`"symbol":"FLAPJAX","source":"author","verified":false,"explorer_url":"https://bscscan.com/token/` + cryptoFlapjax + `","warnings":[]}]}}`
)

var cryptoTx = "0x" + strings.Repeat("ab", 32)

func TestCryptoBuildersNameByChainPlusAddress(t *testing.T) {
	asset, err := CryptoAsset("bsc", cryptoFlapjax, "$flapjax")
	if err != nil || asset != (CryptoRefInput{Kind: "asset", Chain: "bsc", Address: cryptoFlapjax, Symbol: "FLAPJAX"}) {
		t.Errorf("CryptoAsset = %+v, %v", asset, err)
	}
	addr, err := CryptoAddress("eip155:56", cryptoTreasury, "  the   treasury ")
	if err != nil || addr != (CryptoRefInput{Kind: "address", Chain: "bsc", Address: cryptoTreasury, Label: "the treasury"}) {
		t.Errorf("CryptoAddress = %+v, %v", addr, err)
	}
	tx, err := CryptoTx("bsc", cryptoTx)
	if err != nil || tx != (CryptoRefInput{Kind: "tx", Chain: "bsc", ID: cryptoTx}) {
		t.Errorf("CryptoTx = %+v, %v", tx, err)
	}
	if mint, err := CryptoAsset("solana", cryptoWSOL, ""); err != nil || mint.Address != cryptoWSOL || mint.Symbol != "" {
		t.Errorf("solana mint = %+v, %v", mint, err)
	}
}

func TestCryptoBuildersRefuseAShapeTheChainCannotHave(t *testing.T) {
	for _, tc := range []struct {
		name, needle string
		build        func() (CryptoRefInput, error)
	}{
		{"chain", "unknown chain", func() (CryptoRefInput, error) { return CryptoAsset("dogechain", cryptoFlapjax, "") }},
		{"short address", "not a valid bsc address", func() (CryptoRefInput, error) { return CryptoAsset("bsc", "0x1234", "") }},
		{"address as tx", "not a valid bsc transaction id", func() (CryptoRefInput, error) { return CryptoTx("bsc", cryptoFlapjax) }},
		{"solana", "not a valid solana address", func() (CryptoRefInput, error) { return CryptoAddress("solana", "not-base58!", "") }},
		{"bitcoin asset", "no token contracts", func() (CryptoRefInput, error) { return CryptoAsset("bitcoin", "bc1qxyz", "") }},
		{"symbol", "symbol", func() (CryptoRefInput, error) { return CryptoAsset("bsc", cryptoFlapjax, "no spaces") }},
		{"label", "label", func() (CryptoRefInput, error) { return CryptoAddress("bsc", cryptoTreasury, "   ") }},
	} {
		if _, err := tc.build(); err == nil || !strings.Contains(err.Error(), tc.needle) {
			t.Errorf("%s: err = %v, want one naming %q", tc.name, err, tc.needle)
		}
	}
}

// Base58 decoding is quadratic in the length, so an over-long value is refused
// unread; the longest real one is an 88-character signature.
func TestCryptoBuildersRefuseAnOverlongValueUnread(t *testing.T) {
	long := strings.Repeat("z", 200_000)
	if _, err := CryptoAddress("solana", long, ""); err == nil {
		t.Error("an over-long address was accepted")
	}
	if _, err := CryptoTx("solana", long); err == nil {
		t.Error("an over-long signature was accepted")
	}
	if base58Len(strings.Repeat("z", base58Max)) < 0 || base58Len(strings.Repeat("z", base58Max+1)) != -1 {
		t.Error("the bound is not the longest real value")
	}
}

func TestTokenRewards(t *testing.T) {
	r, err := NewTokenReward("500000", "bsc", cryptoFlapjax, "flapjax")
	b, _ := json.Marshal(r)
	if err != nil || string(b) != `{"amount":"500000","asset":{"chain":"bsc","address":"`+cryptoFlapjax+`","symbol":"FLAPJAX"}}` {
		t.Errorf("NewTokenReward = %s, %v", b, err)
	}
	n, err := NewNativeReward("0.25", "bsc")
	b, _ = json.Marshal(n)
	if err != nil || string(b) != `{"amount":"0.25","asset":{"chain":"bsc","native":true}}` {
		t.Errorf("NewNativeReward = %s, %v", b, err)
	}
	for _, bad := range []string{"0", "0.000", "1e5", "-3", "", "01"} {
		if _, err := NewNativeReward(bad, "bsc"); err == nil || !strings.Contains(err.Error(), "positive") {
			t.Errorf("amount %q: err = %v", bad, err)
		}
	}
}

func TestCreatePostSendsTheDeclarationOnlyWhenSet(t *testing.T) {
	ctx := context.Background()
	asset, _ := CryptoAsset("bsc", cryptoFlapjax, "FLAPJAX")
	decl := &CryptoDeclaration{Refs: []CryptoRefInput{asset}, Affiliation: CryptoAffiliationTeam}

	c, rec := wikiServer(t, http.StatusOK, cryptoPost)
	post, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: cryptoColonyID, Crypto: decl})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatal(err)
	}
	want := `{"refs":[{"kind":"asset","chain":"bsc","address":"` + cryptoFlapjax + `","symbol":"FLAPJAX"}],"affiliation":"team"}`
	if string(sent["crypto"]) != want {
		t.Errorf("crypto sent as %s", sent["crypto"])
	}
	if post.Crypto == nil || !post.Crypto.Filed || len(post.Crypto.Refs) != 1 || *post.Crypto.Refs[0].ChainSlug != "bsc" {
		t.Errorf("Post.Crypto decoded as %+v", post.Crypto)
	}

	c, rec = wikiServer(t, http.StatusOK, cryptoPost)
	if _, err := c.CreatePost(ctx, "t", "b", &CreatePostOptions{Colony: cryptoColonyID}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rec.body), "crypto") {
		t.Errorf("unset: sent %s", rec.body)
	}
}

func TestCreatePostRefusesABadDeclarationBeforeTheRequest(t *testing.T) {
	asset, _ := CryptoAsset("bsc", cryptoFlapjax, "")
	no := false
	for name, decl := range map[string]*CryptoDeclaration{
		"contradiction": {Refs: []CryptoRefInput{asset}, AboutCrypto: &no},
		"affiliation":   {Affiliation: "whale"},
		"too many":      {Refs: make([]CryptoRefInput, MaxCryptoRefs+1)},
	} {
		c, rec := wikiServer(t, http.StatusOK, cryptoPost)
		if _, err := c.CreatePost(context.Background(), "t", "b", &CreatePostOptions{Colony: cryptoColonyID, Crypto: decl}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if rec.method != "" {
			t.Errorf("%s: refused, but the request was still sent", name)
		}
	}
}

func TestSetPostCryptoSendsOnlyWhatIsSet(t *testing.T) {
	yes, clear, holder := true, "", CryptoAffiliationHolder
	for _, tc := range []struct {
		name   string
		change SetPostCryptoRequest
		want   string
	}{
		{"remove refs", SetPostCryptoRequest{Refs: []CryptoRefInput{}}, `{"refs":[]}`},
		{"clear affiliation", SetPostCryptoRequest{Affiliation: &clear}, `{"affiliation":null}`},
		{"about and affiliation", SetPostCryptoRequest{AboutCrypto: &yes, Affiliation: &holder}, `{"about_crypto":true,"affiliation":"holder"}`},
	} {
		c, rec := wikiServer(t, http.StatusOK, cryptoPost)
		if _, err := c.SetPostCrypto(context.Background(), cryptoPostID, tc.change); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.method != http.MethodPatch || rec.path != "/posts/"+cryptoPostID+"/crypto" {
			t.Errorf("%s: sent %s %s", tc.name, rec.method, rec.path)
		}
		if string(rec.body) != tc.want {
			t.Errorf("%s: body %s, want %s", tc.name, rec.body, tc.want)
		}
	}
}

func TestSetPostCryptoRefusesBeforeTheRequest(t *testing.T) {
	whale := "whale"
	for name, change := range map[string]SetPostCryptoRequest{
		"nothing":     {},
		"affiliation": {Affiliation: &whale},
		"too many":    {Refs: make([]CryptoRefInput, MaxCryptoRefs+1)},
	} {
		c, rec := wikiServer(t, http.StatusOK, cryptoPost)
		if _, err := c.SetPostCrypto(context.Background(), cryptoPostID, change); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if rec.method != "" {
			t.Errorf("%s: refused, but the request was still sent", name)
		}
	}
	c, rec := wikiServer(t, http.StatusOK, cryptoPost)
	if _, err := c.SetPostCrypto(context.Background(), "..", SetPostCryptoRequest{Refs: []CryptoRefInput{}}); err == nil || rec.method != "" {
		t.Errorf("a dot segment: err %v, sent %q", err, rec.method)
	}
}

func TestGetPostsCryptoFilters(t *testing.T) {
	ctx := context.Background()
	c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0}`)
	if _, err := c.GetPosts(ctx, &GetPostsOptions{Crypto: CryptoOnly, Asset: "bsc:" + cryptoFlapjax}); err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(rec.query)
	if q.Get("crypto") != "only" || q.Get("asset") != "bsc:"+cryptoFlapjax {
		t.Errorf("query %s", rec.query)
	}
	for name, opts := range map[string]*GetPostsOptions{
		"crypto": {Crypto: "yes"},
		"asset":  {Asset: cryptoFlapjax},
	} {
		c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0}`)
		if _, err := c.GetPosts(ctx, opts); err == nil || rec.method != "" {
			t.Errorf("%s: err %v, sent %q", name, err, rec.method)
		}
	}
}

func TestIterPostsForwardsTheCryptoFilters(t *testing.T) {
	c, rec := wikiServer(t, http.StatusOK, `{"items":[],"total":0}`)
	for r := range c.IterPosts(context.Background(), &IterPostsOptions{Crypto: CryptoExclude, Asset: "solana:" + cryptoWSOL}) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	q, _ := url.ParseQuery(rec.query)
	if q.Get("crypto") != "exclude" || q.Get("asset") != "solana:"+cryptoWSOL {
		t.Errorf("query %s", rec.query)
	}
}
