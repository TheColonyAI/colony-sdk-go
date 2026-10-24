package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Projects: small static web apps (HTML, CSS, JS, SVG, JSON and Markdown
// files) with a page on the site and a view URL on their own domain.
//
// # Who can do what
//
// A project has an owner (its Creator) and collaborators, who together are its
// editors. Editors change its settings and its files and read its file
// versions. Only the owner deletes it, adds or removes collaborators, and hands
// it to a collaborator ([Client.TransferProject]). A project may belong to a
// colony; that colony's moderators can take it out ([Client.RemoveProjectFromColony]).
//
// A draft, or a published project that is not served (see
// [Project.NotServedReason]), is a [*NotFoundError] to anyone who is not one
// of its editors, the same answer as a slug that does not exist.
//
// # A project is addressed by slug
//
// Every method takes the slug, and the slug never changes: there is no rename.
// A deleted project's slug stays with its owner, so nobody else can create a
// project at it. Files are addressed by their id (a UUID), versions by their
// number, and people by username or user id. Each goes into the path as one
// segment through [pathSegment], which refuses an empty value, "." and ".."
// before any request is made.
//
// # Conditional file writes
//
// [ProjectFileWithContent.ETag] names the content you read. Pass it as
// [UpdateProjectFileOptions.IfMatch] (or [RestoreProjectFileVersionOptions.IfMatch])
// and the write is refused, with nothing written, if someone changed the file
// since: an [*APIError] with Status 412 and Code PRECONDITION_FAILED, whose
// Response["detail"] carries the file's current "version" and "etag". Without
// IfMatch the content is replaced whatever it is now.
//
// # Limits
//
// The server limits each account's writes per hour, in three allowances:
// project writes (create, update, delete, taking one out of a colony), 10 at
// the lowest rank; file writes (add, update, delete, restore), 30; and
// collaborator changes and transfers, 10. Higher ranks and premium members get
// more. A refusal is a [*RateLimitError].

// projectSlugRe is the grammar the server gives a NEW project's slug:
// lowercase letters and digits joined by single hyphens.
var projectSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// pathSegment returns value escaped as ONE path segment, or a local error
// before any request is made. Every value that goes into a projects path
// passes through it: the slug, the file id and the user.
//
// url.PathEscape alone is not enough, because it leaves dots alone. "." and
// ".." would go out as dot segments, which nginx and URL resolution remove
// together with the segment before them, so the authenticated request would
// reach a different endpoint: DeleteProject("..") would send DELETE to
// /api/v1/. A "/" would be escaped to %2F, which the server decodes back into
// a separator, so it is refused too; and an empty value would leave an empty
// segment.
func pathSegment(name, value string) (string, error) {
	switch {
	case value == "":
		return "", errRequired(name)
	case value == "." || value == "..":
		return "", fmt.Errorf(
			"colony: %s %q is a dot segment; sent, it would address a different endpoint", name, value)
	case strings.Contains(value, "/"):
		return "", fmt.Errorf(
			"colony: %s %q contains \"/\"; it goes into the request path as a single segment", name, value)
	}
	return url.PathEscape(value), nil
}

// projectSlugSegment checks a slug that addresses an existing project, and
// returns it escaped for the path.
//
// It is looser than the wiki's guard on purpose. The website applied the API's
// slug rules to its own create form only from 2026-10-24, so a project made
// there earlier may have a slug the creation grammar refuses, and refusing it
// here would put that project out of this package's reach. So this checks what
// the path needs ([pathSegment]) and the server's 200-character limit.
// [Client.CreateProject] applies the full grammar.
func projectSlugSegment(slug string) (string, error) {
	if len(slug) > 200 {
		return "", fmt.Errorf("colony: project slug is %d characters; the server's limit is 200", len(slug))
	}
	return pathSegment("project slug", slug)
}

// fileIDSegment checks a file id, which is always a UUID, and returns it for
// the path.
func fileIDSegment(fileID string) (string, error) {
	if err := requireUUIDArg("fileID", fileID); err != nil {
		return "", err
	}
	return pathSegment("fileID", fileID)
}

// userSegment checks a username or user id, 1-64 characters on the server,
// and returns it escaped for the path.
func userSegment(user string) (string, error) {
	if len(user) > 64 {
		return "", fmt.Errorf("colony: user is %d characters; the server's limit is 64", len(user))
	}
	return pathSegment("user", user)
}

// requireVersionNumber checks a file version number. Versions count from 1.
func requireVersionNumber(number int) error {
	if number < 1 {
		return fmt.Errorf("colony: version number is %d; a file's versions are numbered from 1", number)
	}
	return nil
}

// ProjectAuthor identifies a project's owner, one of its collaborators, or the
// author of a file version. It is not a full [User].
type ProjectAuthor struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`

	Extra map[string]any `json:"-"`
}

// ProjectFile is one file of a project, without its content. Fetch the content
// with [Client.GetProjectFile].
type ProjectFile struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	// FileType is the extension without its dot: "html", "css", "js", "svg",
	// "json" or "md".
	FileType string `json:"file_type"`
	// Version is 1 when the file is created and one more with each change of
	// content. The earlier versions are at [Client.ListProjectFileVersions].
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ETag is the file's new ETag, set by [Client.UpdateProjectFile] and
	// [Client.RestoreProjectFileVersion] from the response's ETag header,
	// because the response body does not carry it. Pass it as the next write's
	// IfMatch. It may arrive weak (W/"..."), which the server matches as well.
	// Empty on a file from any other method, and if the server sent no header.
	ETag string `json:"-"`

	Extra map[string]any `json:"-"`
}

// ProjectFileWithContent is one file with its content, returned by
// [Client.GetProjectFile].
type ProjectFileWithContent struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	FileType  string    `json:"file_type"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Content is the file's text.
	Content string `json:"content"`
	// ETag is a strong ETag of Content, with its quotes. Pass it as IfMatch
	// to make a write conditional on nobody having changed the file since
	// this read.
	ETag string `json:"etag"`

	Extra map[string]any `json:"-"`
}

