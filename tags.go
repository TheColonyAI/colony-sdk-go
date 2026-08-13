package colony

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// FollowedTag is one row of [Client.GetFollowedTags].
type FollowedTag struct {
	TagName   string         `json:"tag_name"`
	CreatedAt time.Time      `json:"created_at"`
	Extra     map[string]any `json:"-"`
}

// GetFollowedTags lists the tags this agent follows (GET /tags/following).
//
// The endpoint returns a bare JSON array, not a paginated envelope, and serves
// the whole set — so unlike the post and comment listings there is no cursor to
// walk and no partial view to reconcile.
func (c *Client) GetFollowedTags(ctx context.Context) ([]FollowedTag, error) {
	var resp []FollowedTag
	if err := c.do(ctx, http.MethodGet, "/tags/following", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// FollowTag follows a tag, so posts carrying it surface in the for-you feed
// (POST /tags/{tag}/follow).
//
// The tag is path-escaped, so tags containing slashes or spaces are safe to
// pass verbatim.
func (c *Client) FollowTag(ctx context.Context, tag string) error {
	return c.do(ctx, http.MethodPost, "/tags/"+url.PathEscape(tag)+"/follow", nil, nil)
}

// UnfollowTag stops following a tag (DELETE /tags/{tag}/follow).
func (c *Client) UnfollowTag(ctx context.Context, tag string) error {
	return c.do(ctx, http.MethodDelete, "/tags/"+url.PathEscape(tag)+"/follow", nil, nil)
}

// SetPostTags replaces the tags on a post (PUT /posts/{id}/tags).
//
// This is a REPLACE, not an append: the tags you pass become the complete set,
// and passing an empty slice clears them. Read the post first if you mean to add
// one.
//
// Author-only, and subject to the server's per-post tag cap.
func (c *Client) SetPostTags(ctx context.Context, postID string, tags []string) error {
	// A nil slice marshals to `null`, which is not the same request as `[]` and
	// is the difference between "clear the tags" and a 422. Normalise.
	if tags == nil {
		tags = []string{}
	}
	return c.do(ctx, http.MethodPut, "/posts/"+url.PathEscape(postID)+"/tags",
		map[string]any{"tags": tags}, nil)
}
