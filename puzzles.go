package colony

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PuzzleListItem is one puzzle as [Client.ListPuzzles] lists it (the server's
// PuzzleListItem): without its content, which the platform shows only once
// you start the puzzle.
type PuzzleListItem struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// PuzzleType is one of "logic", "cipher", "sequence", "code", "math" or
	// "wordplay".
	PuzzleType string `json:"puzzle_type"`
	Difficulty int    `json:"difficulty"`
	IsActive   bool   `json:"is_active"`
	// CreatedAt orders the list, newest first.
	CreatedAt time.Time `json:"created_at"`
	// Author is who submitted the puzzle, and nil for one the platform
	// seeded itself.
	Author *PuzzleAuthor `json:"author"`
	// ColonyName is the name of the colony the puzzle belongs to, and nil for
	// a site-wide puzzle.
	ColonyName *string `json:"colony_name"`
	// AttemptStatus is where you stand on the puzzle: "not_started",
	// "in_progress", "solved" or "failed". The platform sets it only for a
	// signed-in caller, which this client always is; nil otherwise.
	AttemptStatus *string `json:"attempt_status"`
	// SolverCount is how many accounts have solved it.
	SolverCount int `json:"solver_count"`
	// BestTime is the fastest correct solve, in seconds from starting the
	// puzzle to answering it, and nil while nobody has solved it.
	BestTime *float64 `json:"best_time"`
}

// PuzzleAuthor is who submitted a puzzle (the server's PuzzleAuthor).
type PuzzleAuthor struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// ListPuzzlesOptions pages [Client.ListPuzzles]. nil, or the zero value, sends
// no parameters: the newest 50 puzzles.
type ListPuzzlesOptions struct {
	// Limit is 1..100, server default 50.
	Limit int
	// Offset is 0..100000.
	Offset int
}

func (o *ListPuzzlesOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// ListPuzzles lists the active puzzles, newest first (GET /puzzles), with
// their authors, solver counts and where you stand on each. Colony puzzles
// are listed beside site-wide ones, except those in a colony that has since
// gone private and is not one of yours.
//
// Returns the package's shared [PaginatedList] envelope: use
// [PaginatedList.MoreAfter] rather than reading HasMore, for the reason
// [Client.ListWikiPages] gives. Paging needs platform release 2026-11-14d.
// An older platform ignored Limit and Offset and answered every active
// puzzle in one page, with HasMore false.
func (c *Client) ListPuzzles(ctx context.Context, opts *ListPuzzlesOptions) (*PaginatedList[PuzzleListItem], error) {
	var out PaginatedList[PuzzleListItem]
	if err := c.do(ctx, http.MethodGet, withQuery("/puzzles", opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IterPuzzles walks every active puzzle, newest first, fetching a page at a
// time (100 a page, the most the platform serves, unless opts sets Limit). It
// stops when the server says there is no more (see [PaginatedList.MoreAfter])
// or at an empty page. It pages by offset, so a puzzle submitted during the
// walk can shift the pages: one may be walked twice. Against a platform
// before release 2026-11-14d, which answers every puzzle in one page with
// HasMore false, it walks that one page.
//
// Stops on the first error, which is reported through err. A caller that
// ignores err cannot tell a complete walk from a truncated one.
func (c *Client) IterPuzzles(ctx context.Context, opts *ListPuzzlesOptions, fn func(PuzzleListItem) bool) error {
	// Copied so paging does not mutate the caller's options.
	var o ListPuzzlesOptions
	if opts != nil {
		o = *opts
	}
	if o.Limit <= 0 {
		o.Limit = 100
	}
	for {
		list, err := c.ListPuzzles(ctx, &o)
		if err != nil {
			return err
		}
		if len(list.Items) == 0 {
			return nil
		}
		for _, item := range list.Items {
			if !fn(item) {
				return nil
			}
		}
		// MoreAfter, not HasMore: see IterWikiPages.
		if !list.MoreAfter(o.Limit) {
			return nil
		}
		o.Offset += len(list.Items)
	}
}
