package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Transactional driver double to exercise receipts and rollback through the real
// HTTP handler. No MySQL or running BIND instance is needed.
type jobState struct {
	hash           string
	applied        bool
	mutations      int64
	status         string
	fail           bool
	owner          string
	revision       int64
	result         string
	newZone        bool
	failRecordType string
	recordTypes    string
	soaContent     string
	wwwTarget      string
}
type jobConnector struct {
	state *jobState
	mu    *sync.Mutex
}

func (c jobConnector) Connect(context.Context) (driver.Conn, error) {
	return &jobConn{state: c.state, mu: c.mu}, nil
}
func (c jobConnector) Driver() driver.Driver { return jobDriver{} }

type jobDriver struct{}
type jobResult struct{}

func (jobResult) LastInsertId() (int64, error) { return 1, nil }
func (jobResult) RowsAffected() (int64, error) { return 1, nil }

func (jobDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type jobConn struct {
	state   *jobState
	working *jobState
	mu      *sync.Mutex
}

func (c *jobConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (c *jobConn) Close() error                        { return nil }
func (c *jobConn) Begin() (driver.Tx, error) {
	if c.mu != nil {
		c.mu.Lock()
	}
	copy := *c.state
	c.working = &copy
	return c, nil
}
func (c *jobConn) Commit() error {
	*c.state = *c.working
	c.working = nil
	if c.mu != nil {
		c.mu.Unlock()
	}
	return nil
}
func (c *jobConn) Rollback() error {
	c.working = nil
	if c.mu != nil {
		c.mu.Unlock()
	}
	return nil
}
func (c *jobConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	s := c.working
	if s == nil {
		s = c.state
	}
	switch {
	case strings.HasPrefix(q, "INSERT IGNORE INTO sys_dns_jobs"):
		if s.hash == "" {
			s.hash = args[1].Value.(string)
		}
	case strings.HasPrefix(q, "INSERT INTO sys_dns_records"):
		kind := ""
		for _, candidate := range []string{"NS", "SOA", "CNAME"} {
			if strings.Contains(q, "'"+candidate+"'") {
				kind = candidate
			}
		}
		if strings.Contains(q, "VALUES (?, '@', ?, ?)") {
			kind = args[1].Value.(string)
		}
		if kind == s.failRecordType && kind != "" {
			return nil, errors.New("simulated record failure")
		}
		if kind != "" {
			s.recordTypes += kind + ","
		}
		if kind == "SOA" {
			s.soaContent = args[1].Value.(string)
		}
		if kind == "CNAME" {
			s.wwwTarget = args[1].Value.(string)
		}
		s.mutations++
	case strings.HasPrefix(q, "INSERT INTO sys_dns_zones"):
		s.newZone = false
	case strings.HasPrefix(q, "DELETE FROM sys_dns_records"):
		s.mutations = 0
	case strings.HasPrefix(q, "UPDATE sys_dns_zones"):
		if strings.Contains(q, "assignment_revision = ?") {
			s.owner, _ = args[0].Value.(string)
			s.revision = args[1].Value.(int64)
		}
	case strings.HasPrefix(q, "INSERT INTO sys_dns_requests"):
		if s.fail {
			return nil, errors.New("simulated publication insert failure")
		}
		s.status = "pending"
	case strings.HasPrefix(q, "UPDATE sys_dns_jobs"):
		s.applied = true
		s.result = args[1].Value.(string)
	default:
		return nil, errors.New("unexpected SQL: " + q)
	}
	return jobResult{}, nil
}

type jobRows struct {
	values []driver.Value
	done   bool
}

func (r *jobRows) Columns() []string { result := make([]string, len(r.values)); return result }
func (r *jobRows) Close() error      { return nil }
func (r *jobRows) Next(out []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(out, r.values)
	r.done = true
	return nil
}
func (c *jobConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	s := c.working
	if s == nil {
		s = c.state
	}
	var values []driver.Value
	switch {
	case strings.Contains(q, "FROM sys_dns_jobs"):
		var id driver.Value
		if s.applied && s.status != "" {
			id = int64(1)
		}
		values = []driver.Value{s.hash, id, s.applied, s.result}
	case strings.Contains(q, "FROM sys_dns_zones"):
		if s.newZone {
			return &jobRows{values: []driver.Value{int64(1)}, done: true}, nil
		}
		if strings.Contains(q, "SELECT server_slug, assignment_revision") {
			values = []driver.Value{s.owner, s.revision}
		} else {
			values = []driver.Value{int64(1)}
		}
	case strings.Contains(q, "FROM sys_dns_requests"):
		values = []driver.Value{s.status, nil}
	default:
		return nil, errors.New("unexpected query: " + q)
	}
	return &jobRows{values: values}, nil
}

func TestJobRetryAndPublication(t *testing.T) {
	state := &jobState{}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	api := &API{DB: db}
	payload := `{"key":"` + strings.Repeat("a", 64) + `","endpoint":"/api-dns/record/add","payload":{"domain":"example.com","name":"@","type":"A","content":"192.0.2.1"}}`
	call := func(body string) int {
		req := httptest.NewRequest("POST", "/api-dns/jobs", strings.NewReader(body))
		rec := httptest.NewRecorder()
		api.HandleJob(rec, req)
		return rec.Code
	}
	if code := call(payload); code != 202 {
		t.Fatalf("first request = %d", code)
	}
	if code := call(payload); code != 202 {
		t.Fatalf("retry before publication = %d", code)
	}
	if state.mutations != 1 {
		t.Fatalf("retry duplicated mutation: %d", state.mutations)
	}
	state.status = "completed"
	if code := call(payload); code != 200 {
		t.Fatalf("published receipt = %d", code)
	}
	if code := call(strings.Replace(payload, "192.0.2.1", "192.0.2.2", 1)); code != 409 {
		t.Fatalf("key reuse = %d", code)
	}
	if state.mutations != 1 {
		t.Fatal("receipt re-executed mutation")
	}
}

func TestJobRollsBackIgnoredHandlerError(t *testing.T) {
	state := &jobState{fail: true}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	api := &API{DB: db}
	payload := `{"key":"` + strings.Repeat("a", 64) + `","endpoint":"/api-dns/record/add","payload":{"domain":"example.com","name":"@","type":"A","content":"192.0.2.1"}}`
	rec := httptest.NewRecorder()
	api.HandleJob(rec, httptest.NewRequest("POST", "/api-dns/jobs", strings.NewReader(payload)))
	if rec.Code != 503 || state.mutations != 0 || state.applied {
		t.Fatalf("failed mutation committed: code=%d state=%+v", rec.Code, state)
	}
}

func TestZoneSnapshotIncludesNameservers(t *testing.T) {
	state := &jobState{}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	api := &API{DB: db}
	payload := `{"key":"` + strings.Repeat("b", 64) + `","endpoint":"/api-dns/zone/sync","payload":{"domain":"example.com","records":[{"name":"@","type":"SOA","content":"ns1.example.com. admin.example.com. ( {SERIAL} 3600 1800 604800 86400 )"},{"name":"@","type":"NS","content":"ns1.example.com."},{"name":"@","type":"NS","content":"ns2.example.com."}]}}`
	rec := httptest.NewRecorder()
	api.HandleJob(rec, httptest.NewRequest("POST", "/api-dns/jobs", strings.NewReader(payload)))
	if rec.Code != 202 || state.mutations != 3 {
		t.Fatalf("snapshot dropped records: code=%d records=%d body=%s", rec.Code, state.mutations, rec.Body.String())
	}
}

func TestBindReloadFailureAndFallback(t *testing.T) {
	original := runBindCommand
	defer func() { runBindCommand = original }()
	calls := 0
	runBindCommand = func(string, ...string) ([]byte, error) { calls++; return nil, errors.New("unavailable") }
	if reloadBind(false, nil) == nil {
		t.Fatal("failed reload reported success")
	}
	if calls != 4 {
		t.Fatalf("fallback calls=%d", calls)
	}
	runBindCommand = func(name string, args ...string) ([]byte, error) {
		if name == "systemctl" && args[0] == "reload" && args[1] == "bind9" {
			return nil, nil
		}
		return nil, errors.New("unavailable")
	}
	if err := reloadBind(false, nil); err != nil {
		t.Fatalf("successful fallback: %v", err)
	}
}
