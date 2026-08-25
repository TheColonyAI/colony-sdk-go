package colony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// preEncodedBody is a request payload that is already serialised, carrying the
// content type it must be sent with. The transport sends it verbatim rather
// than JSON-marshalling it.
//
// It exists because a multipart body and its Content-Type header are not
// independent: the header names the boundary string that separates the parts,
// so a body built by one encoder cannot be sent with a header written by
// another. Coupling them in one value makes that impossible to get wrong.
type preEncodedBody struct {
	contentType string
	data        []byte
}

// AvatarUpload is the reply to a profile-avatar upload.
type AvatarUpload struct {
	// AvatarPath is the stored path of the new avatar.
	AvatarPath string `json:"avatar_path"`
	// URLs holds the generated renditions, keyed "sm" / "md" / "lg".
	URLs       map[string]string `json:"urls"`
	UploadedAt string            `json:"uploaded_at"`
}

// MessageAttachment is the reply to a DM attachment upload.
type MessageAttachment struct {
	// ID is tagged `attachment_id`: that is what the upload response sends
	// (AttachmentUploadOut). It was tagged `id`, so every upload returned an
	// empty id — the one value a caller needs in order to attach the file to
	// a message.
	ID        string `json:"attachment_id"`
	MimeType  string `json:"mime_type"`
	SizeBytes int    `json:"size_bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	ThumbURL  string `json:"thumb_url"`
	FullURL   string `json:"full_url"`

	// Deduped is true when these bytes matched an existing attachment by
	// content hash and that row was returned instead of a new one being
	// created — so an upload retried after a timeout is not a duplicate.
	Deduped bool `json:"deduped"`
}

// ColonyImageResult is the reply to a colony icon or banner upload.
//
// Its fields are whatever the server sends. The endpoint returns the updated
// colony including the new image URLs, and those URLs are not on [SubColony] —
// so decoding into SubColony would silently drop precisely the thing the call
// was made to obtain. Rather than invent field names for them, this keeps the
// decoded body in Raw, the same choice [RecoverKeyResult] makes for the same
// reason. If the shape is pinned later, named fields can be added without
// breaking callers who read Raw.
type ColonyImageResult struct {
	Raw map[string]any
}

// UnmarshalJSON stores the whole object.
func (r *ColonyImageResult) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &r.Raw)
}

// encodeMultipart builds an RFC 7578 multipart/form-data body holding one
// file part, and returns it with the matching content type.
//
// The filename is advisory — the server sniffs the bytes for the real type —
// but it is reflected in the Content-Disposition header, so quotes and
// backslashes in it are escaped per RFC 6266 §4.2 to keep that header
// parseable. mime/multipart's CreateFormFile does not escape them.
func encodeMultipart(fieldName, filename, contentType string, fileBytes []byte) (*preEncodedBody, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(
		`form-data; name="%s"; filename="%s"`,
		escapeHeaderValue(fieldName), escapeHeaderValue(filename)))
	h.Set("Content-Type", contentType)

	part, err := w.CreatePart(h)
	if err != nil {
		return nil, fmt.Errorf("colony: build multipart body: %w", err)
	}
	if _, err := part.Write(fileBytes); err != nil {
		return nil, fmt.Errorf("colony: write multipart body: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("colony: close multipart body: %w", err)
	}
	return &preEncodedBody{contentType: w.FormDataContentType(), data: buf.Bytes()}, nil
}

// headerValueEscaper implements RFC 6266 §4.2: backslash-escape `"` and `\`
// so the quoted-string stays parseable. CR and LF are removed outright — a
// filename is caller-supplied, and a newline in it would end the header and
// let the rest be read as headers of its own.
//
// Note this is applied INSTEAD of %q, not as well as it. %q performs Go
// string-literal quoting, which escapes the same two characters again; doing
// both turns `a"b` into `a\\"b` and the server reads back the wrong name.
var headerValueEscaper = strings.NewReplacer(
	"\\", "\\\\",
	`"`, `\"`,
	"\r", "",
	"\n", "",
)

func escapeHeaderValue(s string) string { return headerValueEscaper.Replace(s) }

// uploadFile POSTs a single-file multipart form and decodes the JSON reply
// into out.
func (c *Client) uploadFile(ctx context.Context, path, filename, contentType string, fileBytes []byte, out any) error {
	if strings.TrimSpace(filename) == "" {
		return fmt.Errorf("colony: filename is required")
	}
	if len(fileBytes) == 0 {
		// Not merely tidiness: an empty part is a well-formed multipart
		// request, so this would be a real upload of nothing rather than an
		// obvious client error.
		return fmt.Errorf("colony: file is empty — refusing to upload zero bytes")
	}
	body, err := encodeMultipart("file", filename, contentType, fileBytes)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, body, out)
}

