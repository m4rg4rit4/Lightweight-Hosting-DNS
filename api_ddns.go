package main

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

func (api *API) HandleDdnsRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractBearer(r)
	if token == "" {
		http.Error(w, `{"success":false,"message":"Missing token"}`, http.StatusBadRequest)
		return
	}

	var input map[string]string
	json.NewDecoder(r.Body).Decode(&input)
	hostname := input["hostname"]
	if hostname == "" {
		hostname = "Unknown-Device"
	}

	var id int
	err := api.DB.QueryRow("SELECT id FROM sys_ddns_clients WHERE token = ?", token).Scan(&id)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Token already registered"})
		return
	}

	api.DB.Exec("INSERT INTO sys_ddns_clients (token, hostname, status) VALUES (?, ?, 'pending')", token, hostname)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Device registered"})
}

func (api *API) HandleDdnsStatus(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		http.Error(w, `{"success":false,"message":"Missing token"}`, http.StatusBadRequest)
		return
	}

	var status string
	err := api.DB.QueryRow("SELECT status FROM sys_ddns_clients WHERE token = ?", token).Scan(&status)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Device not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "status": status})
}

func (api *API) HandleDdnsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractBearer(r)
	if token == "" {
		http.Error(w, `{"success":false,"message":"Missing token"}`, http.StatusUnauthorized)
		return
	}

	var cID, cZoneId int
	var cStatus, cRecordName, cLastIp, cDomain string
	err := api.DB.QueryRow(`SELECT c.id, c.status, c.zone_id, c.record_name, c.last_ip, z.domain 
		FROM sys_ddns_clients c LEFT JOIN sys_dns_zones z ON c.zone_id = z.id WHERE c.token = ?`, token).
		Scan(&cID, &cStatus, &cZoneId, &cRecordName, &cLastIp, &cDomain)
	
	if err != nil || cStatus != "approved" {
		http.Error(w, `{"success":false,"message":"Unauthorized or pending"}`, http.StatusUnauthorized)
		return
	}

	if cZoneId == 0 || cRecordName == "" {
		http.Error(w, `{"success":false,"message":"Approved but no record assigned"}`, http.StatusBadRequest)
		return
	}

	ip := clientIP(r)

	api.DB.Exec("UPDATE sys_ddns_clients SET last_seen = NOW() WHERE id = ?", cID)

	if cLastIp == ip {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "ip": ip, "updated": false, "message": "IP has not changed"})
		return
	}

	var rID int
	err = api.DB.QueryRow("SELECT id FROM sys_dns_records WHERE zone_id = ? AND name = ? AND type = 'A'", cZoneId, cRecordName).Scan(&rID)
	if err == nil {
		api.DB.Exec("UPDATE sys_dns_records SET content = ?, ttl = 60 WHERE id = ?", ip, rID)
	} else {
		api.DB.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content, ttl) VALUES (?, ?, 'A', ?, 60)", cZoneId, cRecordName, ip)
	}

	api.DB.Exec("UPDATE sys_ddns_clients SET last_ip = ? WHERE id = ?", ip, cID)
	api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", cDomain)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "ip": ip, "updated": true, "message": "IP updated successfully"})
}

func (api *API) HandleDdnsList(w http.ResponseWriter, r *http.Request) {
	rows, err := api.DB.Query("SELECT id, token, hostname, zone_id, record_name, status, last_ip, last_seen, created_at FROM sys_ddns_clients ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, `{"success":false,"message":"DB error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var clients []map[string]interface{}
	for rows.Next() {
		var id int
		var token, hostname, status, created string
		var zoneId sql.NullInt64
		var recordName, lastIp, lastSeen sql.NullString
		
		rows.Scan(&id, &token, &hostname, &zoneId, &recordName, &status, &lastIp, &lastSeen, &created)
		client := map[string]interface{}{
			"id": id, "token": token, "hostname": hostname, "status": status, "created_at": created,
		}
		if zoneId.Valid { client["zone_id"] = zoneId.Int64 }
		if recordName.Valid { client["record_name"] = recordName.String }
		if lastIp.Valid { client["last_ip"] = lastIp.String }
		if lastSeen.Valid { client["last_seen"] = lastSeen.String }
		clients = append(clients, client)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "clients": clients})
}

type ApproveReq struct {
	ID         int    `json:"id"`
	ZoneID     int    `json:"zone_id"`
	RecordName string `json:"record_name"`
}

func (api *API) HandleDdnsApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ApproveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.ID == 0 || req.ZoneID == 0 || req.RecordName == "" {
		http.Error(w, `{"success":false,"message":"Missing parameters"}`, http.StatusBadRequest)
		return
	}

	_, err := api.DB.Exec("UPDATE sys_ddns_clients SET status = 'approved', zone_id = ?, record_name = ? WHERE id = ?", req.ZoneID, req.RecordName, req.ID)
	if err != nil {
		http.Error(w, `{"success":false,"message":"Update failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Client approved"})
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

// clientIP determina la IP de origen para el registro DDNS.
//
// El registro A del cliente se escribe con esta IP, así que falsificarla permite
// apuntar ese registro a una dirección ajena. X-Forwarded-For lo pone el cliente y es
// falsificable, salvo cuando la petición entra por el proxy local de confianza (Apache
// en loopback): sólo entonces se hace caso, y se toma la ÚLTIMA entrada de la cadena
// —la que añade el proxy con la IP real—, no la primera, que el cliente puede rellenar.
// Si el binario se alcanza directamente (sin proxy), se usa la IP de la conexión y se
// ignora cualquier XFF.
func clientIP(r *http.Request) string {
	ip := stripPort(r.RemoteAddr)
	if isLoopback(ip) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			ip = stripPort(strings.TrimSpace(parts[len(parts)-1]))
		}
	}
	return ip
}

func stripPort(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}
