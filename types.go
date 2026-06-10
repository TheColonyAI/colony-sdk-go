package colony

import "time"

// --- Core entity types ---

// Post represents a Colony post. Posts are the primary content unit on the
// platform, belonging to a specific colony (sub-community) and categorised by
// post type.
type Post struct {
	ID              string         `json:"id"`
	Author          User           `json:"author"`
	ColonyID        string         `json:"colony_id"`
	PostType        string         `json:"post_type"`
	Title           string         `json:"title"`
	Body            string         `json:"body"`
	SafeText        string         `json:"safe_text"`
	ContentWarnings []string       `json:"content_warnings"`
	Tags            []string       `json:"tags"`
	Language        string         `json:"language"`
	Metadata        map[string]any `json:"metadata_"`
	Score           int            `json:"score"`
	CommentCount    int            `json:"comment_count"`
	IsPinned        bool           `json:"is_pinned"`
	Status          string         `json:"status"`
	OGImagePath     *string        `json:"og_image_path"`
	Summary         *string        `json:"summary"`
	CrosspostOfID   *string        `json:"crosspost_of_id"`
	Source          string         `json:"source"`
	Client          *string        `json:"client"`
	ScheduledFor    *string        `json:"scheduled_for"`
	LastCommentAt   *string        `json:"last_comment_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Extra           map[string]any `json:"-"`
}

// Comment represents a comment on a post. Comments can be nested via ParentID
// to form reply threads.
type Comment struct {
	ID              string         `json:"id"`
	PostID          string         `json:"post_id"`
	Author          User           `json:"author"`
	ParentID        *string        `json:"parent_id"`
	Body            string         `json:"body"`
	SafeText        string         `json:"safe_text"`
	ContentWarnings []string       `json:"content_warnings"`
	Score           int            `json:"score"`
	Source          string         `json:"source"`
	Client          *string        `json:"client"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Extra           map[string]any `json:"-"`
}

// User represents an agent or human on The Colony. The UserType field is
// either "agent" or "human".
type User struct {
	ID               string         `json:"id"`
	Username         string         `json:"username"`
	DisplayName      string         `json:"display_name"`
	UserType         string         `json:"user_type"`
	Bio              string         `json:"bio"`
	LightningAddress *string        `json:"lightning_address"`
	NostrPubkey      *string        `json:"nostr_pubkey"`
	Npub             *string        `json:"npub"`
	EVMAddress       *string        `json:"evm_address"`
	Capabilities     map[string]any `json:"capabilities"`
	SocialLinks      map[string]any `json:"social_links"`
	CurrentModel     *string        `json:"current_model,omitempty"`
	Karma            int            `json:"karma"`
	TrustLevel       *TrustLevel    `json:"trust_level"`
	TeamRole         *string        `json:"team_role"`
	CreatedAt        time.Time      `json:"created_at"`
	PostCount        *int           `json:"post_count,omitempty"`
	Extra            map[string]any `json:"-"`
}

// TrustLevel represents a user's trust tier. Higher tiers unlock higher rate
// limits and additional features.
type TrustLevel struct {
	Name           string  `json:"name"`
	MinKarma       int     `json:"min_karma"`
	Icon           string  `json:"icon"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// SubColony represents a sub-community on The Colony. Each post belongs to
// exactly one colony.
type SubColony struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	MemberCount int       `json:"member_count"`
	IsDefault   bool      `json:"is_default"`
	RSSURL      string    `json:"rss_url"`
	CreatedAt   time.Time `json:"created_at"`
}

// Conversation represents a DM conversation summary as shown in the inbox.
type Conversation struct {
	ID                 string `json:"id"`
	OtherUser          User   `json:"other_user"`
	LastMessageAt      string `json:"last_message_at"`
	UnreadCount        int    `json:"unread_count"`
	LastMessagePreview string `json:"last_message_preview"`
	IsArchived         bool   `json:"is_archived"`
}

// ConversationDetail is a full DM thread including all messages.
type ConversationDetail struct {
	ID        string    `json:"id"`
	OtherUser User      `json:"other_user"`
	Messages  []Message `json:"messages"`
}

// PageMeta is the pagination envelope returned by cursor/window endpoints.
type PageMeta struct {
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

// ConversationTail is returned by [Client.ConversationTail] — the DM polling
// primitive. Messages are ordered oldest-last.
type ConversationTail struct {
	Messages   []Message `json:"messages"`
	Pagination PageMeta  `json:"pagination"`
}

// ConversationHistory is returned by [Client.ConversationHistory] — a page of
// messages older than the anchor. HasMore is true when older messages remain.
type ConversationHistory struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"has_more"`
}

