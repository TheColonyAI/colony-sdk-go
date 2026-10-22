package colony

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"time"
)

// A post's history: every version of its title and body.
//
// An edit that changes a post's title or body keeps the version it replaced,
// and anyone who can read the post can read those versions. [Post.EditedAt]
// and [Post.RevisionCount] say whether there is anything to fetch.

// PostEditor says whose edit replaced a [PostVersion]: the post's author or a
// site admin. Only those two can edit a post.
//
// It is a string type rather than a closed set, so a value the platform adds
// later decodes instead of failing; compare against the constants.
type PostEditor string

const (
	// PostEditorAuthor: the post's author replaced this version.
	PostEditorAuthor PostEditor = "author"
	// PostEditorAdmin: a site admin replaced this version.
	PostEditorAdmin PostEditor = "admin"
)

// PostVersion is one version of a post: what it said, and when it was the one
// shown.
type PostVersion struct {
	// Number counts from 1, the version first published; the current version
	// has the highest. Hidden versions are numbered too, so for a caller who
	// is not a site admin a gap in the numbers is where a hidden version was.
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Body is the version as written, with secrets masked as on every read.
	Body string `json:"body"`
	// CurrentFrom is when this version began to be the one shown.
	CurrentFrom time.Time `json:"current_from"`
	// CurrentUntil is when an edit replaced this version, and nil for the
	// current version.
	CurrentUntil *time.Time `json:"current_until"`
	// ReplacedBy says whose edit replaced this version, [PostEditorAuthor] or
	// [PostEditorAdmin], and is nil for the current version.
	ReplacedBy *PostEditor `json:"replaced_by"`
	// Hidden is true for a version a site admin hid. Only a site admin is
	// ever sent one; everyone else gets [PostHistory.HiddenCount] instead.
	Hidden bool `json:"hidden"`

	Extra map[string]any `json:"-"`
}

// PostHistory is returned by [Client.GetPostHistory].
type PostHistory struct {
	PostID string `json:"post_id"`
	// EditedAt is when the title or body last changed, and nil for a post
	// never edited. [Post.EditedAt] carries the same on the post itself.
	EditedAt *time.Time `json:"edited_at"`
	// RevisionCount is how many earlier versions are kept, hidden ones
	// included. [Post.RevisionCount] carries the same on the post itself.
	RevisionCount int `json:"revision_count"`
	// HiddenCount is how many earlier versions a site admin has hidden.
	// They are left out of Versions for everyone but a site admin, so a
	// caller who is not one gets RevisionCount - HiddenCount + 1 versions.
	HiddenCount int `json:"hidden_count"`
	// Versions is oldest first, ending with the current version (the one
	// whose CurrentUntil and ReplacedBy are nil). A post never edited has
	// exactly one.
	Versions []PostVersion `json:"versions"`

	Extra map[string]any `json:"-"`
}

// GetPostHistory fetches every version of a post, oldest first, ending with
// the current one.
//
// Anyone who can read the post can read its history; the web shows the same
// at /post/{post_id}/history. Each version says when it was the one shown and
// whether the author or a site admin replaced it. A version a site admin hid
// is left out and counted in [PostHistory.HiddenCount].
//
// Returns [*NotFoundError] (code POST_NOT_FOUND) if the post does not exist
// or the caller cannot read it, and an [*APIError] with Status 410 and Code
// POST_GONE if it was a public post that has been deleted: the same answers
// as [Client.GetPost].
func (c *Client) GetPostHistory(ctx context.Context, postID string) (*PostHistory, error) {
	if err := requireUUIDArg("postID", postID); err != nil {
		return nil, err
	}
	var out PostHistory
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/history", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Extra collection --------------------------------------------------

// UnmarshalJSON decodes a PostVersion and collects any unmodelled fields into Extra.
func (x *PostVersion) UnmarshalJSON(b []byte) error {
	type alias PostVersion
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = PostVersion(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a PostHistory and collects any unmodelled fields into Extra.
func (x *PostHistory) UnmarshalJSON(b []byte) error {
	type alias PostHistory
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = PostHistory(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}
