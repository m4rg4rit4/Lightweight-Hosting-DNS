package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"strconv"
	"time"
)

// zoneRecordHashes returns a content fingerprint per zone id.
//
// It exists so two cluster nodes can be compared with one call each instead of a
// per-domain "deep check". updated_at cannot do that job: the engine never UPDATEs
// sys_dns_zones, so it is really the creation time, and a zone is created on dns2
// later than on dns1 — the timestamps never match even when the data is identical.
//
// SOA is excluded because its serial legitimately differs per node, and NS because
// each node advertises itself; that is the same pair the panel's sync skips, so
// equal hashes mean exactly "nothing left for a sync to reconcile". ttl and priority
// are included: comparing only name/type/content missed a changed TTL or MX priority
// forever.
func (api *API) zoneRecordHashes() map[int]string {
	rows, err := api.DB.Query(`
		SELECT zone_id, name, type, content, ttl, priority
		FROM sys_dns_records
		WHERE type NOT IN ('SOA', 'NS')`)
	if err != nil {
		log.Println("Error computing zone hashes:", err)
		return nil
	}
	defer rows.Close()

	lines := map[int][]string{}
	for rows.Next() {
		var zoneID, ttl int
		var name, rType, content string
		var prio sql.NullInt32
		if err := rows.Scan(&zoneID, &name, &rType, &content, &ttl, &prio); err != nil {
			continue
		}
		prioStr := ""
		if prio.Valid {
			prioStr = strconv.Itoa(int(prio.Int32))
		}
		lines[zoneID] = append(lines[zoneID], strings.Join([]string{
			strings.ToLower(strings.TrimSpace(name)),
			strings.ToUpper(strings.TrimSpace(rType)),
			strings.TrimSpace(content),
			strconv.Itoa(ttl),
			prioStr,
		}, "|"))
	}

	out := make(map[int]string, len(lines))
	for zoneID, l := range lines {
		sort.Strings(l) // row order must not affect the fingerprint
		sum := md5.Sum([]byte(strings.Join(l, "\n")))
		out[zoneID] = hex.EncodeToString(sum[:])
	}
	return out
}

// unassignedServerFilter is the ?server= value that selects zones with no owner.
// A plain empty value cannot mean that, because an absent parameter must keep
// returning everything for older panels.
const unassignedServerFilter = "__none__"

func (api *API) HandleGetZones(w http.ResponseWriter, r *http.Request) {
	// ?server=<slug> narrows the listing to one hosting server, ?server=__none__ to
	// the unassigned ones. That parameter is only a view filter: leaving it out still
	// returns every zone the token is allowed to see. What actually restricts the
	// listing is the token scope, added below as a separate condition.
	serverFilter := strings.TrimSpace(r.URL.Query().Get("server"))

	query := "SELECT id, domain, created_at, updated_at, server_slug FROM sys_dns_zones"
	var conds []string
	var args []interface{}
	switch serverFilter {
	case "":
		// no filter
	case unassignedServerFilter:
		conds = append(conds, "(server_slug IS NULL OR server_slug = '')")
	default:
		conds = append(conds, "server_slug = ?")
		args = append(args, serverFilter)
	}
	// A token pinned to one zone must not even enumerate the others. This is also
	// what keeps a pinned token usable for DNS-01: the certbot hook resolves the zone
	// by taking the longest suffix of this listing, so trimming it here leaves the
	// hook with exactly one candidate instead of none.
	if z := scopeOf(r).Zone; z != "" {
		conds = append(conds, "domain = ?")
		args = append(args, z)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY domain ASC"

	rows, err := api.DB.Query(query, args...)
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	hashes := api.zoneRecordHashes()

	var zones []map[string]interface{}
	for rows.Next() {
		var id int
		var domain, created, updated string
		var serverSlug sql.NullString
		rows.Scan(&id, &domain, &created, &updated, &serverSlug)
		zone := map[string]interface{}{
			"id": id,
			"domain": domain,
			"created_at": created,
			"updated_at": updated,
			"server_slug": serverSlug.String,
			// For simplicity, omitting full checkDomainDelegation here.
			"delegated": true,
		}
		if hashes != nil {
			// Fingerprint of the zone's records, comparable across cluster nodes.
			// Empty string means the zone has no reconcilable records. The key is
			// omitted entirely when the hashes could not be computed, so the panel
			// falls back to "cannot compare" instead of reading every zone as equal.
			zone["records_hash"] = hashes[id]
		}
		zones = append(zones, zone)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Zones retrieved",
		"zones": zones,
		// Slugs currently in use, so the panel can build its selector without a
		// catalogue table and without a second request.
		"servers": api.serverSlugsInUse(),
	})
}

