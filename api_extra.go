package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

type DeleteRequest struct {
	Domain string `json:"domain"`
}

func (api *API) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.Domain == "" {
		http.Error(w, `{"success":false,"message":"Missing domain"}`, http.StatusBadRequest)
		return
	}

	scope := scopeOf(r)
	if !scope.CanManageZones() || !scope.AllowsZone(req.Domain) {
		denyScope(w)
		return
	}

	var id int
	err := api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&id)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Domain not found"}`, http.StatusNotFound)
		return
	}

	api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('delete', ?, 'pending')", req.Domain)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Delete request queued",
	})
}

type RecordEditReq struct {
	ID         int    `json:"id"`
	Domain     string `json:"domain"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl"`
	Priority   int    `json:"priority"`
	// Pointer so an absent field is distinguishable from an explicit 0: a plain edit
	// does not send sort_order, and writing 0 there would wipe the manual ordering of
	// the record every time someone edits it. Only /reorder sends it.
	SortOrder  *int   `json:"sort_order"`
	OldName    string `json:"old_name"`
	OldType    string `json:"old_type"`
	OldContent string `json:"old_content"`
}

// findRecordID resolves a record by its natural key (domain + name + type + content).
//
// Record IDs are NOT portable across cluster nodes: every node has its own
// AUTO_INCREMENT, so the same logical record has a different id on each node.
// That is why the panel sends id=0 and lets each node resolve locally.
//
// content is what makes the match unambiguous. A zone routinely holds several
// records sharing name and type (SPF + DKIM + verification TXT on '@', several MX,
// round-robin A). Matching on name+type alone picks an arbitrary row, and each node
// can pick a different one. When content is given we never fall back to the loose
// match: returning 0 (and a "not found") is far better than touching another record.
func (api *API) findRecordID(domain, name, recordType, content string) int {
	var id int
	if content != "" {
		api.DB.QueryRow(`
			SELECT r.id FROM sys_dns_records r
			JOIN sys_dns_zones z ON r.zone_id = z.id
			WHERE z.domain = ? AND r.name = ? AND r.type = ? AND r.content = ?
			ORDER BY r.id ASC LIMIT 1`, domain, name, recordType, content).Scan(&id)
		return id
	}

	// Legacy callers that do not send the content yet.
	api.DB.QueryRow(`
		SELECT r.id FROM sys_dns_records r
		JOIN sys_dns_zones z ON r.zone_id = z.id
		WHERE z.domain = ? AND r.name = ? AND r.type = ?
		ORDER BY r.id ASC LIMIT 1`, domain, name, recordType).Scan(&id)
	return id
}

func (api *API) HandleRecordEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RecordEditReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.ID == 0 && req.Domain != "" && req.OldName != "" && req.OldType != "" {
		// Sanitize the old values the same way they were sanitized on insert,
		// otherwise the content comparison never matches (quoted TXT, FQDN names).
		oldName, oldContent := SanitizeRecord(req.Domain, req.OldName, req.OldType, req.OldContent)
		req.ID = api.findRecordID(req.Domain, oldName, req.OldType, oldContent)
	}

	if req.Name == "" || req.Type == "" || req.Content == "" {
		http.Error(w, `{"success":false,"message":"Missing parameters"}`, http.StatusBadRequest)
		return
	}
	if req.ID == 0 {
		// The natural-key lookup above found nothing. Never guess: refusing is better
		// than editing a record the caller did not mean.
		http.Error(w, `{"success":false,"message":"Record not found"}`, http.StatusNotFound)
		return
	}

	// We need domain to sanitize properly if we didn't get it. name and type come
	// along for the scope check below: the request only carries the new values.
	var domain, curName, curType string
	err := api.DB.QueryRow("SELECT z.domain, r.name, r.type FROM sys_dns_records r JOIN sys_dns_zones z ON r.zone_id = z.id WHERE r.id = ?", req.ID).Scan(&domain, &curName, &curType)

	if err != nil {
		http.Error(w, `{"success":false,"message":"Record not found"}`, http.StatusNotFound)
		return
	}

	// Guard against an id from another node: ids are per-node, so a caller that sends
	// an explicit id may be pointing at a completely different zone here.
	if req.Domain != "" && req.Domain != domain {
		http.Error(w, `{"success":false,"message":"Record does not belong to the requested domain"}`, http.StatusConflict)
		return
	}

	req.Name, req.Content = SanitizeRecord(domain, req.Name, req.Type, req.Content)

	// Both the current record and the one it would become have to be in scope.
	// Checking only the target would let a token restricted to TXT under
	// _acme-challenge rewrite any record in the zone into its own scope; checking
	// only the current one would let it turn its own TXT into an A on the apex.
	scope := scopeOf(r)
	if !scope.AllowsRecord(domain, curName, curType) || !scope.AllowsRecord(domain, req.Name, req.Type) {
		denyScope(w)
		return
	}

	ttl := req.TTL
	if ttl == 0 { ttl = 3600 }
	var priority sql.NullInt32
	if req.Type == "MX" || req.Type == "SRV" {
		priority.Int32 = int32(req.Priority)
		priority.Valid = true
	}

	if req.SortOrder != nil {
		api.DB.Exec("UPDATE sys_dns_records SET name=?, type=?, content=?, ttl=?, priority=?, sort_order=? WHERE id=?",
			req.Name, req.Type, req.Content, ttl, priority, *req.SortOrder, req.ID)
	} else {
		api.DB.Exec("UPDATE sys_dns_records SET name=?, type=?, content=?, ttl=?, priority=? WHERE id=?",
			req.Name, req.Type, req.Content, ttl, priority, req.ID)
	}


	api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", domain)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Record updated"})
}

type RecordDelReq struct {
	ID      int    `json:"id"`
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func (api *API) HandleRecordDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RecordDelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.ID == 0 && req.Domain != "" && req.Name != "" && req.Type != "" {
		name, content := SanitizeRecord(req.Domain, req.Name, req.Type, req.Content)
		req.ID = api.findRecordID(req.Domain, name, req.Type, content)
	}

	if req.ID == 0 {
		http.Error(w, `{"success":false,"message":"Record not found"}`, http.StatusNotFound)
		return
	}

	var domain, curName, curType string
	if err := api.DB.QueryRow("SELECT z.domain, r.name, r.type FROM sys_dns_records r JOIN sys_dns_zones z ON r.zone_id = z.id WHERE r.id = ?", req.ID).Scan(&domain, &curName, &curType); err != nil {
		http.Error(w, `{"success":false,"message":"Record not found"}`, http.StatusNotFound)
		return
	}

	// Same guard as in the edit path: never delete across zones on an id we were handed.
	if req.Domain != "" && req.Domain != domain {
		http.Error(w, `{"success":false,"message":"Record does not belong to the requested domain"}`, http.StatusConflict)
		return
	}

	// Resolved from the row, never from the request: an id is enough to reach a
	// record whose name and type the caller never had to name.
	if !scopeOf(r).AllowsRecord(domain, curName, curType) {
		denyScope(w)
		return
	}

	api.DB.Exec("DELETE FROM sys_dns_records WHERE id = ?", req.ID)


	if domain != "" {
		api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", domain)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Record deleted"})
}
