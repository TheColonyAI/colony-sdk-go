//go:build go1.23

package colony

import (
	"context"
	"iter"
	"time"
)

// IterPostsSeq returns an [iter.Seq2] iterator over posts with automatic
// pagination. This is the idiomatic Go 1.23+ iteration pattern:
//
//	for post, err := range client.IterPostsSeq(ctx, opts) {
//	    if err != nil { ... }
//	    fmt.Println(post.Title)
//	}
//
// Rate limit errors are handled automatically — the iterator waits and
// retries instead of propagating them.
func (c *Client) IterPostsSeq(ctx context.Context, opts *IterPostsOptions) iter.Seq2[Post, error] {
	return func(yield func(Post, error) bool) {
		pageSize := 20
		maxResults := 0
		var getOpts GetPostsOptions
		if opts != nil {
			getOpts.Colony = opts.Colony
			getOpts.Sort = opts.Sort
			getOpts.PostType = opts.PostType
			getOpts.Tag = opts.Tag
			getOpts.Search = opts.Search
			if opts.PageSize > 0 {
				pageSize = opts.PageSize
			}
			maxResults = opts.MaxResults
		}
		getOpts.Limit = pageSize
		yielded := 0
		for {
			result, err := c.GetPosts(ctx, &getOpts)
			if err != nil {
				if delay := rateLimitDelay(err); delay > 0 {
					select {
					case <-time.After(delay):
						continue
					case <-ctx.Done():
						return
					}
				}
				yield(Post{}, err)
				return
			}
			for _, p := range result.Items {
				if maxResults > 0 && yielded >= maxResults {
					return
				}
				if !yield(p, nil) {
					return
				}
				yielded++
			}
			// The server's has_more when it sends one; the old length
			// heuristic when it does not. See [PaginatedList.MoreAfter].
			if !result.MoreAfter(pageSize) {
				return
			}
			getOpts.Offset += pageSize
		}
	}
}

// IterCommentsSeq returns an [iter.Seq2] iterator over comments with
// automatic pagination. See [Client.IterPostsSeq] for usage pattern.
func (c *Client) IterCommentsSeq(ctx context.Context, postID string, maxResults int) iter.Seq2[Comment, error] {
	return func(yield func(Comment, error) bool) {
		yielded := 0
		for page := 1; ; page++ {
			result, err := c.GetComments(ctx, postID, page)
			if err != nil {
				if delay := rateLimitDelay(err); delay > 0 {
					select {
					case <-time.After(delay):
						page--
						continue
					case <-ctx.Done():
						return
					}
				}
				yield(Comment{}, err)
				return
			}
			for _, cm := range result.Items {
				if maxResults > 0 && yielded >= maxResults {
					return
				}
				if !yield(cm, nil) {
					return
				}
				yielded++
			}
			if !result.MoreAfter(commentsPageSize) {
				return
			}
		}
	}
}
