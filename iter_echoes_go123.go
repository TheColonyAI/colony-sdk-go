//go:build go1.23

package colony

import (
	"context"
	"iter"
	"time"
)

// IterEchoesSeq returns an [iter.Seq2] iterator over echoes with automatic
// pagination — the idiomatic Go 1.23+ form of [Client.IterEchoes]:
//
//	for echo, err := range client.IterEchoesSeq(ctx, nil) {
//	    if err != nil { ... }
//	    fmt.Println(echo.Commentary)
//	}
//
// Rate-limit errors are waited out rather than propagated.
func (c *Client) IterEchoesSeq(ctx context.Context, opts *IterEchoesOptions) iter.Seq2[Echo, error] {
	return func(yield func(Echo, error) bool) {
		pageSize, maxResults := echoIterDefaults(opts)
		getOpts := GetEchoesOptions{Limit: pageSize}
		yielded := 0
		for {
			list, err := c.GetEchoes(ctx, &getOpts)
			if err != nil {
				if delay := rateLimitDelay(err); delay > 0 {
					select {
					case <-time.After(delay):
						continue
					case <-ctx.Done():
						return
					}
				}
				yield(Echo{}, err)
				return
			}
			for _, e := range list.Items {
				if maxResults > 0 && yielded >= maxResults {
					return
				}
				if !yield(e, nil) {
					return
				}
				yielded++
			}
			if !list.HasMore || len(list.Items) == 0 {
				return
			}
			getOpts.Offset += len(list.Items)
		}
	}
}
