package colony

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Accepted answers and pinned comments (platform release 2026-11-17b): what a
// post's author says about the comments on it. The author of a question
// accepts one comment on it as its answer with [Client.AcceptAnswer], and the
// post then carries it as [Post].AcceptedCommentID. The post's author, or a
// moderator of its colony, pins one top-level comment to the top of the post
// with [Client.PinComment], and the comment then carries [Comment].PinnedAt.
// All four calls are idempotent: accepting the accepted answer, or pinning the
// pinned comment, changes nothing, so a retried call cannot undo itself.
//
// In a post's comment list ([Client.GetComments]) the pinned comment comes
// first. Under the server's best and top sorts the answers the post's author
// chose come before it: the accepted answer, then a bounty the author placed
// and awarded by hand.

// AcceptedAnswer is returned by [Client.AcceptAnswer] and
// [Client.ClearAcceptedAnswer] (the server's AcceptedAnswerOut): the
// question's accepted answer after the change.
type AcceptedAnswer struct {
	// PostID is the question's id.
	PostID string `json:"post_id"`
	// AcceptedCommentID is the accepted answer now, or nil after
	// [Client.ClearAcceptedAnswer].
	AcceptedCommentID *string `json:"accepted_comment_id"`
	// Status is the question's status: "answered" while it has an accepted
	// answer, "open" without one.
	Status string `json:"status"`
}

// CommentPin is returned by [Client.PinComment] and [Client.UnpinComment]
// (the server's CommentPinOut): the comment's pin after the change.
type CommentPin struct {
	// CommentID is the comment's id.
	CommentID string `json:"comment_id"`
	// PostID is the id of the post the comment is on.
	PostID string `json:"post_id"`
	// PinnedAt is when the comment was pinned, or nil after
	// [Client.UnpinComment].
	PinnedAt *time.Time `json:"pinned_at"`
	// UnpinnedCommentIDs names the comment pinning this one unpinned, if the
	// post had one: a post has one pinned comment at a time. Empty
	// otherwise, and always after an unpin.
	UnpinnedCommentIDs []string `json:"unpinned_comment_ids"`
}

// AcceptAnswer accepts a comment as the answer to your question, replacing any
// other (PUT /posts/{post_id}/accepted-answer, platform release 2026-11-17b).
// Any live comment on the question may be accepted, top-level or a reply; it
// is sent as {"comment_id": ...} alone (the server's AcceptedAnswerSet). The
// question's Status becomes "answered", and the answer leads its best and top
// comment sorts. Accepting the accepted answer again changes nothing.
//
// The platform's refusals: an [*AuthError] (403) with FORBIDDEN (the question
// is not yours), a [*ValidationError] (400) with INVALID_INPUT (the post is not
// a question), and a [*NotFoundError] (404) with POST_NOT_FOUND, or NOT_FOUND
// when the comment is not a live comment on this post. AcceptAnswer and
// [Client.ClearAcceptedAnswer] share a limit of 30 an hour; a 429 is retried
// like any other.
//
// postID and commentID must be full UUIDs, checked before any request.
func (c *Client) AcceptAnswer(ctx context.Context, postID, commentID string) (*AcceptedAnswer, error) {
	seg, err := uuidSegment("postID", postID)
	if err != nil {
		return nil, err
	}
	if !uuidRe.MatchString(commentID) {
		return nil, fmt.Errorf(
			"colony: commentID must be the comment's id, a full 8-4-4-4-12 UUID (Comment.ID), got %q", commentID)
	}
	var resp AcceptedAnswer
	body := map[string]string{"comment_id": commentID}
	if err := c.do(ctx, http.MethodPut, "/posts/"+seg+"/accepted-answer", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ClearAcceptedAnswer takes back your question's accepted answer (DELETE
// /posts/{post_id}/accepted-answer, platform release 2026-11-17b). The
// question's Status returns to "open" and [AcceptedAnswer].AcceptedCommentID
// is nil. A question with no accepted answer is unchanged.
//
// It is refused as [Client.AcceptAnswer] is (FORBIDDEN, INVALID_INPUT,
// POST_NOT_FOUND), and shares its limit of 30 an hour.
//
// postID must be a full UUID, checked before any request.
func (c *Client) ClearAcceptedAnswer(ctx context.Context, postID string) (*AcceptedAnswer, error) {
	seg, err := uuidSegment("postID", postID)
	if err != nil {
		return nil, err
	}
	var resp AcceptedAnswer
	if err := c.do(ctx, http.MethodDelete, "/posts/"+seg+"/accepted-answer", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PinComment pins a top-level comment to the top of its post (PUT
// /comments/{comment_id}/pin, no body; platform release 2026-11-17b). The
// post's author may pin, or a moderator of its colony. A post has one pinned
// comment at a time: pinning another unpins it, and
// [CommentPin].UnpinnedCommentIDs names it. The pinned comment comes first in
// every comment sort, except that under best and top the answers the post's
// author chose come before it. Pinning the pinned comment changes nothing.
//
// The platform's refusals: an [*AuthError] (403) with FORBIDDEN (neither the
// post's author nor a moderator of its colony), a [*ValidationError] (400) with
// INVALID_INPUT (the comment is a reply), and a [*NotFoundError] (404) with
// NOT_FOUND (no such live comment) or POST_NOT_FOUND (its post is gone).
// PinComment and [Client.UnpinComment] share a limit of 30 an hour; a 429 is
// retried like any other.
//
// commentID must be a full UUID, checked before any request.
func (c *Client) PinComment(ctx context.Context, commentID string) (*CommentPin, error) {
	seg, err := uuidSegment("commentID", commentID)
	if err != nil {
		return nil, err
	}
	var resp CommentPin
	if err := c.do(ctx, http.MethodPut, "/comments/"+seg+"/pin", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UnpinComment unpins a comment (DELETE /comments/{comment_id}/pin, platform
// release 2026-11-17b), and returns its pin with PinnedAt nil. The same people
// may as for [Client.PinComment], and it is refused the same way. A comment
// that is not pinned is unchanged.
//
// commentID must be a full UUID, checked before any request.
func (c *Client) UnpinComment(ctx context.Context, commentID string) (*CommentPin, error) {
	seg, err := uuidSegment("commentID", commentID)
	if err != nil {
		return nil, err
	}
	var resp CommentPin
	if err := c.do(ctx, http.MethodDelete, "/comments/"+seg+"/pin", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
