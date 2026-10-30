package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// Existing handlers check some SQL errors themselves. Capture all Exec errors
// here as well so a partially failed mutation can never acquire a receipt.
type jobDB struct {
	*sql.Tx
	failure   error
	requestID sql.NullInt64
}

func (db *jobDB) Exec(query string, args ...interface{}) (sql.Result, error) {
	res, err := db.Tx.Exec(query, args...)
	if err != nil {
		db.failure = err
		return res, err
	}
	if strings.HasPrefix(query, "INSERT INTO sys_dns_requests ") {
		id, e := res.LastInsertId()
		if e != nil {
			db.failure = e
		} else {
			db.requestID = sql.NullInt64{Int64: id, Valid: true}
		}
	}
	return res, err
}

// The operation and its receipt commit together. A lost HTTP response can be
// retried with the same key without executing the mutation twice.
func (api *API) HandleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var job struct {
		Key      string          `json:"key"`
		Endpoint string          `json:"endpoint"`
		Payload  json.RawMessage `json:"payload"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&job) != nil || len(job.Key) != 64 {
		http.Error(w, "Invalid job", 400)
		return
	}
	db, ok := api.DB.(*sql.DB)
	if !ok {
		http.Error(w, "Database unavailable", 503)
		return
	}
	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Database unavailable", 503)
		return
	}
	defer tx.Rollback()
	digest := sha256.Sum256(append([]byte(job.Endpoint+"\n"), job.Payload...))
	hash := hex.EncodeToString(digest[:])
	// The key survives credential rotation. A replay only reads publication
	// state; creation still runs the original handler's full scope checks.
	var target struct {
		Domain  string   `json:"domain"`
		Domains []string `json:"domains"`
	}
	if json.Unmarshal(job.Payload, &target) != nil {
		http.Error(w, "Invalid payload", 400)
		return
	}
	if target.Domain != "" && !scopeOf(r).AllowsZone(target.Domain) {
		denyScope(w)
		return
	}
	for _, domain := range target.Domains {
		if !scopeOf(r).AllowsZone(domain) {
			denyScope(w)
			return
		}
	}
	key := job.Key
	if _, err = tx.Exec("INSERT IGNORE INTO sys_dns_jobs (job_key, payload_hash) VALUES (?, ?)", key, hash); err != nil {
		http.Error(w, "Job storage unavailable", 503)
		return
	}
	var storedHash string
	var requestID sql.NullInt64
	var applied bool
	var result sql.NullString
	if err = tx.QueryRow("SELECT payload_hash, request_id, applied, result_json FROM sys_dns_jobs WHERE job_key = ? FOR UPDATE", key).Scan(&storedHash, &requestID, &applied, &result); err != nil {
		http.Error(w, "Job lookup failed", 503)
		return
	}
	if storedHash != hash {
		http.Error(w, "Job key reused with different payload", 409)
		return
	}
	if !applied {
		mutation := &jobDB{Tx: tx}
		worker := &API{DB: mutation}
		handlers := map[string]http.HandlerFunc{
			"/api-dns/zone/assignment/decide": worker.handleAssignmentDecision,
			"/api-dns/zone/assignment/apply":  worker.handleAssignmentReplica,
			"/api-dns/zone/sync":              worker.handleZoneSync,
			"/api-dns/add":                    worker.HandleAdd, "/api-dns/delete": worker.HandleDelete,
			"/api-dns/record/add": worker.HandleRecordAdd, "/api-dns/record/edit": worker.HandleRecordEdit,
			"/api-dns/record/del": worker.handleRecordDeleteJob, "/api-dns/zone/server": worker.HandleZoneServer,
			"/api-dns/zone/server/bulk": worker.HandleZoneServerBulk,
		}
		handler := handlers[job.Endpoint]
		if handler == nil {
			http.Error(w, "Unsupported job endpoint", 400)
			return
		}
		req := r.Clone(r.Context())
		req.URL.Path = job.Endpoint
		req.Body = http.NoBody
		req.Body = ioBody(job.Payload)
		response := httptest.NewRecorder()
		handler(response, req)
		if response.Code < 200 || response.Code >= 300 {
			w.WriteHeader(response.Code)
			w.Write(response.Body.Bytes())
			return
		}
		if mutation.failure != nil {
			http.Error(w, "Mutation failed; transaction rolled back", 503)
			return
		}
		requestID = mutation.requestID
		if !requestID.Valid && !strings.HasPrefix(job.Endpoint, "/api-dns/zone/assignment/") && job.Endpoint != "/api-dns/record/del" && job.Endpoint != "/api-dns/zone/server" && job.Endpoint != "/api-dns/zone/server/bulk" {
			http.Error(w, "Mutation has no publication request", 503)
			return
		}
		result = sql.NullString{String: response.Body.String(), Valid: true}
		if _, err = tx.Exec("UPDATE sys_dns_jobs SET applied = 1, request_id = ?, result_json = ? WHERE job_key = ?", requestID, result.String, key); err != nil {
			http.Error(w, "Job receipt failed", 503)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Job commit failed", 503)
		return
	}
	TriggerSync()
	status := "completed"
	var detail sql.NullString
	if requestID.Valid {
		deadline := time.Now()
		if r.URL.Query().Get("wait") == "1" {
			deadline = deadline.Add(3 * time.Second)
		}
		for {
			if err = db.QueryRow("SELECT status, error_log FROM sys_dns_requests WHERE id = ?", requestID.Int64).Scan(&status, &detail); err != nil {
				http.Error(w, "Publication status unavailable", 503)
				return
			}
			if status == "completed" || status == "error" || time.Now().After(deadline) {
				break
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	// Durable Hosting jobs keep retrying publication, including after the
	// engine's short retry window has expired. Never reapply the mutation.
	if status == "error" {
		_, err = db.Exec("UPDATE sys_dns_requests SET status = 'pending', attempts = 0 WHERE id = ? AND status = 'error' AND processed_date < NOW() - INTERVAL 5 MINUTE", requestID.Int64)
		if err != nil {
			http.Error(w, "Publication retry failed", 503)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if status != "completed" {
		w.WriteHeader(http.StatusAccepted)
	}
	var decoded interface{}
	if result.Valid {
		json.Unmarshal([]byte(result.String), &decoded)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "status": status, "error": detail.String, "result": decoded})
}

type byteBody struct{ *bytes.Reader }

func (byteBody) Close() error  { return nil }
func ioBody(b []byte) byteBody { return byteBody{bytes.NewReader(b)} }

// Deleting an already absent natural key is safe, including a TXT cleanup on a
// node that never received the challenge. Explicit local IDs keep strict errors.
func (api *API) handleRecordDeleteJob(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Invalid deletion", 400)
		return
	}
	req := r.Clone(r.Context())
	req.Body = ioBody(body)
	response := httptest.NewRecorder()
	api.HandleRecordDel(response, req)
	var deletion RecordDelReq
	if response.Code == 404 && json.Unmarshal(body, &deletion) == nil && deletion.ID == 0 && deletion.Domain != "" && deletion.Name != "" && deletion.Type != "" && deletion.Content != "" {
		name, _ := SanitizeRecord(deletion.Domain, deletion.Name, deletion.Type, deletion.Content)
		if !scopeOf(r).AllowsRecord(deletion.Domain, name, deletion.Type) {
			denyScope(w)
			return
		}
		var zoneID int
		err = api.DB.QueryRow("SELECT id FROM sys_dns_zones WHERE domain = ?", deletion.Domain).Scan(&zoneID)
		if err != nil && err != sql.ErrNoRows {
			http.Error(w, "Zone lookup failed", 503)
			return
		}
		if err == nil {
			if _, err = api.DB.Exec("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')", deletion.Domain); err != nil {
				http.Error(w, "Publication queue failed", 503)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
		return
	}
	w.WriteHeader(response.Code)
	w.Write(response.Body.Bytes())
}
