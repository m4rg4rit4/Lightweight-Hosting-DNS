package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Assigning a zone to a hosting server is purely a labelling operation: it drives
// the default filter of each panel's DNS screen and nothing else. Any panel can
// still read and edit any zone, assigned or not.

type ZoneServerReq struct {
	Domain string `json:"domain"`
	Server string `json:"server"`
}

type ZoneServerBulkReq struct {
	Domains []string `json:"domains"`
	Server  string   `json:"server"`
	// OnlyUnassigned keeps a bulk claim from stealing zones that already belong to
	// another server. The panel's "claim my zones" button relies on it.
	OnlyUnassigned bool `json:"only_unassigned"`
}

// normalizeSlug keeps slugs comparable across nodes: they are matched as plain
// strings in WHERE clauses, so casing and stray spaces would silently split one
// server into two.
func normalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (api *API) HandleZoneServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ZoneServerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.Domain == "" {
		http.Error(w, `{"success":false,"message":"Domain is required"}`, http.StatusBadRequest)
		return
	}

	scope := scopeOf(r)
	if !scope.CanManageZones() || !scope.AllowsZone(req.Domain) {
		denyScope(w)
		return
	}

	// An empty server unassigns, which is a legitimate operation.
	var slug interface{}
	if s := normalizeSlug(req.Server); s != "" {
		slug = s
	}

	res, err := api.DB.Exec("UPDATE sys_dns_zones SET server_slug = ? WHERE domain = ?", slug, req.Domain)
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// RowsAffected is also 0 when the value did not change, so check existence
		// before calling it a miss.
		var exists int
		if api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&exists) != nil {
			http.Error(w, `{"success":false,"message":"Domain not found"}`, http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Zone server updated",
		"domain":  req.Domain,
		"server":  normalizeSlug(req.Server),
	})
}

func (api *API) HandleZoneServerBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ZoneServerBulkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// Bulk claims are a panel operation over an arbitrary list of zones; a token that
	// cannot manage zones has no business in it at all.
	if !scopeOf(r).CanManageZones() {
		denyScope(w)
		return
	}

	if len(req.Domains) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Nothing to do", "updated": 0})
		return
	}

	var slug interface{}
	if s := normalizeSlug(req.Server); s != "" {
		slug = s
	}

	placeholders := make([]string, len(req.Domains))
	args := []interface{}{slug}
	for i, d := range req.Domains {
		placeholders[i] = "?"
		args = append(args, d)
	}

	query := "UPDATE sys_dns_zones SET server_slug = ? WHERE domain IN (" + strings.Join(placeholders, ",") + ")"
	if req.OnlyUnassigned {
		query += " AND (server_slug IS NULL OR server_slug = '')"
	}

	res, err := api.DB.Exec(query, args...)
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}
	updated, _ := res.RowsAffected()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Zone servers updated",
		"updated": updated,
	})
}
