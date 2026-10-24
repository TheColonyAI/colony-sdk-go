package colony

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Every value a caller passes that ends up in a request path goes through
// [pathSegment], here, before any request is made. That is ids, usernames,
// slugs, tags, emoji, conversation ids and vault filenames alike.
//
// url.PathEscape alone is not enough, because it leaves dots alone. "." and
// ".." would go out as dot segments, which nginx and URL resolution remove
// together with the segment before them, so the authenticated request would
// reach a different endpoint: DeletePost("..") would send DELETE /posts/..,
// which arrives as DELETE /api/v1/. Interpolating a value raw is worse, since
// "../users/me" or a "/" inside an id leaves its segment entirely. An agent
// that passes on a value it read from content (a post id, a username, an org
// slug) could be steered that way.
//
// The guard refuses only what can never name one resource: an empty value,
// "." and "..", and a "/" (except inside a vault filename, see
// [vaultFilePath]). For a UUID, a username or a slug the escaping is the
// identity, so nothing that worked before changes on the wire. Values that
// already had a stricter check (a UUID, a wiki or org slug) keep it, through
// [uuidSegment], [wikiSlugSegment] and [orgSlugSegment].
//
// TestEveryPathValueIsGuarded reads every request call in the package and
// fails on a path built from a value that did not come through here, and
// TestPathGuardRefusesBeforeAnyRequest calls every method that builds one.

// pathSegment returns value escaped as ONE path segment, or a local error
// before any request is made. name is the parameter, for the error.
//
// A "/" is refused rather than escaped: %2F is decoded back into a separator
// on the way to the server, so the value would still leave its segment.
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

// uuidSegment checks a value that must be a UUID ([requireUUIDArg]) and
// returns it for the path.
func uuidSegment(name, value string) (string, error) {
	if err := requireUUIDArg(name, value); err != nil {
		return "", err
	}
	return pathSegment(name, value)
}

// wikiSlugSegment checks a wiki slug ([requireWikiSlug]) and returns it for
// the path.
func wikiSlugSegment(slug string) (string, error) {
	if err := requireWikiSlug(slug); err != nil {
		return "", err
	}
	return pathSegment("wiki slug", slug)
}

// orgSlugSegment checks an org slug ([requireOrgSlug]) and returns it for the
// path.
func orgSlugSegment(slug string) (string, error) {
	if err := requireOrgSlug(slug); err != nil {
		return "", err
	}
	return pathSegment("org slug", slug)
}

// colonySegment resolves a colony name or UUID ([Client.resolveColonyUUID])
// and returns the UUID for the path.
//
// The value is checked BEFORE it is resolved, because resolving a name the
// package does not know lists the colonies over the network, and a refused
// value must not cost a request.
func (c *Client) colonySegment(ctx context.Context, colony string) (string, error) {
	if _, err := pathSegment("colony", colony); err != nil {
		return "", err
	}
	id, err := c.resolveColonyUUID(ctx, colony)
	if err != nil {
		return "", err
	}
	return pathSegment("colony", id)
}

// vaultFilePath builds the path for a vault filename.
//
// The vault has folders and its route takes the rest of the path
// ({filename:path}), so a filename may contain "/": "notes/2026/aug.md". Each
// "/"-separated part goes through [pathSegment] on its own, so no part may be
// empty, "." or "..": "notes/../x", "/notes", "notes/" and "a//b" are refused.
//
// The parts are joined with %2F, which is exactly what url.PathEscape made of
// the whole name before. The Python client sends the "/" unescaped instead;
// checked against the live API on 2026-08-25, the server resolves BOTH forms
// to the same file.
func vaultFilePath(filename string) (string, error) {
	if filename == "" {
		return "", errRequired("filename")
	}
	parts := strings.Split(filename, "/")
	for i, part := range parts {
		seg, err := pathSegment("vault filename part", part)
		if err != nil {
			return "", fmt.Errorf(
				"colony: vault filename %q has the part %q; a filename may name folders with \"/\", "+
					"but no part of it may be empty, \".\" or \"..\", which would address a different endpoint",
				filename, part)
		}
		parts[i] = seg
	}
	return "/vault/files/" + strings.Join(parts, "%2F"), nil
}
