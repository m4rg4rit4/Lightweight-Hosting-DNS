package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func assignmentCall(db *sql.DB, body string, replica bool) int {
	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()
	rec := httptest.NewRecorder()
	(&API{DB: tx}).changeAssignment(rec, httptest.NewRequest("POST", "/assignment", strings.NewReader(body)), replica)
	if rec.Code == 200 {
		if err = tx.Commit(); err != nil {
			panic(err)
		}
	}
	return rec.Code
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	state := &jobState{}
	mu := &sync.Mutex{}
	db := sql.OpenDB(jobConnector{state: state, mu: mu})
	defer db.Close()
	results := make(chan int, 2)
	for _, server := range []string{"host-a", "host-b"} {
		go func(server string) {
			body, _ := json.Marshal(map[string]interface{}{"domain": "example.com", "server": server, "expected_revision": 0})
			results <- assignmentCall(db, string(body), false)
		}(server)
	}
	counts := map[int]int{}
	counts[<-results]++
	counts[<-results]++
	if counts[200] != 1 || counts[409] != 1 || state.revision != 1 {
		t.Fatalf("claims did not serialize: codes=%v revision=%d", counts, state.revision)
	}
}

func TestDelayedAssignmentCannotUndoRelease(t *testing.T) {
	state := &jobState{owner: "host-a", revision: 1}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	if code := assignmentCall(db, `{"domain":"example.com","server":"host-b","revision":3}`, true); code != 200 {
		t.Fatal(code)
	}
	if code := assignmentCall(db, `{"domain":"example.com","server":"host-a","revision":2}`, true); code != 200 {
		t.Fatal(code)
	}
	if state.owner != "host-b" || state.revision != 3 {
		t.Fatal("late replica undid newer transfer")
	}
	if code := assignmentCall(db, `{"domain":"example.com","server":"","expected_revision":3}`, false); code != 200 {
		t.Fatal(code)
	}
	if state.owner != "" || state.revision != 4 {
		t.Fatal("release did not clear owner")
	}
	assignmentCall(db, `{"domain":"example.com","server":"host-a","revision":2}`, true)
	if state.owner != "" || state.revision != 4 {
		t.Fatal("late claim undid release")
	}
	if code := assignmentCall(db, `{"domain":"example.com","server":"host-b","expected_revision":0}`, false); code != 409 {
		t.Fatalf("stale page = %d", code)
	}
}

func TestAssignmentReceiptReplaysOriginalDecision(t *testing.T) {
	state := &jobState{}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	api := &API{DB: db}
	body := `{"key":"` + strings.Repeat("a", 64) + `","endpoint":"/api-dns/zone/assignment/decide","payload":{"domain":"example.com","server":"host-a","expected_revision":0}}`
	call := func() map[string]interface{} {
		rec := httptest.NewRecorder()
		api.HandleJob(rec, httptest.NewRequest("POST", "/api-dns/jobs", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		var result map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &result)
		return result["result"].(map[string]interface{})
	}
	first := call()
	state.owner = "host-b"
	state.revision = 2
	second := call()
	if first["revision"] != second["revision"] || second["server"] != "host-a" || state.revision != 2 {
		t.Fatal("retry did not preserve original decision")
	}
}
