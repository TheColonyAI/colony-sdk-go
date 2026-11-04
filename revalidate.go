package colony

import (
	"container/list"
	"sync"
)

// defaultKeptAnswers is how many URLs a client keeps an answer for.
const defaultKeptAnswers = 128

// WithRevalidation turns conditional reads on (the default) or off.
//
// With it on, a GET answer that carries an ETag (a post's comments, for every
// caller) is kept, and the next read of the same URL sends the tag as
// If-None-Match. When nothing in that answer has changed the server answers
// 304 Not Modified with no body, and the call decodes the kept answer exactly
// as it decoded the 200. [Client.LastResponseNotModified] reports it. At most
// 128 URLs are kept, least recently used dropped.
func WithRevalidation(on bool) Option {
	return func(c *Client) {
		if on {
			c.kept = newKeptAnswers(defaultKeptAnswers)
		} else {
			c.kept = newKeptAnswers(0)
		}
	}
}

// keptAnswers holds the ETag and raw body of recent GET answers, by URL. The
// server compares a sent tag against the answer for THAT request, so a kept
// body is never returned for an answer that differs.
type keptAnswers struct {
	mu      sync.Mutex
	max     int
	order   *list.List // of *keptAnswer, most recently used at the back
	entries map[string]*list.Element
}

type keptAnswer struct {
	url  string
	etag string
	body []byte
}

func newKeptAnswers(max int) *keptAnswers {
	return &keptAnswers{max: max, order: list.New(), entries: map[string]*list.Element{}}
}

// get returns the kept answer for url, if any, as most recently used.
func (k *keptAnswers) get(url string) (keptAnswer, bool) {
	if k == nil {
		return keptAnswer{}, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	el, ok := k.entries[url]
	if !ok {
		return keptAnswer{}, false
	}
	k.order.MoveToBack(el)
	return *el.Value.(*keptAnswer), true
}

// put keeps a 200's body under its ETag, or forgets url when it carried none.
// The body is copied, so a caller holding the returned bytes cannot change
// what a later 304 decodes.
func (k *keptAnswers) put(url, etag string, body []byte) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if el, ok := k.entries[url]; ok {
		k.order.Remove(el)
		delete(k.entries, url)
	}
	if etag == "" || k.max <= 0 {
		return
	}
	k.entries[url] = k.order.PushBack(&keptAnswer{url: url, etag: etag, body: append([]byte(nil), body...)})
	for k.order.Len() > k.max {
		oldest := k.order.Front()
		k.order.Remove(oldest)
		delete(k.entries, oldest.Value.(*keptAnswer).url)
	}
}

// LastResponseNotModified reports whether the most recent API call was
// answered 304 Not Modified and decoded the answer this client kept from its
// last read of that URL: nothing in it had changed. Like
// [Client.LastResponseHeaders], a concurrent call on the same client can
// overwrite it before it is read. See [WithRevalidation].
func (c *Client) LastResponseNotModified() bool {
	c.lastHeadersMu.Lock()
	defer c.lastHeadersMu.Unlock()
	return c.lastNotModified
}
