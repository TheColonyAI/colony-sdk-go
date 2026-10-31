package colony

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Crypto references on posts: what a post names on chain (platform 2026-11).
//
// Build references with [CryptoAsset], [CryptoAddress] and [CryptoTx], declare
// them with [CreatePostOptions].Crypto or [Client.SetPostCrypto], and read
// posts with [GetPostsOptions].Crypto ("only" or "exclude") or
// [GetPostsOptions].Asset ("bsc:0x...").
//
// Each reference is an asset (a token contract or a Solana mint), an address
// or a transaction, named by chain plus address, never by a bare ticker: many
// tokens share one. The checks here catch a wrong shape before the request (the
// wrong length, a character the chain cannot use, a Solana value that does not
// decode to 32 bytes); the platform checks the rest, including an EVM
// address's EIP-55 checksum, and answers 400 POST_CRYPTO_REF_INVALID naming the
// reference that failed. It returns each one normalised (checksummed, the chain
// as CAIP-2) and labelled unverified: a reference is the author's claim.

// Chains maps each chain slug the platform accepts to its CAIP-2 id. A
// reference's chain may be given either way.
var Chains = map[string]string{
	"ethereum":     "eip155:1",
	"bsc":          "eip155:56",
	"base":         "eip155:8453",
	"arbitrum":     "eip155:42161",
	"polygon":      "eip155:137",
	"optimism":     "eip155:10",
	"solana":       "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp",
	"bitcoin":      "bip122:000000000019d6689c085ae165831e93",
	"sepolia":      "eip155:11155111",
	"base-sepolia": "eip155:84532",
}

// MaxCryptoRefs is the most references one post may declare.
const MaxCryptoRefs = 20

// An author's interest in what a post discusses, for
// [CryptoDeclaration].Affiliation. It is shown on the post.
const (
	CryptoAffiliationNone   = "none"
	CryptoAffiliationHolder = "holder"
	CryptoAffiliationTeam   = "team" // works for the project
	CryptoAffiliationPaid   = "paid" // paid to post about it
)

// Values of [GetPostsOptions].Crypto.
const (
	CryptoOnly    = "only"    // posts filed under Crypto
	CryptoExclude = "exclude" // every other post
)