// serverSlugsInUse lists the distinct hosting servers that own at least one zone.
func (api *API) serverSlugsInUse() []string {
	rows, err := api.DB.Query("SELECT DISTINCT server_slug FROM sys_dns_zones WHERE server_slug IS NOT NULL AND server_slug != '' ORDER BY server_slug ASC")
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	slugs := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			slugs = append(slugs, s)
		}
	}
	return slugs
}

func (api *API) HandleGetRecords(w http.ResponseWriter, r *http.Request) {
	// Extract domain from URL e.g. /api-dns/records/example.com
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, `{"success":false,"message":"Domain required"}`, http.StatusBadRequest)
		return
	}
	domain := parts[3]

	scope := scopeOf(r)
	if !scope.AllowsZone(domain) {
		denyScope(w)
		return
	}

	rows, err := api.DB.Query(`
		SELECT r.id, r.name, r.type, r.content, r.ttl, r.priority, r.sort_order 
		FROM sys_dns_records r 
		JOIN sys_dns_zones z ON r.zone_id = z.id 
		WHERE z.domain = ?
		ORDER BY r.sort_order ASC, r.id ASC`, domain)
		
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var records []map[string]interface{}
	for rows.Next() {
		var rID, rTTL, rSort int
		var rName, rType, rContent string
		var rPrio sql.NullInt32
		rows.Scan(&rID, &rName, &rType, &rContent, &rTTL, &rPrio, &rSort)
		// Filtrado aqui y no en el WHERE porque la coincidencia por nombre incluye
		// los que cuelgan por la izquierda, que en SQL seria un LIKE con el patron
		// escapado a mano. Un cliente ACME solo necesita ver su propio TXT para
		// sacar el id con el que luego lo borra.
		if !scope.AllowsRecord(domain, rName, rType) {
			continue
		}
		rec := map[string]interface{}{
			"id": rID, "name": rName, "type": rType, "content": rContent, "ttl": rTTL, "sort_order": rSort,
		}
		if rPrio.Valid {
			rec["priority"] = rPrio.Int32
		}
		records = append(records, rec)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Records retrieved",
		"domain": domain,
		"records": records,
	})
}

// HandleZoneExport serves GET /api-dns/zone/{domain}/export as plain text: the same
// BIND9 zone file the engine writes to disk. The panel renders the body verbatim.
func (api *API) HandleZoneExport(w http.ResponseWriter, r *http.Request) {
	// /api-dns/zone/{domain}/export
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-1] != "export" {
		http.Error(w, `{"success":false,"message":"Expected /api-dns/zone/{domain}/export"}`, http.StatusBadRequest)
		return
	}
	domain := parts[2]

	// The export is the whole zone file and cannot be filtered down to a scope, so a
	// token narrowed to some records inside the zone does not get it at all.
	if scope := scopeOf(r); !scope.AllowsZone(domain) || !scope.IsZoneWide() {
		denyScope(w)
		return
	}

	var zoneID int
	var zoneFilePath string
	err := api.DB.QueryRow("SELECT id, zone_file_path FROM sys_dns_zones WHERE domain = ?", domain).Scan(&zoneID, &zoneFilePath)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Domain not found"}`, http.StatusNotFound)
		return
	}

	// Show the serial BIND is actually serving; fall back to what a rebuild would use.
	serial := readZoneSerial(zoneFilePath)
	if serial == "" {
		serial = time.Now().Format("20060102") + "01"
	}

	content, err := buildZoneFile(api.DB, zoneID, domain, serial)
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(content))
}

func (api *API) HandleStatus(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, `{"success":false,"message":"Invalid path"}`, http.StatusBadRequest)
		return
	}
	idOrPending := parts[3]

	if idOrPending == "pending" {
		var count int
		api.DB.QueryRow("SELECT COUNT(*) FROM sys_dns_requests WHERE status IN ('pending', 'processing')").Scan(&count)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "message": "Pending tasks retrieved", "pending_count": count,
		})
		return
	}

	id, err := strconv.Atoi(idOrPending)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Invalid ID"}`, http.StatusBadRequest)
		return
	}

	var status, domain string
	var errorLog, reqDate, procDate sql.NullString
	err = api.DB.QueryRow("SELECT status, error_log, domain, request_date, processed_date FROM sys_dns_requests WHERE id = ?", id).Scan(&status, &errorLog, &domain, &reqDate, &procDate)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Request not found"}`, http.StatusNotFound)
		return
	}

	// Request ids are sequential, so without this a scoped token could walk the queue
	// and learn every domain hosted here. The "pending" branch above stays open: it
	// returns a bare count and names nothing.
	if !scopeOf(r).AllowsZone(domain) {
		denyScope(w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true, "message": "Status retrieved",
		"status": status, "domain": domain,
		"error_log": errorLog.String, "request_date": reqDate.String, "processed_date": procDate.String,
	})
}
