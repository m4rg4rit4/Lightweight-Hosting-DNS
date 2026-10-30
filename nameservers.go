package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var nameserverPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

// Infrastructure names belong in deployment configuration or the API payload,
// never in public source. Hosting sends the same list to every DNS node.
func resolveNameservers(explicit []string) ([]string, error) {
	if len(explicit) == 0 {
		explicit = strings.Split(os.Getenv("DNS_NAMESERVERS"), ",")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range explicit {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if value == "" {
			continue
		}
		if len(value) > 253 || !strings.Contains(value, ".") || strings.Contains(value, "..") || !nameserverPattern.MatchString(value) {
			return nil, fmt.Errorf("invalid nameserver")
		}
		value += "."
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("configure DNS_NAMESERVERS or provide nameservers")
	}
	return out, nil
}