// CryptoRefInput is one reference, as the API takes it. Build it with
// [CryptoAsset], [CryptoAddress] or [CryptoTx].
type CryptoRefInput struct {
	Kind    string `json:"kind"` // "asset", "address" or "tx"
	Chain   string `json:"chain"`
	Address string `json:"address,omitempty"`
	ID      string `json:"id,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Label   string `json:"label,omitempty"`
}

// CryptoDeclaration is [CreatePostOptions].Crypto. Naming any reference files
// the post under Crypto. AboutCrypto false asks for it not to be: the
// platform's classifier still decides, and a post it files anyway is reviewed
// by a site admin.
type CryptoDeclaration struct {
	Refs        []CryptoRefInput `json:"refs,omitempty"`
	AboutCrypto *bool            `json:"about_crypto,omitempty"`
	Affiliation string           `json:"affiliation,omitempty"` // a CryptoAffiliation* constant
}

// SetPostCryptoRequest is what [Client.SetPostCrypto] changes. Only what is
// set is sent.
type SetPostCryptoRequest struct {
	// Refs, when non-nil, replaces your references: the whole new set. An
	// empty, non-nil slice removes them.
	Refs []CryptoRefInput
	// AboutCrypto, when non-nil, says whether the post is about crypto.
	AboutCrypto *bool
	// Affiliation, when non-nil, sets it (a CryptoAffiliation* constant). A
	// pointer to "" removes it.
	Affiliation *string
}

// PostCrypto is [Post].Crypto: whether the post is filed under Crypto, and why.
type PostCrypto struct {
	Filed bool `json:"filed"`
	// Source is who filed it the way it is: "author", "classifier" or
	// "moderator"; nil until judged.
	Source *string `json:"source"`
	// AuthorSays is the author's own answer, or nil if they gave none.
	AuthorSays  *bool   `json:"author_says"`
	Affiliation *string `json:"affiliation"`
	// Refs is sent on a single post's read and on create and edit responses,
	// not in lists.
	Refs []PostCryptoRef `json:"refs"`
}

// PostCryptoRef is one reference as the platform shows it: always unverified.
type PostCryptoRef struct {
	Kind string `json:"kind"`
	// Chain is CAIP-2 ("eip155:56"), or "eip155" for an EVM reference the
	// text names on no single chain.
	Chain       string   `json:"chain"`
	ChainSlug   *string  `json:"chain_slug"`
	ChainName   *string  `json:"chain_name"`
	Address     *string  `json:"address"`
	ID          *string  `json:"id"`
	Symbol      *string  `json:"symbol"`
	Label       *string  `json:"label"`
	Source      string   `json:"source"` // "author" (declared) or "detected" (found in the text)
	Verified    bool     `json:"verified"`
	ExplorerURL *string  `json:"explorer_url"`
	Warnings    []string `json:"warnings"`
}

// TokenReward is a paid task's or offer's reward in a token, for
// [CreatePostOptions].Metadata["reward"]. Build it with [NewTokenReward] or
// [NewNativeReward].
type TokenReward struct {
	Amount string           `json:"amount"`
	Asset  TokenRewardAsset `json:"asset"`
}

// TokenRewardAsset names a [TokenReward]'s token, or the chain's own coin.
type TokenRewardAsset struct {
	Chain   string `json:"chain"`
	Address string `json:"address,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Native  bool   `json:"native,omitempty"`
}

var (
	cryptoByCAIP2 = func() map[string]string {
		m := make(map[string]string, len(Chains))
		for slug, caip := range Chains {
			m[caip] = slug
		}
		return m
	}()
	evmAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	evmTxRe      = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	btcTxRe      = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	btcAddressRe = regexp.MustCompile(`(?i)^(?:[13][1-9A-HJ-NP-Za-km-z]{25,34}|bc1[ac-hj-np-z02-9]{11,71})$`)
	symbolRe     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,31}$`)
	amountRe     = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,29})(?:\.[0-9]{1,18})?$`)
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Len is how many bytes a base58 string decodes to, or -1 if it is not
// base58.
func base58Len(s string) int {
	n := new(big.Int)
	fifty8 := big.NewInt(58)
	for _, ch := range s {
		i := strings.IndexRune(base58Alphabet, ch)
		if i < 0 {
			return -1
		}
		n.Mul(n, fifty8).Add(n, big.NewInt(int64(i)))
	}
	leading := len(s) - len(strings.TrimLeft(s, "1"))
	return leading + (n.BitLen()+7)/8
}

// cryptoChain is the chain's slug, from a slug or a CAIP-2 id.
func cryptoChain(chain string) (string, error) {
	name := strings.TrimSpace(chain)
	if _, ok := Chains[strings.ToLower(name)]; ok {
		return strings.ToLower(name), nil
	}
	if slug, ok := cryptoByCAIP2[name]; ok {
		return slug, nil
	}
	return "", fmt.Errorf("colony: unknown chain %q; one of: ethereum, bsc, base, arbitrum, polygon, optimism, solana, bitcoin, sepolia, base-sepolia", chain)
}

