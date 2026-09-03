package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /api-dns/zone/ is a subtree pattern (it serves the zone export) and
// /api-dns/zone/server lives inside it. Getting that precedence wrong silently
// breaks either the export button or the server assignment, and neither failure is
// obvious from the panel. Assert which pattern each path resolves to.
//
// mux.Handler only looks the route up, it never invokes the handler, so no database
// is needed here.
func TestRouteResolution(t *testing.T) {
	api := &API{} // no DB: handlers are never called
	mux := http.NewServeMux()
	api.registerRoutes(mux)

	cases := []struct {
		path        string
		wantPattern string
	}{
		{"/api-dns/zone/server", "/api-dns/zone/server"},
		{"/api-dns/zone/server/bulk", "/api-dns/zone/server/bulk"},
		{"/api-dns/zone/example.com/export", "/api-dns/zone/"},
		// A zone actually named "server" must still export, not hit the assignment
		// endpoint.
		{"/api-dns/zone/server/export", "/api-dns/zone/"},
		{"/api-dns/zones", "/api-dns/zones"},
		{"/api-dns/records/example.com", "/api-dns/records/"},
		{"/api-dns/status/pending", "/api-dns/status/"},
		{"/api-dns/record/add", "/api-dns/record/add"},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		_, pattern := mux.Handler(req)
		if pattern != c.wantPattern {
			t.Errorf("%s resolved to pattern %q, want %q", c.path, pattern, c.wantPattern)
		}
	}
}

// Every endpoint the hosting panel calls must exist. /api-dns/zone/{d}/export and
// /api-dns/query/ were both being called by the panel without being registered:
// export silently returned nothing and the query caller was dead code. Keep the
// export covered here so it cannot regress.
func TestPanelEndpointsAreRegistered(t *testing.T) {
	api := &API{}
	mux := http.NewServeMux()
	api.registerRoutes(mux)

	paths := []string{
		"/api-dns/add",
		"/api-dns/zones",
		"/api-dns/records/example.com",
		"/api-dns/record/add",
		"/api-dns/record/edit",
		"/api-dns/record/del",
		"/api-dns/status/pending",
		"/api-dns/zone/example.com/export",
		"/api-dns/zone/server",
		"/api-dns/zone/server/bulk",
	}

	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not routed to any handler", p)
		}
	}
}