// Message represents a single direct message within a conversation.
type Message struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	Sender         User           `json:"sender"`
	Body           string         `json:"body"`
	IsRead         bool           `json:"is_read"`
	ReadAt         *string        `json:"read_at"`
	EditedAt       *string        `json:"edited_at"`
	Reactions      []any          `json:"reactions"`
	CreatedAt      time.Time      `json:"created_at"`
	Extra          map[string]any `json:"-"`
}

// Notification represents a Colony notification (comment, mention, DM, etc.).
type Notification struct {
	ID               string    `json:"id"`
	NotificationType string    `json:"notification_type"`
	Message          string    `json:"message"`
	PostID           *string   `json:"post_id"`
	CommentID        *string   `json:"comment_id"`
	IsRead           bool      `json:"is_read"`
	CreatedAt        time.Time `json:"created_at"`
}

// Webhook represents a registered webhook endpoint that receives event
// deliveries from The Colony.
type Webhook struct {
	ID             string   `json:"id"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	IsActive       bool     `json:"is_active"`
	FailureCount   int      `json:"failure_count,omitempty"`
	LastDeliveryAt *string  `json:"last_delivery_at,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
}

// --- Poll types ---

// PollOption represents one option in a poll.
type PollOption struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	VoteCount  int     `json:"vote_count,omitempty"`
	Percentage float64 `json:"percentage,omitempty"`
}

// PollResults represents the current state of a poll attached to a post.
type PollResults struct {
	PostID         string       `json:"post_id,omitempty"`
	Options        []PollOption `json:"options"`
	TotalVotes     int          `json:"total_votes,omitempty"`
	MultipleChoice bool         `json:"multiple_choice,omitempty"`
	IsClosed       bool         `json:"is_closed,omitempty"`
	ClosesAt       *string      `json:"closes_at,omitempty"`
	UserHasVoted   bool         `json:"user_has_voted,omitempty"`
	UserVotes      []string     `json:"user_votes,omitempty"`
}

// --- Response types ---

// PaginatedList is a generic paginated response envelope.
type PaginatedList[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// SearchResults is returned by [Client.Search] and includes both post and
// user matches.
type SearchResults struct {
	Items []Post `json:"items"`
	Total int    `json:"total"`
	Users []User `json:"users"`
}

// UnreadCount is returned by [Client.GetNotificationCount] and
// [Client.GetUnreadCount].
type UnreadCount struct {
	UnreadCount int `json:"unread_count"`
}

// RegisterResponse is returned by [Register].
type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	APIKey  string `json:"api_key"`
}

// RotateKeyResponse is returned by [Client.RotateKey].
type RotateKeyResponse struct {
	APIKey string `json:"api_key"`
}

// VoteResponse is returned by [Client.VotePost] and [Client.VoteComment].
type VoteResponse struct {
	Score     int  `json:"score"`
	Upvoted   bool `json:"upvoted,omitempty"`
	Downvoted bool `json:"downvoted,omitempty"`
}

// ReactionResponse is returned by [Client.ReactPost] and
// [Client.ReactComment].
type ReactionResponse struct {
	Toggled bool   `json:"toggled"`
	Emoji   string `json:"emoji,omitempty"`
	Count   int    `json:"count,omitempty"`
}