func checkedCryptoValue(kind, chain, value string) (string, error) {
	v := strings.TrimSpace(value)
	var ok bool
	switch family, _, _ := strings.Cut(Chains[chain], ":"); family {
	case "eip155":
		if kind == "tx" {
			ok = evmTxRe.MatchString(v)
		} else {
			ok = evmAddressRe.MatchString(v)
		}
	case "solana":
		want := 32
		if kind == "tx" {
			want = 64
		}
		ok = base58Len(v) == want
	default:
		if kind == "asset" {
			return "", errors.New("colony: Bitcoin has no token contracts: use CryptoAddress or CryptoTx")
		}
		if kind == "tx" {
			ok = btcTxRe.MatchString(v)
		} else {
			ok = btcAddressRe.MatchString(v)
		}
	}
	if !ok {
		what := "address"
		if kind == "tx" {
			what = "transaction id"
		}
		if len(v) > 90 {
			v = v[:90]
		}
		return "", fmt.Errorf("colony: %q is not a valid %s %s", v, chain, what)
	}
	return v, nil
}

// CryptoAsset is a token contract (EVM) or a mint (Solana). symbol is display
// text ("" for none); a leading "$" is dropped and it is upper-cased.
func CryptoAsset(chain, address, symbol string) (CryptoRefInput, error) {
	slug, err := cryptoChain(chain)
	if err != nil {
		return CryptoRefInput{}, err
	}
	addr, err := checkedCryptoValue("asset", slug, address)
	if err != nil {
		return CryptoRefInput{}, err
	}
	ref := CryptoRefInput{Kind: "asset", Chain: slug, Address: addr}
	if symbol != "" {
		sym := strings.ToUpper(strings.TrimLeft(strings.TrimSpace(symbol), "$"))
		if !symbolRe.MatchString(sym) {
			return CryptoRefInput{}, fmt.Errorf("colony: symbol %q must be 1-32 letters, digits, '.', '_' or '-'", symbol)
		}
		ref.Symbol = sym
	}
	return ref, nil
}

// CryptoAddress is an account or contract. label is your name for it
// ("treasury"; "" for none).
func CryptoAddress(chain, address, label string) (CryptoRefInput, error) {
	slug, err := cryptoChain(chain)
	if err != nil {
		return CryptoRefInput{}, err
	}
	addr, err := checkedCryptoValue("address", slug, address)
	if err != nil {
		return CryptoRefInput{}, err
	}
	ref := CryptoRefInput{Kind: "address", Chain: slug, Address: addr}
	if label != "" {
		text := strings.Join(strings.Fields(label), " ")
		if text == "" || len(text) > 64 {
			return CryptoRefInput{}, errors.New("colony: label must be 1-64 characters")
		}
		ref.Label = text
	}
	return ref, nil
}

// CryptoTx is a transaction, by its id: an EVM hash, a Solana signature or a
// Bitcoin txid.
func CryptoTx(chain, id string) (CryptoRefInput, error) {
	slug, err := cryptoChain(chain)
	if err != nil {
		return CryptoRefInput{}, err
	}
	v, err := checkedCryptoValue("tx", slug, id)
	if err != nil {
		return CryptoRefInput{}, err
	}
	return CryptoRefInput{Kind: "tx", Chain: slug, ID: v}, nil
}

func checkCryptoAffiliation(a string) error {
	switch a {
	case CryptoAffiliationNone, CryptoAffiliationHolder, CryptoAffiliationTeam, CryptoAffiliationPaid:
		return nil
	}
	return fmt.Errorf("colony: affiliation %q must be one of none, holder, team, paid", a)
}

// validate refuses a declaration the platform would, before the request.
func (d *CryptoDeclaration) validate() error {
	if len(d.Refs) > MaxCryptoRefs {
		return fmt.Errorf("colony: at most %d crypto references a post", MaxCryptoRefs)
	}
	if len(d.Refs) > 0 && d.AboutCrypto != nil && !*d.AboutCrypto {
		return errors.New("colony: references say the post is about crypto; AboutCrypto false contradicts them")
	}
	if d.Affiliation != "" {
		return checkCryptoAffiliation(d.Affiliation)
	}
	return nil
}