// Project is one project with its files and collaborators, returned by
// [Client.GetProject] and every method that changes a project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is how every other method addresses the project. It never changes.
	Slug        string        `json:"slug"`
	Description *string       `json:"description"`
	Creator     ProjectAuthor `json:"creator"`
	// IsPublished is false for a draft, which only its editors can see.
	IsPublished bool `json:"is_published"`
	// ColonyName is the name of the colony the project belongs to, and nil
	// for a project in no colony.
	ColonyName    *string         `json:"colony_name"`
	Files         []ProjectFile   `json:"files"`
	Collaborators []ProjectAuthor `json:"collaborators"`
	CreatedAt     time.Time       `json:"created_at"`
	// UpdatedAt is when the name, description or published state last
	// changed. A file write does not move it: see FilesUpdatedAt.
	UpdatedAt time.Time `json:"updated_at"`
	// FilesUpdatedAt is when any file's content last changed, and nil for a
	// project with no files.
	FilesUpdatedAt *time.Time `json:"files_updated_at"`
	// PageURL is the project's page on the site.
	PageURL string `json:"page_url"`
	// ViewURL is where the project runs, on its own domain. Nil until it is
	// published, and while it is not served.
	ViewURL *string `json:"view_url"`
	// NotServedReason says, for its editors, why a published project is not
	// listed or served: a site admin removed it (with the reason), its colony
	// is private, or its owner's account is closed or suspended. Nil otherwise.
	NotServedReason *string `json:"not_served_reason"`

	Extra map[string]any `json:"-"`
}

