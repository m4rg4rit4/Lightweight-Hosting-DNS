package main

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZoneCreationIncludesRequiredRecords(t *testing.T) {
	for _, test := range []struct{ ip, types string }{
		{"192.0.2.20", "NS,NS,A,CNAME,SOA,"},
		{"2001:db8::20", "NS,NS,AAAA,CNAME,SOA,"},
		{"", "NS,NS,SOA,"},
	} {
		state := &jobState{newZone: true}
		db := sql.OpenDB(jobConnector{state: state})
		body := `{"domain":"example.invalid","ip":"` + test.ip + `","nameservers":["ns-a.example.invalid","ns-b.example.invalid"]}`
		rec := httptest.NewRecorder()
		(&API{DB: db}).HandleAdd(rec, httptest.NewRequest("POST", "/api-dns/add", strings.NewReader(body)))
		db.Close()
		if rec.Code != 200 || state.recordTypes != test.types || state.status != "pending" {
			t.Fatalf("incomplete creation: code=%d types=%s status=%s", rec.Code, state.recordTypes, state.status)
		}
		if !strings.Contains(state.soaContent, "{SERIAL}") || !strings.HasPrefix(state.soaContent, "ns-a.example.invalid. admin.example.invalid.") {
			t.Fatal("missing or invalid SOA row")
		}
		if test.ip != "" && state.wwwTarget != "example.invalid." {
			t.Fatal("www does not point at zone apex")
		}
	}
}

func TestSOAInsertFailureRollsBackWholeZone(t *testing.T) {
	state := &jobState{newZone: true, failRecordType: "SOA"}
	db := sql.OpenDB(jobConnector{state: state})
	defer db.Close()
	rec := httptest.NewRecorder()
	(&API{DB: db}).HandleAdd(rec, httptest.NewRequest("POST", "/api-dns/add", strings.NewReader(`{"domain":"example.invalid","ip":"192.0.2.20","nameservers":["ns.example.invalid"]}`)))
	if rec.Code != 503 || !state.newZone || state.mutations != 0 || state.status != "" {
		t.Fatalf("partial zone survived failed SOA insert: code=%d state=%+v", rec.Code, state)
	}
}

func TestInvalidCreationHasNoSideEffects(t *testing.T) {
	for _, body := range []string{
		`{"domain":"../example.invalid","nameservers":["ns.example.invalid"]}`,
		`{"domain":"example.invalid","ip":"not-an-ip","nameservers":["ns.example.invalid"]}`,
	} {
		state := &jobState{newZone: true}
		db := sql.OpenDB(jobConnector{state: state})
		rec := httptest.NewRecorder()
		(&API{DB: db}).HandleAdd(rec, httptest.NewRequest("POST", "/api-dns/add", strings.NewReader(body)))
		db.Close()
		if rec.Code != 400 || !state.newZone {
			t.Fatal("invalid zone creation accepted")
		}
	}
}

func TestLegacySOARepairPreservesServedValues(t *testing.T) {
	old := "$ORIGIN example.invalid.\n@ IN SOA ns.example.invalid. dns-admin.example.invalid. (\n2026103001 ; serial\n7200 900 1209600 300 )"
	got := existingSOA(old, "fallback")
	if got != "ns.example.invalid. dns-admin.example.invalid. ( {SERIAL} 7200 900 1209600 300 )" {
		t.Fatal(got)
	}
	if existingSOA("no SOA here", "fallback") != "fallback" {
		t.Fatal("invalid SOA parsed")
	}
}

func TestImportedSOAUsesIncreasingSerial(t *testing.T) {
	got := soaWithSerial("ns.example.invalid. admin.example.invalid. ( 2026103001 7200 900 1209600 300 )", "2026103002")
	if !strings.Contains(got, "( 2026103002 7200 900 1209600 300 )") {
		t.Fatal(got)
	}
}