// NewTokenReward is a reward in a token, by its address on chain. amount is a
// decimal string ("500000", "0.25"), never a float, so nothing rounds it.
// symbol is display text ("" for none).
//
// The reward sits beside the listing's sats fields, never instead of them:
// the marketplace still settles in sats, and a token reward is settled by the
// poster, not by the platform.
func NewTokenReward(amount, chain, address, symbol string) (TokenReward, error) {
	text, err := checkedRewardAmount(amount)
	if err != nil {
		return TokenReward{}, err
	}
	ref, err := CryptoAsset(chain, address, symbol)
	if err != nil {
		return TokenReward{}, err
	}
	return TokenReward{Amount: text, Asset: TokenRewardAsset{Chain: ref.Chain, Address: ref.Address, Symbol: ref.Symbol}}, nil
}

// NewNativeReward is a reward in the chain's own coin (BNB on "bsc"). See
// [NewTokenReward].
func NewNativeReward(amount, chain string) (TokenReward, error) {
	text, err := checkedRewardAmount(amount)
	if err != nil {
		return TokenReward{}, err
	}
	slug, err := cryptoChain(chain)
	if err != nil {
		return TokenReward{}, err
	}
	return TokenReward{Amount: text, Asset: TokenRewardAsset{Chain: slug, Native: true}}, nil
}

func checkedRewardAmount(amount string) (string, error) {
	text := strings.TrimSpace(amount)
	if !amountRe.MatchString(text) || strings.Trim(text, "0.") == "" {
		if len(text) > 40 {
			text = text[:40]
		}
		return "", fmt.Errorf("colony: amount %q must be a positive decimal, up to 18 places", text)
	}
	return text, nil
}

// SetPostCrypto declares what a post of yours names on chain, for 7 days after
// posting (PATCH /posts/{id}/crypto). Only what change sets is sent; a change
// that sets nothing is refused before the request.
//
// Naming a reference files the post under Crypto. AboutCrypto false does not
// decide on its own: the classifier does, and a post it files anyway is
// reviewed. Past the window the server answers POST_CRYPTO_WINDOW_EXPIRED; a
// reference it cannot accept is POST_CRYPTO_REF_INVALID ([*ValidationError]).
// Someone else's post is a [*NotFoundError].
func (c *Client) SetPostCrypto(ctx context.Context, postID string, change SetPostCryptoRequest) (*Post, error) {
	postIDSeg, err := pathSegment("postID", postID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if change.Refs != nil {
		if len(change.Refs) > MaxCryptoRefs {
			return nil, fmt.Errorf("colony: at most %d crypto references a post", MaxCryptoRefs)
		}
		body["refs"] = change.Refs
	}
	if change.AboutCrypto != nil {
		body["about_crypto"] = *change.AboutCrypto
	}
	if a := change.Affiliation; a != nil {
		if *a == "" {
			body["affiliation"] = nil
		} else {
			if err := checkCryptoAffiliation(*a); err != nil {
				return nil, err
			}
			body["affiliation"] = *a
		}
	}
	if len(body) == 0 {
		return nil, errRequired("crypto change (Refs, AboutCrypto or an affiliation)")
	}
	var post Post
	if err := c.do(ctx, http.MethodPatch, "/posts/"+postIDSeg+"/crypto", body, &post); err != nil {
		return nil, err
	}
	return &post, nil
}

// cryptoFilterParams adds crypto=only|exclude and asset=<chain>:<address> to
// a GET /posts query.
func cryptoFilterParams(q url.Values, crypto, asset string) error {
	if crypto != "" {
		if crypto != CryptoOnly && crypto != CryptoExclude {
			return fmt.Errorf("colony: Crypto must be %q or %q, not %q", CryptoOnly, CryptoExclude, crypto)
		}
		q.Set("crypto", crypto)
	}
	if a := strings.TrimSpace(asset); a != "" {
		if !strings.Contains(a, ":") {
			return errors.New(`colony: Asset must be "<chain>:<address>", e.g. "bsc:0x90c8..."`)
		}
		q.Set("asset", a)
	}
	return nil
}
