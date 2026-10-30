package main

import "testing"

func TestNameserversUsePrivateConfiguration(t *testing.T) {
	t.Setenv("DNS_NAMESERVERS", "NS-A.example.invalid,ns-b.example.invalid.,ns-a.example.invalid")
	got, err := resolveNameservers(nil)
	if err != nil || len(got) != 2 || got[0] != "ns-a.example.invalid." {
		t.Fatalf("configuration: %v %v", got, err)
	}
	got, err = resolveNameservers([]string{"ns-c.example.invalid"})
	if err != nil || len(got) != 1 || got[0] != "ns-c.example.invalid." {
		t.Fatal("explicit nameservers did not take precedence")
	}
	t.Setenv("DNS_NAMESERVERS", "")
	if _, err = resolveNameservers(nil); err == nil {
		t.Fatal("missing nameservers silently accepted")
	}
	for _, name := range []string{"ns-a..example.invalid", "ns;bad.example.invalid", "bad name.example.invalid", "localhost"} {
		if _, err = resolveNameservers([]string{name}); err == nil {
			t.Fatalf("invalid nameserver accepted: %q", name)
		}
	}
}