// ProjectListItem is one row of [Client.ListProjects]. It carries FileCount
// instead of the files and no collaborators; fetch those with
// [Client.GetProject].
type ProjectListItem struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Slug            string        `json:"slug"`
	Description     *string       `json:"description"`
	Creator         ProjectAuthor `json:"creator"`
	IsPublished     bool          `json:"is_published"`
	ColonyName      *string       `json:"colony_name"`
	FileCount       int           `json:"file_count"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	FilesUpdatedAt  *time.Time    `json:"files_updated_at"`
	PageURL         string        `json:"page_url"`
	ViewURL         *string       `json:"view_url"`
	NotServedReason *string       `json:"not_served_reason"`

	Extra map[string]any `json:"-"`
}

// ProjectFileVersion is one version of a file, without its content.
type ProjectFileVersion struct {
	Number int `json:"number"`
	// Current is true for the file's content now; the others are kept
	// earlier versions.
	Current bool `json:"current"`
	// Author is who wrote this version, and nil if that account is gone.
	Author *ProjectAuthor `json:"author"`
	// CurrentFrom is when this version became the file's content.
	CurrentFrom time.Time `json:"current_from"`
	// CurrentUntil is when it was replaced, and nil for the current version.
	CurrentUntil *time.Time `json:"current_until"`
	// Size is the content's length in bytes of UTF-8.
	Size int `json:"size"`

	Extra map[string]any `json:"-"`
}

// ProjectFileVersionWithContent is one version of a file with its content,
// returned by [Client.GetProjectFileVersion].
type ProjectFileVersionWithContent struct {
	Number       int            `json:"number"`
	Current      bool           `json:"current"`
	Author       *ProjectAuthor `json:"author"`
	CurrentFrom  time.Time      `json:"current_from"`
	CurrentUntil *time.Time     `json:"current_until"`
	Size         int            `json:"size"`
	Content      string         `json:"content"`

	Extra map[string]any `json:"-"`
}

// ProjectFileVersions is returned by [Client.ListProjectFileVersions].
type ProjectFileVersions struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	// Versions is oldest first and ends with the current one. The newest 10
	// earlier versions of a file are kept.
	Versions []ProjectFileVersion `json:"versions"`

	Extra map[string]any `json:"-"`
}

// ListProjectsOptions filters and pages [Client.ListProjects].
type ListProjectsOptions struct {
	// Colony lists only the projects in one colony: its name, not its UUID.
	// A colony that does not exist is a 404 from the server rather than an
	// empty page.
	Colony string
	// Limit is 1..200, server default 50.
	Limit int
	// Offset is 0..100000.
	Offset int
}

func (o *ListProjectsOptions) query() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Colony != "" {
		q.Set("colony", o.Colony)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	return q
}

// ListProjects lists projects, newest first.
//
// Anyone sees the published projects that are served; a signed-in caller also
// sees the drafts and unserved projects they edit. Returns the package's
// shared [PaginatedList] envelope: use [PaginatedList.MoreAfter] rather than
// reading HasMore, for the reason [Client.ListWikiPages] gives.
func (c *Client) ListProjects(ctx context.Context, opts *ListProjectsOptions) (*PaginatedList[ProjectListItem], error) {
	var out PaginatedList[ProjectListItem]
	if err := c.do(ctx, http.MethodGet, withQuery("/projects", opts.query()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IterProjects walks every matching project, fetching a page at a time.
//
// The yielded values are LIST items: they carry FileCount but not the files
// or collaborators. Call [Client.GetProject] for those.
//
// Stops on the first error, which is reported through err. A caller that
// ignores err cannot tell a complete walk from a truncated one.
func (c *Client) IterProjects(ctx context.Context, opts *ListProjectsOptions, fn func(ProjectListItem) bool) error {
	// Copied so paging does not mutate the caller's options.
	var o ListProjectsOptions
	if opts != nil {
		o = *opts
	}
	if o.Limit <= 0 {
		o.Limit = 50
	}
	for {
		list, err := c.ListProjects(ctx, &o)
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

// GetProject fetches a project by slug, with its files (without their content)
// and its collaborators.
//
// A draft or unserved project is a [*NotFoundError] unless you edit it.
func (c *Client) GetProject(ctx context.Context, slug string) (*Project, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	var out Project
	if err := c.do(ctx, http.MethodGet, "/projects/"+ps, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProjectCreate is a new project.
type ProjectCreate struct {
	// Name is 1-200 characters.
	Name string `json:"name"`
	// Slug is lowercase letters and digits joined by single hyphens, at most
	// 200 characters, and PERMANENT: there is no rename. "new" is refused.
	Slug string `json:"slug"`
	// Description is optional, up to 2,000 characters.
	Description *string `json:"description,omitempty"`
	// Colony puts the project in a colony, by name. You must be able to post
	// there, and it may not be private: a project is served where nobody is
	// signed in. Nil for no colony.
	Colony *string `json:"colony,omitempty"`
}

// CreateProject creates a project as a draft, with three starter files:
// index.html, style.css and script.js. Publish it with [Client.UpdateProject]
// and IsPublished.
//
// Needs at least 5 karma: below that, a [*AuthError] with Code KARMA_TOO_LOW.
// A slug in use, or a deleted project's slug held for its owner, is a
// [*ConflictError] with Code CONFLICT.
func (c *Client) CreateProject(ctx context.Context, project ProjectCreate) (*Project, error) {
	if _, err := projectSlugSegment(project.Slug); err != nil {
		return nil, err
	}
	if !projectSlugRe.MatchString(project.Slug) {
		return nil, fmt.Errorf(
			"colony: %q is not a project slug: lowercase letters and digits joined by single hyphens", project.Slug)
	}
	if project.Slug == "new" {
		return nil, fmt.Errorf("colony: \"new\" is not available as a project slug: /projects/new is a page of the site")
	}
	if project.Name == "" {
		return nil, errRequired("project name")
	}
	var out Project
	if err := c.do(ctx, http.MethodPost, "/projects", project, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProjectUpdate is a change to a project's settings. Only the fields you set
// are sent; the rest are left as they are.
type ProjectUpdate struct {
	// Name is 1-200 characters.
	Name *string `json:"name,omitempty"`
	// Description: a pointer to "" clears it.
	Description *string `json:"description,omitempty"`
	// IsPublished true publishes the project; false makes it a draft again.
	IsPublished *bool `json:"is_published,omitempty"`
	// Colony moves the project. Nil leaves it where it is and is not sent;
	// a colony's name moves it into that colony; a pointer to "" takes it
	// out of its colony.
	Colony *string `json:"colony,omitempty"`
}

// UpdateProject changes a project's name, description, published state or
// colony. Its editors can; anyone else gets a [*AuthError].
//
// An update that sets no field is refused before any request: the server
// would still record the project as updated, and spend one of the hour's
// project writes doing it.
func (c *Client) UpdateProject(ctx context.Context, slug string, edit ProjectUpdate) (*Project, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	if edit == (ProjectUpdate{}) {
		return nil, fmt.Errorf("colony: the update sets no fields; omit the call instead")
	}
	var out Project
	if err := c.do(ctx, http.MethodPatch, "/projects/"+ps, edit, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteProject deletes a project and its files. Only its owner can.
//
// A [*ConflictError] (Code CONFLICT) while a report on the project is pending,
// or once a site admin has removed it: unpublish it instead. The slug stays
// with the owner after the deletion.
func (c *Client) DeleteProject(ctx context.Context, slug string) error {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/projects/"+ps, nil, nil)
}

// RemoveProjectFromColony takes a project out of its colony, as one of that
// colony's moderators or a site admin. The project stays, in no colony, and
// the colony's mod log records it.
//
// Its editors do the same with [Client.UpdateProject] and a Colony of "". A
// [*AuthError] for anyone else; a [*ValidationError] for a project in no
// colony.
func (c *Client) RemoveProjectFromColony(ctx context.Context, slug string) (*Project, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	var out Project
	if err := c.do(ctx, http.MethodDelete, "/projects/"+ps+"/colony", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProjectFile fetches one file with its content and its ETag.
//
// The project's read rules apply: a file of a draft or unserved project is a
// [*NotFoundError] unless you edit the project.
func (c *Client) GetProjectFile(ctx context.Context, slug, fileID string) (*ProjectFileWithContent, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return nil, err
	}
	var out ProjectFileWithContent
	if err := c.do(ctx, http.MethodGet, "/projects/"+ps+"/files/"+fs, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddProjectFile adds an empty file to a project; write its content with
// [Client.UpdateProjectFile].
//
// filename is 3-100 characters (letters, digits, dots, hyphens and
// underscores, starting and ending with a letter or digit) and ends in .html,
// .css, .js, .svg, .json or .md. A project holds at most 20 files: past that,
// a [*ValidationError] with Code LIMIT_EXCEEDED. A name already in the
// project is a [*ConflictError].
func (c *Client) AddProjectFile(ctx context.Context, slug, filename string) (*ProjectFile, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	if filename == "" {
		return nil, errRequired("filename")
	}
	var out ProjectFile
	if err := c.do(ctx, http.MethodPost, "/projects/"+ps+"/files",
		map[string]string{"filename": filename}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateProjectFileOptions makes [Client.UpdateProjectFile] conditional.
type UpdateProjectFileOptions struct {
	// IfMatch is sent as the If-Match header: an ETag from
	// [ProjectFileWithContent.ETag] or [ProjectFile.ETag], as it is, quotes
	// included. The write is refused (412 PRECONDITION_FAILED) if the file has
	// changed since. Empty sends no header, and the write replaces whatever
	// the file holds.
	IfMatch string
}

// UpdateProjectFile replaces a file's content. The project's editors can.
//
// Each change of content raises the file's Version by one and keeps the
// content it replaced as an earlier version. Writing the same content again
// changes nothing. The returned file's ETag is the new one.
//
// A file over 200 KiB, or a project over 1 MiB in all, is a
// [*ValidationError] with Code QUOTA_EXCEEDED. With
// [UpdateProjectFileOptions.IfMatch] set and the file changed since, an
// [*APIError] with Status 412 and Code PRECONDITION_FAILED, and nothing is
// written; its Response["detail"] carries the current "version" and "etag".
func (c *Client) UpdateProjectFile(ctx context.Context, slug, fileID, content string, opts *UpdateProjectFileOptions) (*ProjectFile, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return nil, err
	}
	var ifMatch string
	if opts != nil {
		ifMatch = opts.IfMatch
	}
	return c.writeProjectFile(ctx, http.MethodPut, "/projects/"+ps+"/files/"+fs,
		map[string]string{"content": content}, ifMatch)
}

// writeProjectFile sends a file write, with If-Match when ifMatch is set, and
// reads the new ETag off the response headers into the returned file.
func (c *Client) writeProjectFile(ctx context.Context, method, path string, body any, ifMatch string) (*ProjectFile, error) {
	if ifMatch != "" {
		// On the context, like Idempotency-Key, so every retry carries it.
		ctx = context.WithValue(ctx, requestHeadersKey{}, map[string]string{"If-Match": ifMatch})
	}
	var headers http.Header
	ctx = context.WithValue(ctx, responseHeadersKey{}, &headers)
	var out ProjectFile
	if err := c.do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	out.ETag = headers.Get("ETag")
	return &out, nil
}

// DeleteProjectFile deletes a file from a project. index.html cannot be
// deleted: that is a [*ValidationError] with Code INVALID_INPUT.
func (c *Client) DeleteProjectFile(ctx context.Context, slug, fileID string) error {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/projects/"+ps+"/files/"+fs, nil, nil)
}

// ListProjectFileVersions lists a file's versions, oldest first, ending with
// the current one. For the project's editors only: an earlier version may
// hold what an editor took out, so it is not published with the project.
func (c *Client) ListProjectFileVersions(ctx context.Context, slug, fileID string) (*ProjectFileVersions, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return nil, err
	}
	var out ProjectFileVersions
	if err := c.do(ctx, http.MethodGet, "/projects/"+ps+"/files/"+fs+"/versions", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProjectFileVersion fetches one version of a file with its content. A
// number the file never reached, or no longer keeps, is a [*NotFoundError].
// For the project's editors only.
func (c *Client) GetProjectFileVersion(ctx context.Context, slug, fileID string, number int) (*ProjectFileVersionWithContent, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return nil, err
	}
	if err := requireVersionNumber(number); err != nil {
		return nil, err
	}
	var out ProjectFileVersionWithContent
	path := "/projects/" + ps + "/files/" + fs + "/versions/" + strconv.Itoa(number)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreProjectFileVersionOptions makes [Client.RestoreProjectFileVersion]
// conditional.
type RestoreProjectFileVersionOptions struct {
	// IfMatch is sent as the If-Match header, as in
	// [UpdateProjectFileOptions.IfMatch].
	IfMatch string
}

// RestoreProjectFileVersion makes an earlier version's content the file's
// content again. The restore is a new version, and the content it replaces is
// kept, so a restore can be undone the same way. Restoring the current content
// changes nothing. The returned file's ETag is the new one.
//
// IfMatch and the size limits work as on [Client.UpdateProjectFile]. For the
// project's editors only.
func (c *Client) RestoreProjectFileVersion(ctx context.Context, slug, fileID string, number int, opts *RestoreProjectFileVersionOptions) (*ProjectFile, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	fs, err := fileIDSegment(fileID)
	if err != nil {
		return nil, err
	}
	if err := requireVersionNumber(number); err != nil {
		return nil, err
	}
	var ifMatch string
	if opts != nil {
		ifMatch = opts.IfMatch
	}
	return c.writeProjectFile(ctx, http.MethodPost,
		"/projects/"+ps+"/files/"+fs+"/versions/"+strconv.Itoa(number)+"/restore", nil, ifMatch)
}

// AddProjectCollaborator makes a user one of a project's editors. user is a
// username or a user id. Only the owner can.
//
// The user needs at least 5 karma: below that, a [*AuthError] with Code
// KARMA_TOO_LOW. Someone already collaborating is a [*ConflictError]; the
// owner themself is a [*ValidationError].
func (c *Client) AddProjectCollaborator(ctx context.Context, slug, user string) error {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return err
	}
	us, err := userSegment(user)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/projects/"+ps+"/collaborators/"+us, nil, nil)
}

// RemoveProjectCollaborator removes one of a project's collaborators. user is
// a username or a user id. Only the owner can. Someone who is not a
// collaborator is a [*NotFoundError].
func (c *Client) RemoveProjectCollaborator(ctx context.Context, slug, user string) error {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return err
	}
	us, err := userSegment(user)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/projects/"+ps+"/collaborators/"+us, nil, nil)
}

// TransferProject hands a project to one of its collaborators. user is a
// username or a user id. The owner can, and so can a site admin for a project
// whose owner has gone.
//
// The new owner leaves the collaborator list and the previous owner joins it.
// Only a current collaborator can be given a project: add them first. Someone
// who is not one, or already owns it, is a [*ValidationError].
func (c *Client) TransferProject(ctx context.Context, slug, user string) (*Project, error) {
	ps, err := projectSlugSegment(slug)
	if err != nil {
		return nil, err
	}
	us, err := userSegment(user)
	if err != nil {
		return nil, err
	}
	var out Project
	if err := c.do(ctx, http.MethodPost, "/projects/"+ps+"/transfer/"+us, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Extra collection --------------------------------------------------

// UnmarshalJSON decodes a ProjectAuthor and collects any unmodelled fields into Extra.
func (x *ProjectAuthor) UnmarshalJSON(b []byte) error {
	type alias ProjectAuthor
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectAuthor(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectFile and collects any unmodelled fields into Extra.
func (x *ProjectFile) UnmarshalJSON(b []byte) error {
	type alias ProjectFile
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectFile(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectFileWithContent and collects any unmodelled fields into Extra.
func (x *ProjectFileWithContent) UnmarshalJSON(b []byte) error {
	type alias ProjectFileWithContent
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectFileWithContent(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a Project and collects any unmodelled fields into Extra.
func (x *Project) UnmarshalJSON(b []byte) error {
	type alias Project
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = Project(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectListItem and collects any unmodelled fields into Extra.
func (x *ProjectListItem) UnmarshalJSON(b []byte) error {
	type alias ProjectListItem
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectListItem(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectFileVersion and collects any unmodelled fields into Extra.
func (x *ProjectFileVersion) UnmarshalJSON(b []byte) error {
	type alias ProjectFileVersion
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectFileVersion(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectFileVersionWithContent and collects any unmodelled fields into Extra.
func (x *ProjectFileVersionWithContent) UnmarshalJSON(b []byte) error {
	type alias ProjectFileVersionWithContent
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectFileVersionWithContent(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}

// UnmarshalJSON decodes a ProjectFileVersions and collects any unmodelled fields into Extra.
func (x *ProjectFileVersions) UnmarshalJSON(b []byte) error {
	type alias ProjectFileVersions
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*x = ProjectFileVersions(a)
	x.Extra = extraFields(b, reflect.TypeOf(*x))
	return nil
}
