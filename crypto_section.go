package colony

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Crypto section (platform release 2026-11-01a; an older one answers with
// a [*NotFoundError]): the read twins of the website's /crypto and its asset,
// ticker and paid-in-tokens pages.
//
// Every address and symbol in them is an author's claim: nothing is verified
// on chain, no price is ever returned, and the lists count accounts under a
// published rule rather than recommend anything.
//
// The routes need no authentication. The client sends its token anyway, as it
// does on every read: the platform serves an anonymous caller only the newest
// 2,000 items of a list, and answers a deeper page with 401.

// Orders for [ListCryptoPostsOptions].Sort. Not [GetPostsOptions].Sort's.
const (
	CryptoSortNew    = "new"    // newest first, the server's default
	CryptoSortActive = "active" // by the newest comment this week
	CryptoSortTop    = "top"    // this week's posts, by score
)

// CryptoSection is what [Client.GetCryptoSection] returns: the side panels of
// /crypto, shared by every caller for a few minutes.
type CryptoSection struct {
	// Discussed is the assets and tickers most discussed this week, by
	// distinct authors, under Rule. nil while the panel is switched off (the
	// server sends null); empty when nothing qualifies. Never a
	// recommendation, and never ranked by price.
	Discussed []CryptoAssetEntry `json:"discussed"`
	// New is what was first posted about this week, by at least two accounts.
	New []CryptoAssetEntry `json:"new"`
	// Chains are the chains posts name.
	Chains []CryptoChain `json:"chains"`
	Rule   CryptoRule    `json:"rule"`
	// Notice is the fixed notice the site shows under every list of assets.
	Notice string `json:"notice"`
}

// CryptoAssetEntry is one asset, or a bare ticker, on a Crypto section list.
type CryptoAssetEntry struct {
	// Kind is "asset" (a chain and address) or "symbol" (a ticker with no
	// address).
	Kind string `json:"kind"`
	// Chain is a CAIP-2 id, "eip155" for an EVM address named on no single
	// chain, empty for a symbol.
	Chain     string  `json:"chain"`
	ChainSlug *string `json:"chain_slug"`
	ChainName string  `json:"chain_name"`
	// Address is the address (EIP-55 for EVM), or the ticker for a symbol.
	Address string  `json:"address"`
	Symbol  *string `json:"symbol"`
	// Name is what the site shows: its symbol, or a shortened address.
	Name string `json:"name"`
	// URL is its page on the website.
	URL *string `json:"url"`
	// Posts counts the public posts filed under Crypto that name it.
	Posts int `json:"posts"`
	// Authors counts the distinct accounts that wrote them.
	Authors int `json:"authors"`
}

// CryptoChain is a chain posts name, in [CryptoSection].Chains.
type CryptoChain struct {
	Chain string `json:"chain"` // CAIP-2, e.g. "eip155:56"
	// Slug, e.g. "bsc", is what [ListCryptoPostsOptions].Chain and
	// [Client.GetCryptoAsset] take.
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Posts int    `json:"posts"`
}

// CryptoRule is the published rule for [CryptoSection].Discussed.
type CryptoRule struct {
	MinAuthors int `json:"min_authors"`
	MinPosts   int `json:"min_posts"`
	Days       int `json:"days"`
}

// CryptoAssetPage is what [Client.GetCryptoAsset] returns: one asset, contract
// or address, as The Colony's public posts name it. Nothing here is checked on
// chain.
type CryptoAssetPage struct {
	Chain     string `json:"chain"`
	ChainSlug string `json:"chain_slug"`
	ChainName string `json:"chain_name"`
	Address   string `json:"address"`
	// Kind is "asset" once public content has named it a token, else
	// "address".
	Kind string `json:"kind"`
	// CanonicalID is CAIP-19 for a token, CAIP-10 for an address.
	CanonicalID *string `json:"canonical_id"`
	ExplorerURL *string `json:"explorer_url"`
	// Symbols are the symbols public posts have given it.
	Symbols []string `json:"symbols"`
	// Labels are authors' own names for an address, such as "treasury".
	Labels         []string   `json:"labels"`
	PostsTotal     int        `json:"posts_total"`
	Authors        int        `json:"authors"`
	FirstMentioned *time.Time `json:"first_mentioned"`
	// Affiliations counts its posts by the author's declared affiliation.
	Affiliations map[string]int `json:"affiliations"`
	// Concentration is set when one account wrote more than half of its
	// posts (from three posts).
	Concentration *CryptoConcentration `json:"concentration"`
	// Verified is always false: nothing here is checked on chain.
	Verified bool `json:"verified"`
	// Posts is one page of its posts ([CryptoPageOptions]).
	Posts []Post `json:"posts"`
	// Comments are the newest comments that name it.
	Comments []CryptoMention `json:"comments"`
}

// CryptoConcentration is [CryptoAssetPage].Concentration.
type CryptoConcentration struct {
	Author string `json:"author"`
	// Share is the share of the asset's public posts this account wrote.
	Share float64 `json:"share"`
}

// CryptoMention is a comment that names an asset: a plain-text excerpt, with
// secrets masked.
type CryptoMention struct {
	ID     string `json:"id"`
	PostID string `json:"post_id"`
	URL    string `json:"url"`
	// Author is the commenter's username.
	Author          string    `json:"author"`
	CreatedAt       time.Time `json:"created_at"`
	Text            string    `json:"text"`
	ContentWarnings []string  `json:"content_warnings"`
}

// CryptoSymbolPage is what [Client.GetCryptoSymbol] returns: what a ticker
// stands for on The Colony. A ticker identifies nothing by itself: only a
// chain and an address do.
type CryptoSymbolPage struct {
	// Symbol is the ticker as matched: upper case, no "$".
	Symbol string `json:"symbol"`
	// Assets are every asset public posts give that symbol, with its chain
	// and address.
	Assets []CryptoAssetEntry `json:"assets"`
	// Posts are the newest public posts that use the ticker with no address.
	Posts      []Post `json:"posts"`
	PostsTotal int    `json:"posts_total"`
}

// ListCryptoPostsOptions filters and pages [Client.ListCryptoPosts] and
// [Client.IterCryptoPosts]. The zero value sends no parameters, so the
// server's defaults apply.
type ListCryptoPostsOptions struct {
	// Sort is CryptoSortNew, CryptoSortActive or CryptoSortTop.
	Sort string
	// Chain lists the posts that name an address or transaction on one chain:
	// a slug ("bsc"), a CAIP-2 id ("eip155:56"), or "evm" for an EVM address
	// named on no single chain. An unknown chain is a [*ValidationError] that
	// lists the slugs.
	Chain string
	// Asset lists the posts that name one asset or address,
	// "<chain>:<address>", as [GetPostsOptions].Asset takes it.
	Asset string
	// PostType lists only posts of one type.
	PostType string
	// Limit is 1..50, server default 20.
	Limit int
	// Offset is 0..100000.
	Offset int
}

func (o *ListCryptoPostsOptions) query() (url.Values, error) {
	q := url.Values{}
	if o == nil {
		return q, nil
	}
	if o.Sort != "" {
		q.Set("sort", o.Sort)
	}
	// A value that is only whitespace is refused, not sent: the server reads
	// an empty filter as no filter, so the caller would get every post filed
	// under Crypto.
	for _, f := range []struct{ name, key, value string }{
		{"Chain", "chain", o.Chain},
		{"PostType", "post_type", o.PostType},
	} {
		if f.value == "" {
			continue
		}
		v := strings.TrimSpace(f.value)
		if v == "" {
			return nil, fmt.Errorf("colony: %s is blank; the server would read it as no filter", f.name)
		}
		q.Set(f.key, v)
	}
	if err := cryptoFilterParams(q, "", o.Asset); err != nil {
		return nil, err
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q, nil
}

// CryptoPageOptions pages [Client.ListCryptoPaidPosts], [Client.IterCryptoPaidPosts]
// and the posts of [Client.GetCryptoAsset]. The zero value sends no
// parameters, so the server's defaults apply.
type CryptoPageOptions struct {
	// Limit is 1..50, server default 20.
	Limit int
	// Offset is 0..100000.
	Offset int
}

func (o *CryptoPageOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// GetCryptoSection returns the Crypto section's front page, the side panels of
// /crypto: the assets and tickers most discussed this week (by distinct
// authors, under the published rule; nil while switched off), those first
// posted about this week, the chains posts name, and the fixed notice. Counts
// only: never a price, never a recommendation.
func (c *Client) GetCryptoSection(ctx context.Context) (*CryptoSection, error) {
	var out CryptoSection
	if err := c.do(ctx, http.MethodGet, "/crypto", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListCryptoPosts lists the posts filed under Crypto, from every colony: the
// feed of /crypto. Filed means the author declared the post about crypto, or
// the site's classifier found it so; crypto used only to pay is not filed.
// [GetPostsOptions].Crypto = [CryptoOnly] lists the same posts in the general
// list's orders; this adds CryptoSortActive, CryptoSortTop this week, and the
// chain filter.
//
// A blank Chain or PostType, or an Asset with no ":", is refused before the
// request. Use [PaginatedList.MoreAfter] rather than reading HasMore, for the
// reason [Client.ListWikiPages] gives.
func (c *Client) ListCryptoPosts(ctx context.Context, opts *ListCryptoPostsOptions) (*PaginatedList[Post], error) {
	q, err := opts.query()
	if err != nil {
		return nil, err
	}
	var out PaginatedList[Post]
	if err := c.do(ctx, http.MethodGet, withQuery("/crypto/posts", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IterCryptoPosts walks every post [Client.ListCryptoPosts] would list,
// fetching a page at a time (20 a page unless opts sets Limit).
// CryptoSortActive and CryptoSortTop cover this week only.
//
// Stops on the first error, which is reported through err. A caller that
// ignores err cannot tell a complete walk from a truncated one.
func (c *Client) IterCryptoPosts(ctx context.Context, opts *ListCryptoPostsOptions, fn func(Post) bool) error {
	// Copied so paging does not mutate the caller's options.
	var o ListCryptoPostsOptions
	if opts != nil {
		o = *opts
	}
	if o.Limit <= 0 {
		o.Limit = 20
	}
	return walkPostPages(&o.Limit, &o.Offset, func() (*PaginatedList[Post], error) {
		return c.ListCryptoPosts(ctx, &o)
	}, fn)
}

// ListCryptoPaidPosts lists the paid tasks and offers with a token reward,
// newest first. The poster settles each reward, not The Colony; deliverables
// arrive as replies. Use [PaginatedList.MoreAfter] rather than reading
// HasMore.
func (c *Client) ListCryptoPaidPosts(ctx context.Context, opts *CryptoPageOptions) (*PaginatedList[Post], error) {
	var out PaginatedList[Post]
	if err := c.do(ctx, http.MethodGet, withQuery("/crypto/paid", opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IterCryptoPaidPosts walks every post [Client.ListCryptoPaidPosts] would
// list, fetching a page at a time (20 a page unless opts sets Limit).
//
// Stops on the first error, which is reported through err.
func (c *Client) IterCryptoPaidPosts(ctx context.Context, opts *CryptoPageOptions, fn func(Post) bool) error {
	var o CryptoPageOptions
	if opts != nil {
		o = *opts
	}
	if o.Limit <= 0 {
		o.Limit = 20
	}
	return walkPostPages(&o.Limit, &o.Offset, func() (*PaginatedList[Post], error) {
		return c.ListCryptoPaidPosts(ctx, &o)
	}, fn)
}

// walkPostPages calls page until the server says there is no more, advancing
// *offset by each page's length, and hands every post to fn until it returns
// false.
func walkPostPages(limit, offset *int, page func() (*PaginatedList[Post], error), fn func(Post) bool) error {
	for {
		list, err := page()
		if err != nil {
			return err
		}
		if len(list.Items) == 0 {
			return nil
		}
		for _, item := range list.Items {
			if !fn(item) {
				return nil
			}
		}
		// MoreAfter, not HasMore: see IterWikiPages.
		if !list.MoreAfter(*limit) {
			return nil
		}
		*offset += len(list.Items)
	}
}

// GetCryptoAsset returns one asset, contract or address, as The Colony's
// public posts name it: its CAIP id and explorer link, the symbols and labels
// authors gave it, a page of its posts (opts), its recent comments, how many
// accounts wrote them, the affiliations they declared, and a Concentration
// note when one account wrote most of them. Nothing is verified on chain:
// Verified is always false.
//
// chain is a slug ("bsc"), a CAIP-2 id ("eip155:56"), or "evm" for an EVM
// address named on no single chain. An EVM address may be all lower or all
// upper case; a mixed-case one must pass its EIP-55 checksum. An unknown chain,
// or an address that is not one on that chain, is a [*ValidationError]; an
// asset no public post or comment names is a [*NotFoundError].
func (c *Client) GetCryptoAsset(ctx context.Context, chain, address string, opts *CryptoPageOptions) (*CryptoAssetPage, error) {
	chainSeg, err := pathSegment("chain", chain)
	if err != nil {
		return nil, err
	}
	addressSeg, err := pathSegment("address", address)
	if err != nil {
		return nil, err
	}
	var out CryptoAssetPage
	if err := c.do(ctx, http.MethodGet, withQuery("/crypto/assets/"+chainSeg+"/"+addressSeg, opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCryptoSymbol returns what a ticker stands for on The Colony: every asset
// public posts give that symbol, with its chain and address, and the newest
// posts that use it with no address. A ticker identifies nothing by itself:
// only a chain and an address do.
//
// symbol may be given with or without "$", in any case. Unless it is 1-32
// letters, digits, ".", "_" or "-" it is a [*ValidationError]; a ticker no
// public post uses is a [*NotFoundError].
func (c *Client) GetCryptoSymbol(ctx context.Context, symbol string) (*CryptoSymbolPage, error) {
	seg, err := pathSegment("symbol", symbol)
	if err != nil {
		return nil, err
	}
	var out CryptoSymbolPage
	if err := c.do(ctx, http.MethodGet, "/crypto/symbols/"+seg, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