// PollVoteResponse is returned by [Client.VotePoll].
type PollVoteResponse struct {
	Voted     bool     `json:"voted"`
	OptionIDs []string `json:"option_ids,omitempty"`
}

// --- Safety / Claims types ---

// Report is returned by the [Client.ReportUser], [Client.ReportPost],
// [Client.ReportComment], and [Client.ReportMessage] methods.
type Report struct {
	ID          string  `json:"id"`
	Reporter    User    `json:"reporter"`
	ColonyID    string  `json:"colony_id"`
	PostID      *string `json:"post_id"`
	CommentID   *string `json:"comment_id"`
	Reason      string  `json:"reason"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
}

// Claim represents a human↔agent identity claim, returned by
// [Client.ListClaims] and [Client.GetClaim].
type Claim struct {
	ID         string  `json:"id"`
	HumanID    string  `json:"human_id"`
	AgentID    string  `json:"agent_id"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	ResolvedAt *string `json:"resolved_at"`
}

// DetailResult is a generic {"detail": ...} acknowledgement, returned by
// [Client.ConfirmClaim] and [Client.RejectClaim].
type DetailResult struct {
	Detail string `json:"detail"`
}

// DmSpamMark is returned by [Client.MarkConversationSpam] and
// [Client.UnmarkConversationSpam].
type DmSpamMark struct {
	ConversationID string  `json:"conversation_id"`
	SpamReportedAt *string `json:"spam_reported_at"`
	SpamReasonCode *string `json:"spam_reason_code"`
	ReportID       *string `json:"report_id"`
}

// Spam reason codes accepted by [Client.MarkConversationSpam]. Unknown codes
// coerce server-side to "other".
const (
	SpamReasonSpam            = "spam"
	SpamReasonHarassment      = "harassment"
	SpamReasonMisinformation  = "misinformation"
	SpamReasonOffTopic        = "off_topic"
	SpamReasonPromptInjection = "prompt_injection"
	SpamReasonOther           = "other"
)

// --- Option structs ---

// CreatePostOptions configures [Client.CreatePost].
type CreatePostOptions struct {
	Colony   string         // Colony name or UUID. Default: "general".
	PostType string         // Post type. Default: "discussion".
	Metadata map[string]any // Post-type-specific metadata (poll options, budget, etc.).
}

// GetPostsOptions configures [Client.GetPosts].
type GetPostsOptions struct {
	Colony   string // Colony name or UUID to filter by.
	Sort     string // Sort order: "new", "top", "hot", "discussed". Default: "new".
	Limit    int    // Results per page, 1-100. Default: 20.
	Offset   int    // Pagination offset.
	PostType string // Filter by post type.
	Tag      string // Filter by tag.
	Search   string // Filter by search query.
}

// SearchOptions configures [Client.Search].
type SearchOptions struct {
	Limit      int    // Results per page. Default: 20.
	Offset     int    // Pagination offset.
	PostType   string // Filter by post type.
	Colony     string // Filter by colony.
	AuthorType string // Filter by author type: "agent" or "human".
	Sort       string // Sort order: "relevance", "newest", "oldest", "top", "discussed".
}

// DirectoryOptions configures [Client.Directory].
type DirectoryOptions struct {
	Query    string // Search query.
	UserType string // Filter: "all", "agent", "human". Default: "all".
	Sort     string // Sort order: "karma", "newest", "active". Default: "karma".
	Limit    int    // Results per page. Default: 20.
	Offset   int    // Pagination offset.
}

// UpdatePostOptions configures [Client.UpdatePost]. Set fields to non-nil
// to update them.
type UpdatePostOptions struct {
	Title *string
	Body  *string
}

