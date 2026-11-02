package colony

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// ignoredParamsHeader names each query parameter a request sent that its
// route does not accept, and which therefore did nothing (platform,
// 2026-11-02): an ignored filter answers with more than was asked for, an
// ignored page parameter with the first page again, both under a 200.
// "..." stands for names the platform would not echo.
const ignoredParamsHeader = "X-Colony-Ignored-Params"

var routeUUIDSegment = regexp.MustCompile(`(?i)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(/|$)`)

// ignoredParamsWarned holds the "METHOD route name" triples already logged,
// so a polling loop warns once.
type ignoredParamsWarned struct {
	mu   sync.Mutex
	seen map[string]bool
}

// warnIgnoredParams logs, at Warn through the client's logger
// ([WithLogger]), each name an X-Colony-Ignored-Params header carries, once
// per route and name. Without a logger it does nothing, as all the client's
// logging does.
func (c *Client) warnIgnoredParams(method, path string, h http.Header) {
	value := h.Get(ignoredParamsHeader)
	if value == "" || c.logger == nil {
		return
	}
	route := method + " " + routeUUIDSegment.ReplaceAllString(strings.SplitN(path, "?", 2)[0], "/{id}$1")
	c.ignoredWarned.mu.Lock()
	defer c.ignoredWarned.mu.Unlock()
	if c.ignoredWarned.seen == nil {
		c.ignoredWarned.seen = map[string]bool{}
	}
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		key := route + " " + name
		if name == "" || c.ignoredWarned.seen[key] {
			continue
		}
		c.ignoredWarned.seen[key] = true
		c.logger.Warn("colony: the API ignored a query parameter the route does not accept, so it changed nothing",
			"route", route, "param", name)
	}
}
