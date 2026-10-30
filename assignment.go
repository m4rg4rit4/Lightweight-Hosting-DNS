package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
)

var serverSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type assignmentRequest struct {
	Domain           string  `json:"domain"`
	Server           string  `json:"server"`
	ExpectedRevision *uint64 `json:"expected_revision"`
	Revision         uint64  `json:"revision"`
}

// Both handlers run exclusively inside the job transaction. Hosting sends all
// decisions to its first configured DNS, then replicates that decision's revision.
// This prevents two independent Hosting databases from choosing different winners.
func (api *API) changeAssignment(w http.ResponseWriter, r *http.Request, replica bool) {
	var req assignmentRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Domain == "" {
		http.Error(w, `{"message":"Invalid assignment"}`, 400)
		return
	}
	req.Server = normalizeSlug(req.Server)
	if req.Server != "" && !serverSlugPattern.MatchString(req.Server) {
		http.Error(w, `{"message":"Invalid server"}`, 400)
		return
	}
	if !scopeOf(r).CanManageZones() || !scopeOf(r).AllowsZone(req.Domain) {
		denyScope(w)
		return
	}
	var owner sql.NullString
	var revision uint64
	err := api.DB.QueryRow("SELECT server_slug, assignment_revision FROM sys_dns_zones WHERE domain = ? FOR UPDATE", req.Domain).Scan(&owner, &revision)
	if err == sql.ErrNoRows {
		http.Error(w, `{"message":"Domain not found"}`, 404)
		return
	}
	if err != nil {
		http.Error(w, `{"message":"Assignment lookup failed"}`, 503)
		return
	}
	if replica {
		// A delayed decision must never undo a newer transfer or release.
		if req.Revision < revision {
			assignmentResponse(w, req.Domain, owner.String, revision)
			return
		}
		if req.Revision == revision {
			if req.Server != normalizeSlug(owner.String) {
				http.Error(w, `{"message":"Conflicting assignment revision"}`, 409)
				return
			}
			assignmentResponse(w, req.Domain, owner.String, revision)
			return
		}
	} else {
		if req.ExpectedRevision == nil {
			http.Error(w, `{"message":"Expected assignment revision required"}`, 400)
			return
		}
		if *req.ExpectedRevision != revision {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			json.NewEncoder(w).Encode(map[string]interface{}{"message": "La asignación ha cambiado. Actualiza la página antes de volver a intentarlo.", "server": owner.String, "revision": revision})
			return
		}
		req.Revision = revision + 1
	}
	var value interface{}
	if req.Server != "" {
		value = req.Server
	}
	if _, err = api.DB.Exec("UPDATE sys_dns_zones SET server_slug = ?, assignment_revision = ? WHERE domain = ?", value, req.Revision, req.Domain); err != nil {
		http.Error(w, `{"message":"Assignment update failed"}`, 503)
		return
	}
	assignmentResponse(w, req.Domain, req.Server, req.Revision)
}
func assignmentResponse(w http.ResponseWriter, domain, server string, revision uint64) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "domain": domain, "server": server, "revision": revision})
}
func (api *API) handleAssignmentDecision(w http.ResponseWriter, r *http.Request) {
	api.changeAssignment(w, r, false)
}
func (api *API) handleAssignmentReplica(w http.ResponseWriter, r *http.Request) {
	api.changeAssignment(w, r, true)
}

func (api *API) HandleGetAssignment(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "Domain required", 400)
		return
	}
	if !scopeOf(r).AllowsZone(domain) {
		denyScope(w)
		return
	}
	var owner sql.NullString
	var revision uint64
	if err := api.DB.QueryRow("SELECT server_slug, assignment_revision FROM sys_dns_zones WHERE domain = ?", domain).Scan(&owner, &revision); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Domain not found", 404)
		} else {
			http.Error(w, "Assignment lookup failed", 503)
		}
		return
	}
	assignmentResponse(w, domain, owner.String, revision)
}