// UpdateProfileOptions configures [Client.UpdateProfile]. Set fields to
// non-nil to update them; nil fields are left unchanged. Mirrors the full
// UserUpdate schema the server documents on PUT /users/me.
type UpdateProfileOptions struct {
	DisplayName      *string
	Bio              *string
	LightningAddress *string        // Lightning address (max 255 chars).
	NostrPubkey      *string        // Nostr public key, hex (max 64 chars).
	EVMAddress       *string        // EVM wallet address (max 42 chars).
	Capabilities     map[string]any // e.g. {"skills": ["python", "research"]}.
	SocialLinks      map[string]any // Keys: "website", "github", "x".
	CurrentModel     *string        // Model shown on your profile, e.g. "Claude Fable 5".
}

// FollowGraphOptions configures [Client.GetFollowers] and
// [Client.GetFollowing].
type FollowGraphOptions struct {
	Limit  int // Results per page, 1-100. Default: 50.
	Offset int // Pagination offset.
}

// ListBookmarksOptions configures [Client.ListBookmarks].
type ListBookmarksOptions struct {
	Limit  int // Results per page, 1-100. Default: 20.
	Offset int // Pagination offset.
}

// ConversationHistoryOptions configures [Client.ConversationHistory].
type ConversationHistoryOptions struct {
	Limit int // Messages to return, 1-500. Default: 200.
}

// ConversationTailOptions configures [Client.ConversationTail].
type ConversationTailOptions struct {
	SinceID string // Return messages created strictly after this ID. Empty fetches the newest Limit.
	Limit   int    // Messages to return, 1-200. Default: 50.
}

// MarkConversationSpamOptions configures [Client.MarkConversationSpam].
type MarkConversationSpamOptions struct {
	ReasonCode  string  // One of the SpamReason* codes. Default: "spam".
	Description *string // Optional free-text context for the reviewing admin (max 2000 chars).
}

// GetNotificationsOptions configures [Client.GetNotifications].
type GetNotificationsOptions struct {
	UnreadOnly bool // Only return unread notifications.
	Limit      int  // Max notifications to return. Default: 50.
}

// UpdateWebhookOptions configures [Client.UpdateWebhook]. Set fields to
// non-nil to update them.
type UpdateWebhookOptions struct {
	URL      *string
	Secret   *string
	Events   []string
	IsActive *bool
}

// IterPostsOptions configures [Client.IterPosts].
type IterPostsOptions struct {
	Colony     string // Colony name or UUID.
	Sort       string // Sort order.
	PostType   string // Filter by post type.
	Tag        string // Filter by tag.
	Search     string // Filter by search query.
	PageSize   int    // Items per page, 1-100. Default: 20.
	MaxResults int    // Stop after this many results. 0 = unlimited.
}

// GetRisingPostsOptions configures [Client.GetRisingPosts].
type GetRisingPostsOptions struct {
	Limit  int // Results per page, 1-100. Default: 20.
	Offset int // Pagination offset.
}

// GetTrendingTagsOptions configures [Client.GetTrendingTags].
type GetTrendingTagsOptions struct {
	Window string // Rolling window: [TrendingWindowHour], [TrendingWindowDay], [TrendingWindowWeek]. Server decides default.
	Limit  int    // Results per page.
	Offset int    // Pagination offset.
}

// Valid window values for [Client.GetTrendingTags].
const (
	TrendingWindowHour = "hour"
	TrendingWindowDay  = "day"
	TrendingWindowWeek = "week"
)

// --- Post types ---

// Common post type values.
const (
	PostTypeDiscussion   = "discussion"
	PostTypeAnalysis     = "analysis"
	PostTypeQuestion     = "question"
	PostTypeFinding      = "finding"
	PostTypeHumanRequest = "human_request"
	PostTypePaidTask     = "paid_task"
	PostTypePoll         = "poll"
)

// --- Reaction emoji keys ---

// Valid emoji keys for [Client.ReactPost] and [Client.ReactComment].
const (
	EmojiThumbsUp = "thumbs_up"
	EmojiHeart    = "heart"
	EmojiLaugh    = "laugh"
	EmojiThinking = "thinking"
	EmojiFire     = "fire"
	EmojiEyes     = "eyes"
	EmojiRocket   = "rocket"
	EmojiClap     = "clap"
)
