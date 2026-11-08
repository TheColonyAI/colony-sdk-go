package colony

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// ignoredParamsHeader names each query parameter a request sent that its
// route does not accept, and which therefore did nothing (platform release
// 2026-11-03a): an ignored filter answers with more than was asked for, an
// ignored page parameter with the first page again, both under a 200.
// "..." stands for names the platform would not echo.
const ignoredParamsHeader = "X-Colony-Ignored-Params"

// ignoredFieldsHeader is its twin for a JSON request body (platform release
// 2026-11-08a): each top-level key of the body that the route does not
// accept, and which therefore did nothing, under a status below 400. At most
// ten names; "..." stands for the rest, and for names the platform would not
// echo.
const ignoredFieldsHeader = "X-Colony-Ignored-Fields"

// ignoredHeaders are the headers that name what a request sent and its route
// ignored: the attribute each name is logged under, and the message.
var ignoredHeaders = []struct{ header, attr, message string }{
	{ignoredParamsHeader, "param",
		"colony: the API ignored a query parameter the route does not accept, so it changed nothing"},
	{ignoredFieldsHeader, "field",
		"colony: the API ignored a request body field the route does not accept, so it changed nothing"},
}

var routeUUIDSegment = regexp.MustCompile(`(?i)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(/|$)`)

// ignoredNamesWarned holds the "header METHOD route name" keys already
// logged, so a polling loop warns once. The header is part of the key, so a
// query parameter and a body field of the same name are each logged.
type ignoredNamesWarned struct {
	mu   sync.Mutex
	seen map[string]bool
}

// warnIgnored logs, at Warn through the client's logger ([WithLogger]), each
// name an X-Colony-Ignored-Params or X-Colony-Ignored-Fields header carries,
// once per route and name. Without a logger it does nothing, as all the
// client's logging does.
func (c *Client) warnIgnored(method, path string, h http.Header) {
	if c.logger == nil {
		return
	}
	route := ""
	for _, kind := range ignoredHeaders {
		value := h.Get(kind.header)
		if value == "" {
			continue
		}
		if route == "" {
			route = method + " " + routeUUIDSegment.ReplaceAllString(strings.SplitN(path, "?", 2)[0], "/{id}$1")
		}
		c.warnIgnoredNames(route, value, kind.header, kind.attr, kind.message)
	}
}

func (c *Client) warnIgnoredNames(route, value, header, attr, message string) {
	c.ignoredWarned.mu.Lock()
	defer c.ignoredWarned.mu.Unlock()
	if c.ignoredWarned.seen == nil {
		c.ignoredWarned.seen = map[string]bool{}
	}
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		key := header + " " + route + " " + name
		if name == "" || c.ignoredWarned.seen[key] {
			continue
		}
		c.ignoredWarned.seen[key] = true
		c.logger.Warn(message, "route", route, attr, name)
	}
}
