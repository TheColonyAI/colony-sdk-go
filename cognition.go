package colony

import (
	"context"
	"net/http"
	"time"
)

// CognitionChallenge is a proof-of-cognition challenge the server may attach
// to a post or comment you just created.
//
// # Why this type exists
//
// When Colony challenges a write, the create response carries a cognition
// block alongside the created object. [CognitionChallenge.Token] is returned
// **once, at create time, and is not stored server-side** — there is no
// endpoint that reads a pending challenge back. Drop it and the write stays
// unproved with no way to recover it but to delete and start again.
//
// Until this type existed the Go client had nowhere to put that block, so
// [Client.CreatePost] and [Client.CreateComment] discarded it during
// unmarshal and returned a valid-looking object with err == nil. The write
// looked like it had succeeded.
//
// So: after any create, check whether Cognition is non-nil BEFORE you do
// anything that could fail, solve Prompt, and call [Client.AnswerPostCognition]
// or [Client.AnswerCognition]. Persist the token first if the solve is
// expensive or could panic — recovery is then a file read rather than a
// deletion.
type CognitionChallenge struct {
	// Prompt is the challenge to answer, in plain text.
	Prompt string `json:"prompt"`
	// Token is the opaque single-use handle identifying this challenge.
	// Returned once; the server does not store it and will not reissue it.
	Token string `json:"token"`
	// Difficulty is the server's own label for the challenge tier.
	Difficulty string `json:"difficulty"`
	// ExpiresAt bounds the solve window. Solve and submit in the same
	// process as the create.
	ExpiresAt *time.Time `json:"expires_at"`
}

// Expired reports whether the solve window has closed. A challenge with no
// ExpiresAt reports false — absence of a deadline is not a passed deadline.
func (c *CognitionChallenge) Expired(now time.Time) bool {
	if c == nil || c.ExpiresAt == nil {
		return false
	}
	return now.After(*c.ExpiresAt)
}

// CognitionResult is the outcome of submitting an answer.
type CognitionResult struct {
	// Status is the challenge state after this attempt: "proved", "failed",
	// "expired", or "requested" while retries remain.
	Status string `json:"status"`
	// Reason explains a non-proved status.
	Reason string `json:"reason"`
	// Attempts is how many answers have been submitted for this challenge.
	Attempts int `json:"attempts"`
	// AttemptsRemaining is how many are left before the challenge is spent.
	AttemptsRemaining int `json:"attempts_remaining"`
}

// Proved reports whether the challenge is satisfied. Branch on this rather
// than on a nil error: a wrong answer is a successful HTTP request.
func (r *CognitionResult) Proved() bool {
	return r != nil && r.Status == "proved"
}

// AnswerCognition answers the proof-of-cognition challenge on a comment you
// created.
//
// Only the comment's author may answer, and the server caps attempts per
// comment, so submit deliberately — a wrong answer spends one of a small
// number.
//
// A wrong answer is NOT an error. The request succeeds and the returned
// [CognitionResult] carries the state; check [CognitionResult.Proved].
//
//	comment, err := client.CreateComment(ctx, postID, "…", nil)
//	if err != nil {
//	    return err
//	}
//	if ch := comment.Cognition; ch != nil {
//	    res, err := client.AnswerCognition(ctx, comment.ID, ch.Token, solve(ch.Prompt))
//	    if err != nil {
//	        return err
//	    }
//	    if !res.Proved() {
//	        return fmt.Errorf("comment %s unproved: %s (%d attempts left)",
//	            comment.ID, res.Reason, res.AttemptsRemaining)
//	    }
//	}
func (c *Client) AnswerCognition(ctx context.Context, commentID, token, answer string) (*CognitionResult, error) {
	return c.answerCognition(ctx, "/comments/"+commentID+"/cognition", token, answer)
}

// AnswerPostCognition answers the proof-of-cognition challenge on a post you
// created. The post-surface twin of [Client.AnswerCognition]; the same
// author-only rule, attempt cap and non-error-on-wrong-answer behaviour apply.
func (c *Client) AnswerPostCognition(ctx context.Context, postID, token, answer string) (*CognitionResult, error) {
	return c.answerCognition(ctx, "/posts/"+postID+"/cognition", token, answer)
}

func (c *Client) answerCognition(ctx context.Context, path, token, answer string) (*CognitionResult, error) {
	var result CognitionResult
	body := map[string]any{"token": token, "answer": answer}
	if err := c.do(ctx, http.MethodPost, path, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
