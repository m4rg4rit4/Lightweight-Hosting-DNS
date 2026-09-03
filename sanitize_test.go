package main

import "testing"

// The record lookup used by edit/delete matches on the *stored* content, and the
// stored content is whatever SanitizeRecord produced at insert time. So sanitizing
// the incoming old_content must land on exactly the same string, and sanitizing an
// already-stored value must be a no-op. If this breaks, edit and delete stop finding
// their record and fall back to a 404 instead of touching the wrong one.
func TestSanitizeRecordIsIdempotent(t *testing.T) {
	cases := []struct {
		domain, name, recordType, content string
	}{
		{"example.com", "@", "TXT", `"v=spf1 include:_spf.google.com ~all"`},
		{"example.com", "@", "TXT", "v=spf1 include:_spf.google.com ~all"},
		{"example.com", "www.example.com.", "A", "192.0.2.10"},
		{"example.com", "www.example.com", "A", "192.0.2.10"},
		{"example.com", "example.com", "MX", "mail.example.com."},
		{"example.com", "mail", "MX", "mail.example.com."},
	}

	for _, c := range cases {
		name1, content1 := SanitizeRecord(c.domain, c.name, c.recordType, c.content)
		name2, content2 := SanitizeRecord(c.domain, name1, c.recordType, content1)

		if name1 != name2 {
			t.Errorf("name not idempotent for %q: %q -> %q", c.name, name1, name2)
		}
		if content1 != content2 {
			t.Errorf("content not idempotent for %q: %q -> %q", c.content, content1, content2)
		}
	}
}

// The zone apex must collapse to "@" however the caller spells it, otherwise the
// same record is looked up under two different names on two different nodes.
func TestSanitizeRecordCollapsesApex(t *testing.T) {
	for _, name := range []string{"example.com", "example.com.", "@"} {
		got, _ := SanitizeRecord("example.com", name, "A", "192.0.2.10")
		if got != "@" {
			t.Errorf("SanitizeRecord(%q) = %q, want \"@\"", name, got)
		}
	}
}

func TestSanitizeRecordStripsZoneSuffix(t *testing.T) {
	for _, name := range []string{"www.example.com", "www.example.com.", "www"} {
		got, _ := SanitizeRecord("example.com", name, "A", "192.0.2.10")
		if got != "www" {
			t.Errorf("SanitizeRecord(%q) = %q, want \"www\"", name, got)
		}
	}
}
