package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type dnsDB interface {
	Exec(string, ...interface{}) (sql.Result, error)
	Query(string, ...interface{}) (*sql.Rows, error)
	QueryRow(string, ...interface{}) *sql.Row
}

type API struct{ DB dnsDB }

type AddRequest struct {
	Domain string `json:"domain"`
	IP     string `json:"ip"`
	// Hosting server this zone belongs to. Only a label for the panel's view
	// filter; empty means unassigned and is perfectly valid.
	Server      string   `json:"server"`
	Nameservers []string `json:"nameservers"`
}

type RecordAddReq struct {
	Domain    string `json:"domain"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	TTL       int    `json:"ttl"`
	Priority  int    `json:"priority"`
	SortOrder int    `json:"sort_order"`
}

func (api *API) HandleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AddRequest
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

	req.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Domain), "."))
	if !validZoneName(req.Domain) {
		http.Error(w, `{"message":"Invalid domain"}`, 400)
		return
	}
	nameservers, nsErr := resolveNameservers(req.Nameservers)
	if nsErr != nil {
		http.Error(w, `{"message":"Configure DNS_NAMESERVERS or provide valid nameservers"}`, 503)
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	addressType := "A"
	if req.IP != "" {
		ip := net.ParseIP(req.IP)
		if ip == nil {
			http.Error(w, `{"message":"Invalid IP address"}`, 400)
			return
		}
		if ip.To4() == nil {
			addressType = "AAAA"
		}
		req.IP = ip.String()
	}
	store := api.DB
	var tx *sql.Tx
	if db, ok := api.DB.(*sql.DB); ok {
		var err error
		tx, err = db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, `{"message":"Database unavailable"}`, 503)
			return
		}
		defer tx.Rollback()
		store = tx
	}
	var id int
	err := store.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&id)
	if err == nil {
		http.Error(w, `{"message":"Domain already exists"}`, 409)
		return
	}
	if err != sql.ErrNoRows {
		http.Error(w, `{"message":"Domain lookup failed"}`, 503)
		return
	}
	if err = insertZone(store, req, nameservers, addressType); err != nil {
		http.Error(w, `{"message":"Zone creation failed; no partial zone was saved"}`, 503)
		return
	}
	if tx != nil {
		if err = tx.Commit(); err != nil {
			http.Error(w, `{"message":"Zone commit failed"}`, 503)
			return
		}
	}
	TriggerSync()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Zone created and publication queued", "domain": req.Domain})
}

func insertZone(store dnsDB, req AddRequest, nameservers []string, addressType string) error {
	var owner interface{}
	if s := normalizeSlug(req.Server); s != "" {
		owner = s
	}
	result, err := store.Exec("INSERT INTO sys_dns_zones (domain, zone_file_path, is_active, server_slug) VALUES (?, ?, 1, ?)", req.Domain, zonesDir+"/db."+req.Domain, owner)
	if err != nil {
		return err
	}
	zoneID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, ns := range nameservers {
		if _, err = store.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'NS', ?)", zoneID, ns); err != nil {
			return err
		}
	}
	if req.IP != "" {
		if _, err = store.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', ?, ?)", zoneID, addressType, req.IP); err != nil {
			return err
		}
		if _, err = store.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, 'www', 'CNAME', ?)", zoneID, req.Domain+"."); err != nil {
			return err
		}
	}
	soa := fmt.Sprintf("%s admin.%s. ( {SERIAL} 3600 1800 604800 86400 )", nameservers[0], req.Domain)
	if _, err = store.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'SOA', ?)", zoneID, soa); err != nil {
		return err
	}
	_, err = store.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", req.Domain)
	return err
}

func (api *API) HandleRecordAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RecordAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.Domain == "" || req.Name == "" || req.Type == "" || req.Content == "" {
		http.Error(w, `{"success":false,"message":"Missing parameters"}`, http.StatusBadRequest)
		return
	}

	req.Name, req.Content = SanitizeRecord(req.Domain, req.Name, req.Type, req.Content)

	// Checked on the sanitized name, which is the form the row is stored as. Checking
	// the raw value would let a caller slip an FQDN ("_acme-challenge.example.com")
	// past a scope that only allows the single label the zone actually keeps.
	if !scopeOf(r).AllowsRecord(req.Domain, req.Name, req.Type) {
		denyScope(w)
		return
	}

	var zoneId int
	err := api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&zoneId)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Domain not found"}`, http.StatusBadRequest)
		return
	}

	ttl := req.TTL
	if ttl == 0 {
		ttl = 3600
	}

	var priority sql.NullInt32
	if req.Type == "MX" || req.Type == "SRV" {
		priority.Int32 = int32(req.Priority)
		priority.Valid = true
	}

	_, err = api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content, ttl, priority, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?)",
		zoneId, req.Name, req.Type, req.Content, ttl, priority, req.SortOrder)

	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}

	api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", req.Domain)
	TriggerSync()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Record added successfully",
	})
}

func (api *API) HandleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	scope := scopeOf(r)
	if !scope.CanManageZones() {
		denyScope(w)
		return
	}

	TriggerSync()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Check and reload initiated",
	})
}

func SanitizeRecord(domain, name, recordType, content string) (string, string) {
	if domain != "" && name != "@" {
		if name == domain || name == domain+"." {
			name = "@"
		} else if strings.HasSuffix(name, "."+domain+".") {
			name = strings.TrimSuffix(name, "."+domain+".")
		} else if strings.HasSuffix(name, "."+domain) {
			name = strings.TrimSuffix(name, "."+domain)
		}
	}
	if recordType == "TXT" {
		if strings.HasPrefix(content, "\"") && strings.HasSuffix(content, "\"") {
			content = strings.TrimSuffix(strings.TrimPrefix(content, "\""), "\"")
		}
	}
	return name, content
}
