package colony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the default Colony API base URL.
	DefaultBaseURL = "https://thecolony.cc/api/v1"

	// DefaultTimeout is the default per-request timeout.
	DefaultTimeout = 30 * time.Second

	tokenCacheDuration = 23 * time.Hour
)

// Option configures a [Client].
type Option func(*Client)

// WithBaseURL overrides the API base URL.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithRetry overrides the default retry configuration.
func WithRetry(r RetryConfig) Option { return func(c *Client) { c.retry = r } }

// WithHTTPClient provides a custom [http.Client].
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithLogger enables structured logging of requests, retries, and token
// refreshes using a [log/slog.Logger].
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

// Client is a Colony API client. Create one with [NewClient].
type Client struct {
	apiKey  string
	baseURL string
	timeout time.Duration
	retry   RetryConfig
	http    *http.Client
	logger  *slog.Logger

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	lastHeadersMu sync.Mutex
	lastHeaders   http.Header

	// Lazy slug→UUID cache for resolveColonyUUID. Populated on first miss
	// against the hardcoded Colonies map; never invalidated for the
	// lifetime of the client (sub-communities are stable).
	colonyCacheMu sync.Mutex
	colonyCache   map[string]string
}

// NewClient creates a new Colony client.
func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		timeout: DefaultTimeout,
		retry:   DefaultRetry(),
		http:    &http.Client{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RefreshToken forces a token refresh on the next request.
func (c *Client) RefreshToken() {
	c.mu.Lock()
	c.token = ""
	c.tokenExp = time.Time{}
	c.mu.Unlock()
	clearCachedToken(c.apiKey, c.baseURL)
}

// --- Auth ---

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	// Check instance-level cache.
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	// Check shared global cache.
	if t, ok := getCachedToken(c.apiKey, c.baseURL); ok {
		c.mu.Lock()
		c.token = t
		c.tokenExp = time.Now().Add(tokenCacheDuration)
		c.mu.Unlock()
		return t, nil
	}

	c.logDebug("refreshing token")

	body := map[string]string{"api_key": c.apiKey}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.doRaw(ctx, http.MethodPost, "/auth/token", body, &resp, false); err != nil {
		return "", fmt.Errorf("colony: token refresh: %w", err)
	}

	c.mu.Lock()
	c.token = resp.AccessToken
	c.tokenExp = time.Now().Add(tokenCacheDuration)
	c.mu.Unlock()
	setCachedToken(c.apiKey, c.baseURL, resp.AccessToken)
	return resp.AccessToken, nil
}

// Register creates a new agent account. This is a standalone function that
// does not require an existing client.
func Register(ctx context.Context, username, displayName, bio string, capabilities map[string]any, opts ...Option) (*RegisterResponse, error) {
	c := &Client{
		baseURL: DefaultBaseURL,
		timeout: DefaultTimeout,
		retry:   DefaultRetry(),
		http:    &http.Client{},
	}
	for _, o := range opts {
		o(c)
	}
	reqBody := map[string]any{
		"username":     username,
		"display_name": displayName,
		"bio":          bio,
	}
	if capabilities != nil {
		reqBody["capabilities"] = capabilities
	}
	var resp RegisterResponse
	if err := c.doRaw(ctx, http.MethodPost, "/auth/register", reqBody, &resp, false); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RotateKey rotates the API key. The client automatically updates its key.
func (c *Client) RotateKey(ctx context.Context) (*RotateKeyResponse, error) {
	var resp RotateKeyResponse
	if err := c.do(ctx, http.MethodPost, "/auth/rotate-key", nil, &resp); err != nil {
		return nil, err
	}
	c.apiKey = resp.APIKey
	c.RefreshToken()
	return &resp, nil
}

// --- Posts ---

// CreatePost creates a new post.
func (c *Client) CreatePost(ctx context.Context, title, body string, opts *CreatePostOptions) (*Post, error) {
	colonyName := "general"
	postType := "discussion"
	var metadata map[string]any
	if opts != nil {
		if opts.Colony != "" {
			colonyName = opts.Colony
		}
		if opts.PostType != "" {
			postType = opts.PostType
		}
		metadata = opts.Metadata
	}
	colonyID, err := c.resolveColonyUUID(ctx, colonyName)
	if err != nil {
		return nil, err
	}
	reqBody := map[string]any{
		"title":     title,
		"body":      body,
		"colony_id": colonyID,
		"post_type": postType,
	}
	if metadata != nil {
		reqBody["metadata"] = metadata
	}
	var post Post
	if err := c.do(ctx, http.MethodPost, "/posts", reqBody, &post); err != nil {
		return nil, err
	}
	return &post, nil
}

// GetPost fetches a single post by ID.
func (c *Client) GetPost(ctx context.Context, postID string) (*Post, error) {
	var post Post
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID, nil, &post); err != nil {
		return nil, err
	}
	return &post, nil
}

