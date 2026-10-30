package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// Invoked only inside HandleJob's transaction: a full repair is one atomic
// mutation, including NS, SOA, TTL and priority, followed by one publication.
func (api *API) handleZoneSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain             string         `json:"domain"`
		Server             string         `json:"server"`
		AssignmentRevision uint64         `json:"assignment_revision"`
		Records            []RecordAddReq `json:"records"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Domain == "" || len(req.Records) == 0 {
		http.Error(w, "Invalid zone snapshot", 400)
		return
	}
	scope := scopeOf(r)
	if !scope.CanManageZones() || !scope.IsZoneWide() || !scope.AllowsZone(req.Domain) {
		denyScope(w)
		return
	}
	soa, ns := 0, 0
	for _, rec := range req.Records {
		if rec.Name == "" || rec.Type == "" || rec.Content == "" {
			http.Error(w, "Invalid record", 400)
			return
		}
		if rec.Type == "SOA" {
			soa++
		}
		if rec.Type == "NS" {
			ns++
		}
	}
	if soa != 1 || ns == 0 {
		http.Error(w, "Snapshot requires one SOA and nameservers", 400)
		return
	}
	var zoneID int64
	err := api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&zoneID)
	if err == sql.ErrNoRows {
		result, e := api.DB.Exec("INSERT INTO sys_dns_zones (domain, zone_file_path, is_active, server_slug) VALUES (?, ?, 1, ?)", req.Domain, zonesDir+"/db."+req.Domain, normalizeSlug(req.Server))
		if e != nil {
			http.Error(w, "Zone creation failed", 503)
			return
		}
		zoneID, err = result.LastInsertId()
	}
	if err != nil {
		http.Error(w, "Zone lookup failed", 503)
		return
	}
	if _, err = api.DB.Exec("DELETE FROM sys_dns_records WHERE zone_id = ?", zoneID); err != nil {
		http.Error(w, "Record replacement failed", 503)
		return
	}
	for _, rec := range req.Records {
		name, content := SanitizeRecord(req.Domain, rec.Name, rec.Type, rec.Content)
		ttl := rec.TTL
		if ttl == 0 {
			ttl = 3600
		}
		var priority sql.NullInt32
		if rec.Type == "MX" || rec.Type == "SRV" {
			priority = sql.NullInt32{Int32: int32(rec.Priority), Valid: true}
		}
		if _, err = api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content, ttl, priority, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?)", zoneID, name, rec.Type, content, ttl, priority, rec.SortOrder); err != nil {
			http.Error(w, "Record insertion failed", 503)
			return
		}
	}
	if _, err = api.DB.Exec("UPDATE sys_dns_zones SET server_slug = ?, assignment_revision = ? WHERE id = ? AND assignment_revision <= ?", normalizeSlug(req.Server), req.AssignmentRevision, zoneID, req.AssignmentRevision); err != nil {
		http.Error(w, "Owner update failed", 503)
		return
	}
	if _, err = api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", req.Domain); err != nil {
		http.Error(w, "Publication queue failed", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
