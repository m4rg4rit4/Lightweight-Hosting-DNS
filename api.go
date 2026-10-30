package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

	// Check if exists
	nameservers, nsErr := resolveNameservers(req.Nameservers)
	if nsErr != nil {
		http.Error(w, `{"message":"Configure DNS_NAMESERVERS or provide valid nameservers"}`, http.StatusServiceUnavailable)
		return
	}
	var id int
	err := api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", req.Domain).Scan(&id)
	if err == nil {
		http.Error(w, `{"success":false,"message":"Domain already exists"}`, http.StatusBadRequest)
		return
	}

	zoneFile := "/etc/bind/zones/db." + req.Domain

	var serverSlug interface{}
	if s := strings.TrimSpace(req.Server); s != "" {
		serverSlug = s
	}

	res, err := api.DB.Exec("INSERT INTO sys_dns_zones (domain, zone_file_path, is_active, server_slug) VALUES (?, ?, 1, ?)", req.Domain, zoneFile, serverSlug)
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB Error"}`, http.StatusInternalServerError)
		return
	}
	zoneId, _ := res.LastInsertId()

	adminEmail := "admin." + req.Domain

	// Insert base records: valid authoritative nameservers
	for _, nameserver := range nameservers {
		api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'NS', ?)", zoneId, nameserver)
	}

	if req.IP != "" {
		api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'A', ?)", zoneId, req.IP)
		api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, 'www', 'CNAME', ?)", zoneId, "@")
	}

	soaContent := fmt.Sprintf("%s %s. ( {SERIAL} 3600 1800 604800 86400 )", nameservers[0], adminEmail)
	api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'SOA', ?)", zoneId, soaContent)

	// Queue update and trigger immediate sync
	api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", req.Domain)
	TriggerSync()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Domain added successfully",
		"domain":  req.Domain,
	})
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