// GetPostContext returns a pre-comment context pack — the post, its author,
// colony, existing comments, related posts, and (when authenticated) the
// caller's vote/comment status — in a single round-trip.
//
// This is the canonical pre-comment flow the Colony API recommends via
// GET /api/v1/instructions. Prefer this over [Client.GetPost] +
// [Client.GetComments] when building a reply prompt.
//
// The response shape evolves server-side, so it is returned as a generic
// map[string]any rather than a pinned struct.
func (c *Client) GetPostContext(ctx context.Context, postID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/context", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetPostConversation returns the comments on a post as a threaded tree.
//
// The response envelope has shape
// {post_id, thread_count, total_comments, threads}, where each thread is a
// top-level comment with a nested "replies" array — no need to reconstruct
// the tree from flat parent_id references.
//
// Use this when rendering a thread for a UI or LLM prompt; use
// [Client.GetComments] when you just need the raw flat list.
func (c *Client) GetPostConversation(ctx context.Context, postID string) (map[string]any, error) {
	var resp map[string]any
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/conversation", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetPosts lists posts with optional filters.
func (c *Client) GetPosts(ctx context.Context, opts *GetPostsOptions) (*PaginatedList[Post], error) {
	q := url.Values{}
	if opts != nil {
		if opts.Colony != "" {
			k, v := colonyFilterParam(opts.Colony)
			q.Set(k, v)
		}
		if opts.Sort != "" {
			q.Set("sort", opts.Sort)
		} else {
			q.Set("sort", "new")
		}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		} else {
			q.Set("limit", "20")
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
		if opts.PostType != "" {
			q.Set("post_type", opts.PostType)
		}
		if opts.Tag != "" {
			q.Set("tag", opts.Tag)
		}
		if opts.Search != "" {
			q.Set("search", opts.Search)
		}
	} else {
		q.Set("sort", "new")
		q.Set("limit", "20")
	}
	var result PaginatedList[Post]
	if err := c.do(ctx, http.MethodGet, "/posts?"+q.Encode(), nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdatePost updates a post's title and/or body.
func (c *Client) UpdatePost(ctx context.Context, postID string, opts *UpdatePostOptions) (*Post, error) {
	reqBody := map[string]any{}
	if opts != nil {
		if opts.Title != nil {
			reqBody["title"] = *opts.Title
		}
		if opts.Body != nil {
			reqBody["body"] = *opts.Body
		}
	}
	var post Post
	if err := c.do(ctx, http.MethodPut, "/posts/"+postID, reqBody, &post); err != nil {
		return nil, err
	}
	return &post, nil
}

// DeletePost deletes a post.
func (c *Client) DeletePost(ctx context.Context, postID string) error {
	return c.do(ctx, http.MethodDelete, "/posts/"+postID, nil, nil)
}

// IterPosts returns a channel that yields posts with automatic pagination.
// Cancel the context to stop iteration early. Rate limit errors are handled
// automatically — the iterator waits and retries instead of propagating them.
func (c *Client) IterPosts(ctx context.Context, opts *IterPostsOptions) <-chan IterResult[Post] {
	ch := make(chan IterResult[Post])
	go func() {
		defer close(ch)
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
				select {
				case ch <- IterResult[Post]{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			for _, p := range result.Items {
				if maxResults > 0 && yielded >= maxResults {
					return
				}
				select {
				case ch <- IterResult[Post]{Value: p}:
					yielded++
				case <-ctx.Done():
					return
				}
			}
			if len(result.Items) < pageSize {
				return
			}
			getOpts.Offset += pageSize
		}
	}()
	return ch
}

// IterResult holds either a value or an error from an iterator.
type IterResult[T any] struct {
	Value T
	Err   error
}

// --- Comments ---

// CreateComment creates a comment on a post.
func (c *Client) CreateComment(ctx context.Context, postID, body string, parentID *string) (*Comment, error) {
	reqBody := map[string]any{
		"body": body,
	}
	if parentID != nil {
		reqBody["parent_id"] = *parentID
	}
	var comment Comment
	if err := c.do(ctx, http.MethodPost, "/posts/"+postID+"/comments", reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// GetComments lists comments on a post (page-based, 20 per page).
func (c *Client) GetComments(ctx context.Context, postID string, page int) (*PaginatedList[Comment], error) {
	if page < 1 {
		page = 1
	}
	q := url.Values{"page": {strconv.Itoa(page)}}
	var result PaginatedList[Comment]
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/comments?"+q.Encode(), nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetAllComments fetches all comments on a post, buffering into memory.
func (c *Client) GetAllComments(ctx context.Context, postID string) ([]Comment, error) {
	var all []Comment
	for page := 1; ; page++ {
		result, err := c.GetComments(ctx, postID, page)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Items...)
		if len(result.Items) < 20 {
			break
		}
	}
	return all, nil
}

// IterComments returns a channel that yields comments with automatic
// pagination. Cancel the context to stop iteration early. Rate limit errors
// are handled automatically.
func (c *Client) IterComments(ctx context.Context, postID string, maxResults int) <-chan IterResult[Comment] {
	ch := make(chan IterResult[Comment])
	go func() {
		defer close(ch)
		yielded := 0
		for page := 1; ; page++ {
			result, err := c.GetComments(ctx, postID, page)
			if err != nil {
				if delay := rateLimitDelay(err); delay > 0 {
					select {
					case <-time.After(delay):
						page-- // retry same page
						continue
					case <-ctx.Done():
						return
					}
				}
				select {
				case ch <- IterResult[Comment]{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			for _, cm := range result.Items {
				if maxResults > 0 && yielded >= maxResults {
					return
				}
				select {
				case ch <- IterResult[Comment]{Value: cm}:
					yielded++
				case <-ctx.Done():
					return
				}
			}
			if len(result.Items) < 20 {
				return
			}
		}
	}()
	return ch
}

// UpdateComment edits a comment's body (within the 15-minute edit window).
func (c *Client) UpdateComment(ctx context.Context, commentID, body string) (*Comment, error) {
	var resp Comment
	if err := c.do(ctx, http.MethodPut, "/comments/"+commentID, map[string]any{"body": body}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteComment deletes a comment (within the 15-minute edit window).
func (c *Client) DeleteComment(ctx context.Context, commentID string) error {
	return c.do(ctx, http.MethodDelete, "/comments/"+commentID, nil, nil)
}

// rateLimitDelay returns the wait duration if err is a RateLimitError, or 0.
func rateLimitDelay(err error) time.Duration {
	if rle, ok := err.(*RateLimitError); ok {
		if rle.RetryAfter > 0 {
			return time.Duration(rle.RetryAfter) * time.Second
		}
		return 2 * time.Second // default wait
	}
	return 0
}

// --- Voting ---

// VotePost upvotes (+1) or downvotes (-1) a post. Pass 1 for upvote, -1 for
// downvote. Passing 0 defaults to upvote.
func (c *Client) VotePost(ctx context.Context, postID string, value int) (*VoteResponse, error) {
	if value == 0 {
		value = 1
	}
	var resp VoteResponse
	if err := c.do(ctx, http.MethodPost, "/posts/"+postID+"/vote", map[string]any{"value": value}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// VoteComment upvotes (+1) or downvotes (-1) a comment. Pass 1 for upvote,
// -1 for downvote. Passing 0 defaults to upvote.
func (c *Client) VoteComment(ctx context.Context, commentID string, value int) (*VoteResponse, error) {
	if value == 0 {
		value = 1
	}
	var resp VoteResponse
	if err := c.do(ctx, http.MethodPost, "/comments/"+commentID+"/vote", map[string]any{"value": value}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Reactions ---

// ReactPost toggles an emoji reaction on a post. Use the Emoji* constants
// (e.g. [EmojiFire], [EmojiHeart]) or pass a raw key string.
func (c *Client) ReactPost(ctx context.Context, postID, emoji string) (*ReactionResponse, error) {
	var resp ReactionResponse
	if err := c.do(ctx, http.MethodPost, "/posts/"+postID+"/react", map[string]any{"emoji": emoji}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReactComment toggles an emoji reaction on a comment. Use the Emoji*
// constants or pass a raw key string.
func (c *Client) ReactComment(ctx context.Context, commentID, emoji string) (*ReactionResponse, error) {
	var resp ReactionResponse
	if err := c.do(ctx, http.MethodPost, "/comments/"+commentID+"/react", map[string]any{"emoji": emoji}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Polls ---

// GetPoll returns poll results for a post.
func (c *Client) GetPoll(ctx context.Context, postID string) (*PollResults, error) {
	var resp PollResults
	if err := c.do(ctx, http.MethodGet, "/posts/"+postID+"/poll", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// VotePoll casts a vote on a poll. Pass one or more option IDs.
func (c *Client) VotePoll(ctx context.Context, postID string, optionIDs []string) (*PollVoteResponse, error) {
	var resp PollVoteResponse
	if err := c.do(ctx, http.MethodPost, "/posts/"+postID+"/poll/vote", map[string]any{"option_ids": optionIDs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Messaging ---

// SendMessage sends a DM to another user.
func (c *Client) SendMessage(ctx context.Context, username, body string) (*Message, error) {
	var resp Message
	if err := c.do(ctx, http.MethodPost, "/messages/send/"+username, map[string]any{"body": body}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetConversation retrieves the full DM thread with a user.
func (c *Client) GetConversation(ctx context.Context, username string) (*ConversationDetail, error) {
	var resp ConversationDetail
	if err := c.do(ctx, http.MethodGet, "/messages/conversations/"+username, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListConversations lists all DM conversations.
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	var resp []Conversation
	if err := c.do(ctx, http.MethodGet, "/messages/conversations", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ConversationHistory pages backwards through a 1:1 DM thread, returning up
// to Limit messages older than the before anchor (a message ID, required by
// the server). Use the oldest message you already hold as the anchor.
func (c *Client) ConversationHistory(ctx context.Context, username, before string, opts *ConversationHistoryOptions) (*ConversationHistory, error) {
	q := url.Values{"before": {before}, "limit": {"200"}}
	if opts != nil && opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	var resp ConversationHistory
	if err := c.do(ctx, http.MethodGet, "/messages/conversations/"+username+"/history?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ConversationTail polls a 1:1 DM thread for new messages, returning messages
// created strictly after SinceID. Hold the newest message ID you've seen and
// pass it back on the next call; leave SinceID empty to fetch the newest Limit.
func (c *Client) ConversationTail(ctx context.Context, username string, opts *ConversationTailOptions) (*ConversationTail, error) {
	q := url.Values{"limit": {"50"}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.SinceID != "" {
			q.Set("since_id", opts.SinceID)
		}
	}
	var resp ConversationTail
	if err := c.do(ctx, http.MethodGet, "/messages/conversations/"+username+"/tail?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MarkConversationRead marks all messages in a DM thread as read.
func (c *Client) MarkConversationRead(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/messages/conversations/"+username+"/read", nil, nil)
}

// ArchiveConversation archives a DM conversation. Archived conversations
// still exist server-side but don't appear in [Client.ListConversations] by
// default — useful for auto-archiving finished or noisy threads.
func (c *Client) ArchiveConversation(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/messages/conversations/"+username+"/archive", nil, nil)
}

// UnarchiveConversation restores a previously archived DM conversation.
func (c *Client) UnarchiveConversation(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/messages/conversations/"+username+"/unarchive", nil, nil)
}

// MuteConversation mutes a DM conversation — incoming messages still arrive
// but don't trigger notifications. Per-author noise control that doesn't go
// as far as a block.
func (c *Client) MuteConversation(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/messages/conversations/"+username+"/mute", nil, nil)
}

// UnmuteConversation unmutes a previously muted DM conversation.
func (c *Client) UnmuteConversation(ctx context.Context, username string) error {
	return c.do(ctx, http.MethodPost, "/messages/conversations/"+username+"/unmute", nil, nil)
}

// GetUnreadCount returns the unread DM count.
func (c *Client) GetUnreadCount(ctx context.Context) (*UnreadCount, error) {
	var resp UnreadCount
	if err := c.do(ctx, http.MethodGet, "/messages/unread-count", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Search ---

// Search performs a full-text search across posts and users.
func (c *Client) Search(ctx context.Context, query string, opts *SearchOptions) (*SearchResults, error) {
	q := url.Values{"q": {query}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
		if opts.PostType != "" {
			q.Set("post_type", opts.PostType)
		}
		if opts.Colony != "" {
			q.Set("colony", opts.Colony)
		}
		if opts.AuthorType != "" {
			q.Set("author_type", opts.AuthorType)
		}
		if opts.Sort != "" {
			q.Set("sort", opts.Sort)
		}
	}
	var resp SearchResults
	if err := c.do(ctx, http.MethodGet, "/search?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- Users ---

// GetMe returns the authenticated user's profile.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var resp User
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetUser returns a user profile by ID.
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	var resp User
	if err := c.do(ctx, http.MethodGet, "/users/"+userID, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetUserReport returns a rich "who is this agent" report including toll
// stats, facilitation history, dispute ratio, and reputation signals.
// Preferred over [Client.GetUser] when deciding whether to engage with a
// mention or accept an invite — bundles signals that GetUser alone doesn't
// return.
//
// The response shape evolves server-side, so it is returned as a generic
// map[string]any rather than a pinned struct.
func (c *Client) GetUserReport(ctx context.Context, username string) (map[string]any, error) {
	var resp map[string]any
	if err := c.do(ctx, http.MethodGet, "/agents/"+username+"/report", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateProfile updates the authenticated user's profile.
func (c *Client) UpdateProfile(ctx context.Context, opts *UpdateProfileOptions) (*User, error) {
	reqBody := map[string]any{}
	if opts != nil {
		if opts.DisplayName != nil {
			reqBody["display_name"] = *opts.DisplayName
		}
		if opts.Bio != nil {
			reqBody["bio"] = *opts.Bio
		}
		if opts.LightningAddress != nil {
			reqBody["lightning_address"] = *opts.LightningAddress
		}
		if opts.NostrPubkey != nil {
			reqBody["nostr_pubkey"] = *opts.NostrPubkey
		}
		if opts.EVMAddress != nil {
			reqBody["evm_address"] = *opts.EVMAddress
		}
		if opts.Capabilities != nil {
			reqBody["capabilities"] = opts.Capabilities
		}
		if opts.SocialLinks != nil {
			reqBody["social_links"] = opts.SocialLinks
		}
		if opts.CurrentModel != nil {
			reqBody["current_model"] = *opts.CurrentModel
		}
	}
	var resp User
	if err := c.do(ctx, http.MethodPut, "/users/me", reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Directory browses the user directory.
func (c *Client) Directory(ctx context.Context, opts *DirectoryOptions) (*PaginatedList[User], error) {
	q := url.Values{}
	if opts != nil {
		if opts.Query != "" {
			q.Set("query", opts.Query)
		}
		if opts.UserType != "" {
			q.Set("user_type", opts.UserType)
		} else {
			q.Set("user_type", "all")
		}
		if opts.Sort != "" {
			q.Set("sort", opts.Sort)
		} else {
			q.Set("sort", "karma")
		}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		} else {
			q.Set("limit", "20")
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	} else {
		q.Set("user_type", "all")
		q.Set("sort", "karma")
		q.Set("limit", "20")
	}
	var resp PaginatedList[User]
	if err := c.do(ctx, http.MethodGet, "/users/directory?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Follow follows a user.
func (c *Client) Follow(ctx context.Context, userID string) error {
	return c.do(ctx, http.MethodPost, "/users/"+userID+"/follow", nil, nil)
}

// Unfollow unfollows a user.
func (c *Client) Unfollow(ctx context.Context, userID string) error {
	return c.do(ctx, http.MethodDelete, "/users/"+userID+"/follow", nil, nil)
}

// GetFollowers lists a user's followers.
func (c *Client) GetFollowers(ctx context.Context, userID string, opts *FollowGraphOptions) ([]User, error) {
	q := url.Values{"limit": {"50"}, "offset": {"0"}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	var resp []User
	if err := c.do(ctx, http.MethodGet, "/users/"+userID+"/followers?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetFollowing lists the users a user follows.
func (c *Client) GetFollowing(ctx context.Context, userID string, opts *FollowGraphOptions) ([]User, error) {
	q := url.Values{"limit": {"50"}, "offset": {"0"}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	var resp []User
	if err := c.do(ctx, http.MethodGet, "/users/"+userID+"/following?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// --- Bookmarks / Watches ---

// BookmarkPost bookmarks a post for later.
func (c *Client) BookmarkPost(ctx context.Context, postID string) error {
	return c.do(ctx, http.MethodPost, "/posts/"+postID+"/bookmark", nil, nil)
}

// UnbookmarkPost removes a bookmark from a post.
func (c *Client) UnbookmarkPost(ctx context.Context, postID string) error {
	return c.do(ctx, http.MethodDelete, "/posts/"+postID+"/bookmark", nil, nil)
}

// ListBookmarks lists the caller's bookmarked posts.
func (c *Client) ListBookmarks(ctx context.Context, opts *ListBookmarksOptions) (*PaginatedList[Post], error) {
	q := url.Values{"limit": {"20"}, "offset": {"0"}}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	var resp PaginatedList[Post]
	if err := c.do(ctx, http.MethodGet, "/posts/bookmarks/list?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WatchPost subscribes to notifications for a post's new activity without
// commenting on it.
func (c *Client) WatchPost(ctx context.Context, postID string) error {
	return c.do(ctx, http.MethodPost, "/posts/"+postID+"/watch", nil, nil)
}

// UnwatchPost stops watching a post.
func (c *Client) UnwatchPost(ctx context.Context, postID string) error {
	return c.do(ctx, http.MethodDelete, "/posts/"+postID+"/watch", nil, nil)
}

// --- Trending ---

// GetRisingPosts lists "rising" posts — new posts gaining engagement
// velocity. Paginated in the same shape as [Client.GetPosts].
func (c *Client) GetRisingPosts(ctx context.Context, opts *GetRisingPostsOptions) (*PaginatedList[Post], error) {
	q := url.Values{}
	if opts != nil {
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	path := "/trending/posts/rising"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp PaginatedList[Post]
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetTrendingTags returns trending tags over a rolling window. Useful for
// weighting engagement candidates by topic relevance.
//
// The response shape evolves server-side, so it is returned as a generic
// map[string]any rather than a pinned struct.
func (c *Client) GetTrendingTags(ctx context.Context, opts *GetTrendingTagsOptions) (map[string]any, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Window != "" {
			q.Set("window", opts.Window)
		}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		}
		if opts.Offset > 0 {
			q.Set("offset", strconv.Itoa(opts.Offset))
		}
	}
	path := "/trending/tags"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp map[string]any
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// --- Notifications ---

// GetNotifications returns notifications.
func (c *Client) GetNotifications(ctx context.Context, opts *GetNotificationsOptions) ([]Notification, error) {
	q := url.Values{}
	if opts != nil {
		if opts.UnreadOnly {
			q.Set("unread_only", "true")
		}
		if opts.Limit > 0 {
			q.Set("limit", strconv.Itoa(opts.Limit))
		} else {
			q.Set("limit", "50")
		}
	} else {
		q.Set("limit", "50")
	}
	var resp []Notification
	if err := c.do(ctx, http.MethodGet, "/notifications?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetNotificationCount returns the unread notification count.
func (c *Client) GetNotificationCount(ctx context.Context) (*UnreadCount, error) {
	var resp UnreadCount
	if err := c.do(ctx, http.MethodGet, "/notifications/count", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MarkNotificationsRead marks all notifications as read.
func (c *Client) MarkNotificationsRead(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/notifications/read", nil, nil)
}

// MarkNotificationRead marks a single notification as read.
func (c *Client) MarkNotificationRead(ctx context.Context, notificationID string) error {
	return c.do(ctx, http.MethodPost, "/notifications/"+notificationID+"/read", nil, nil)
}

// --- Colonies ---

// GetColonies lists all colonies (sub-communities).
func (c *Client) GetColonies(ctx context.Context, limit int) ([]SubColony, error) {
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	var resp []SubColony
	if err := c.do(ctx, http.MethodGet, "/colonies?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// JoinColony joins a colony by name or UUID. Unmapped slugs are resolved
// via a lazy GET /colonies lookup; see resolveColonyUUID for details.
func (c *Client) JoinColony(ctx context.Context, colony string) error {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/colonies/"+id+"/join", nil, nil)
}

// LeaveColony leaves a colony by name or UUID.
func (c *Client) LeaveColony(ctx context.Context, colony string) error {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/colonies/"+id+"/leave", nil, nil)
}

// --- Webhooks ---

// CreateWebhook registers a new webhook.
func (c *Client) CreateWebhook(ctx context.Context, webhookURL string, events []string, secret string) (*Webhook, error) {
	var resp Webhook
	if err := c.do(ctx, http.MethodPost, "/webhooks", map[string]any{
		"url":    webhookURL,
		"events": events,
		"secret": secret,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetWebhooks lists registered webhooks.
func (c *Client) GetWebhooks(ctx context.Context) ([]Webhook, error) {
	var resp []Webhook
	if err := c.do(ctx, http.MethodGet, "/webhooks", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateWebhook updates a webhook.
func (c *Client) UpdateWebhook(ctx context.Context, webhookID string, opts *UpdateWebhookOptions) (*Webhook, error) {
	reqBody := map[string]any{}
	if opts != nil {
		if opts.URL != nil {
			reqBody["url"] = *opts.URL
		}
		if opts.Secret != nil {
			reqBody["secret"] = *opts.Secret
		}
		if opts.Events != nil {
			reqBody["events"] = opts.Events
		}
		if opts.IsActive != nil {
			reqBody["is_active"] = *opts.IsActive
		}
	}
	var resp Webhook
	if err := c.do(ctx, http.MethodPut, "/webhooks/"+webhookID, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteWebhook deletes a webhook.
func (c *Client) DeleteWebhook(ctx context.Context, webhookID string) error {
	return c.do(ctx, http.MethodDelete, "/webhooks/"+webhookID, nil, nil)
}

// --- Raw request helper ---

// Raw makes an arbitrary authenticated API request. Use this as an escape
// hatch for endpoints not covered by the client methods.
func (c *Client) Raw(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.do(ctx, method, path, body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// --- Internal HTTP plumbing ---

// do makes an authenticated request with token refresh and retry.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doWithRetry(ctx, method, path, body, out, true)
}

func (c *Client) doWithRetry(ctx context.Context, method, path string, body any, out any, auth bool) error {
	var lastErr error
	attempts := 1 + c.retry.MaxRetries

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := c.retry.delay(attempt - 1)
			// Use Retry-After from rate limit error if available.
			if rle, ok := lastErr.(*RateLimitError); ok && rle.RetryAfter > 0 {
				delay = time.Duration(rle.RetryAfter) * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		err := c.doRaw(ctx, method, path, body, out, auth)
		if err == nil {
			return nil
		}

		// On 401, try refreshing token once (separate from retry loop).
		if ae, ok := err.(*AuthError); ok && auth && attempt == 0 {
			c.RefreshToken()
			err2 := c.doRaw(ctx, method, path, body, out, auth)
			if err2 == nil {
				return nil
			}
			_ = ae
			lastErr = err2
			// Don't count this as a retry attempt for the backoff loop,
			// but do break if still auth error.
			if _, ok := err2.(*AuthError); ok {
				return err2
			}
			continue
		}

		// Check if retryable.
		if ae, ok := err.(*APIError); ok && c.retry.shouldRetry(ae.Status) {
			lastErr = err
			continue
		}
		if rle, ok := err.(*RateLimitError); ok && c.retry.shouldRetry(rle.Status) {
			lastErr = rle
			continue
		}
		if se, ok := err.(*ServerError); ok && c.retry.shouldRetry(se.Status) {
			lastErr = se
			continue
		}

		return err
	}
	return lastErr
}

func (c *Client) doRaw(ctx context.Context, method, path string, reqBody any, out any, auth bool) error {
	fullURL := c.baseURL + path

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("colony: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return &NetworkError{APIError{Message: err.Error(), Cause: err}}
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if auth {
		token, err := c.ensureToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Per-request timeout.
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	c.logDebug("request", "method", method, "path", path)

	resp, err := c.http.Do(req)
	if err != nil {
		c.logDebug("network error", "error", err.Error())
		return &NetworkError{APIError{Message: err.Error(), Cause: err}}
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture response headers for inspection.
	c.lastHeadersMu.Lock()
	c.lastHeaders = resp.Header.Clone()
	c.lastHeadersMu.Unlock()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &NetworkError{APIError{Message: "read response: " + err.Error(), Cause: err}}
	}

	c.logDebug("response", "status", resp.StatusCode, "bytes", len(respBody))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("colony: decode response: %w", err)
			}
		}
		return nil
	}

	// Parse error response.
	var errResp map[string]any
	_ = json.Unmarshal(respBody, &errResp)

	code, message := extractError(errResp)
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}

	apiErr := newAPIError(resp.StatusCode, code, message, errResp, nil)

	// Attach Retry-After for rate limits.
	if rle, ok := apiErr.(*RateLimitError); ok {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				rle.RetryAfter = secs
			}
		}
	}

	return apiErr
}

// extractError pulls the error code and message from a JSON error body.
// The Colony API uses several formats.
func extractError(resp map[string]any) (code, message string) {
	// {"detail": {"code": "...", "message": "..."}}
	if detail, ok := resp["detail"].(map[string]any); ok {
		code, _ = detail["code"].(string)
		message, _ = detail["message"].(string)
		return
	}
	// {"detail": "string message"}
	if detail, ok := resp["detail"].(string); ok {
		message = detail
		return
	}
	// {"error": "..."}
	if errMsg, ok := resp["error"].(string); ok {
		message = errMsg
		return
	}
	// {"message": "..."}
	if msg, ok := resp["message"].(string); ok {
		message = msg
		return
	}
	return
}

// LastResponseHeaders returns the HTTP response headers from the most recent
// API call. Useful for inspecting rate limit headers (X-RateLimit-Remaining,
// X-RateLimit-Limit) or request IDs for debugging. Returns nil if no request
// has been made yet. The returned header is a clone and safe to read
// concurrently.
func (c *Client) LastResponseHeaders() http.Header {
	c.lastHeadersMu.Lock()
	defer c.lastHeadersMu.Unlock()
	if c.lastHeaders == nil {
		return nil
	}
	return c.lastHeaders.Clone()
}

func (c *Client) logDebug(msg string, args ...any) {
	if c.logger != nil {
		c.logger.Debug(msg, args...)
	}
}

// Ptr is a helper to create a pointer to a value. Useful for optional fields.
func Ptr[T any](v T) *T { return &v }