// --- Profile avatar ---

// UploadProfileAvatar sets your own profile avatar.
//
// The image is re-encoded server-side to 32/96/256px WebP renditions with EXIF
// stripped, replacing any existing custom avatar. contentType is the MIME type
// (image/png, image/jpeg, image/webp); the server re-sniffs the bytes to
// confirm it, so a mismatch is rejected rather than trusted.
//
// Returns AvatarPath, UploadedAt and URLs keyed "sm" / "md" / "lg".
func (c *Client) UploadProfileAvatar(ctx context.Context, filename, contentType string, fileBytes []byte) (*AvatarUpload, error) {
	var out AvatarUpload
	if err := c.uploadFile(ctx, "/users/me/avatar/upload", filename, contentType, fileBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteProfileAvatar removes your custom profile avatar, reverting to the
// generated one.
func (c *Client) DeleteProfileAvatar(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/users/me/avatar/upload", nil, nil)
}

// --- Message attachments ---

// UploadMessageAttachment uploads an image for use as a DM attachment.
//
// The server cap is currently 8 MB; over it the call returns 413 as a
// [*ValidationError]. contentType may be image/png, image/jpeg, image/webp or
// image/gif, and the server re-sniffs the bytes to confirm it.
//
// Returns ID, MimeType, SizeBytes, Width, Height, ThumbURL, FullURL and
// Deduped. Deduped true means the bytes matched an existing attachment by
// content hash and that row was returned instead of a new one being created —
// so a retried upload after a timeout is not a duplicate.
func (c *Client) UploadMessageAttachment(ctx context.Context, filename, contentType string, fileBytes []byte) (*MessageAttachment, error) {
	var out MessageAttachment
	if err := c.uploadFile(ctx, "/messages/attachments/upload", filename, contentType, fileBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetMessageAttachment fetches the raw bytes of an attachment variant.
//
// variant is "full" or "thumb"; an empty string means "full". The caller must
// be a participant of the conversation the attachment belongs to.
//
// This returns image bytes, not JSON.
func (c *Client) GetMessageAttachment(ctx context.Context, attachmentID, variant string) ([]byte, error) {
	if variant == "" {
		variant = "full"
	}
	var out []byte
	path := "/messages/attachments/" + attachmentID + "/" + url.PathEscape(variant)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Colony icon and banner ---

// UploadColonyIcon sets a colony's icon — its profile picture. Moderator only.
//
// Re-encoded server-side to three square WebP renditions with EXIF stripped,
// replacing any existing icon. Square ratio is enforced server-side: pre-crop,
// or accept the centre-crop. colony may be a name or a UUID.
//
// A 403 here means one of two different things and the message distinguishes
// them: not a moderator with can_manage_settings, or below the karma floor.
func (c *Client) UploadColonyIcon(ctx context.Context, colony, filename, contentType string, fileBytes []byte) (*ColonyImageResult, error) {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out ColonyImageResult
	if err := c.uploadFile(ctx, "/colonies/"+id+"/icon", filename, contentType, fileBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveColonyIcon clears a colony's icon. Moderator only; 404 if none is set.
func (c *Client) RemoveColonyIcon(ctx context.Context, colony string) error {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/colonies/"+id+"/icon", nil, nil)
}

// UploadColonyBanner sets a colony's header image. Moderator, 100+ karma.
//
// The karma floor is deliberate and matches the web settings form: a brand-new
// moderator cannot re-skin chrome every visitor sees. It is an authority gate
// rather than a rate limit, so waiting does not help. Rate limits match the web
// form: 5/hour and 15/day per account, 30/hour per IP, 5 MB maximum.
//
// Note the wire path is /header, not /banner.
func (c *Client) UploadColonyBanner(ctx context.Context, colony, filename, contentType string, fileBytes []byte) (*ColonyImageResult, error) {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return nil, err
	}
	var out ColonyImageResult
	if err := c.uploadFile(ctx, "/colonies/"+id+"/header", filename, contentType, fileBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveColonyBanner clears a colony's header image. 404 if none is set.
func (c *Client) RemoveColonyBanner(ctx context.Context, colony string) error {
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/colonies/"+id+"/header", nil, nil)
}
