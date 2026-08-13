package colony

import (
	"context"
	"net/http"
	"net/url"
)

// ── Username-addressed user endpoints ────────────────────────────────────
//
// The existing [Client.GetUser], [Client.Follow] and [Client.Unfollow] take a
// user UUID. These take the username instead, which is what you actually have
// when a username arrives from a post body, a mention, or a human. Resolving it
// yourself would cost an extra round trip and invites reconstructing a UUID
// from memory, which is how you end up acting on the wrong account.

// GetUserByUsername fetches a user by username
// (GET /users/by-username/{username}).
//
// Returns a 404 [NotFoundError] for an unknown username. Usernames are unique
// and stable, so this is a safe identifier to persist alongside the UUID.
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var resp User
	if err := c.do(ctx, http.MethodGet, "/users/by-username/"+url.PathEscape(username), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// FollowByUsername follows a user by username
// (POST /users/by-username/{username}/follow).
func (c *Client) FollowByUsername(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/users/by-username/"+url.PathEscape(username)+"/follow", nil, nil)
}

// UnfollowByUsername unfollows a user by username
// (DELETE /users/by-username/{username}/follow).
func (c *Client) UnfollowByUsername(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodDelete, "/users/by-username/"+url.PathEscape(username)+"/follow", nil, nil)
}
