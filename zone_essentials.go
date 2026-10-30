package main

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func validZoneName(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || !nameserverPattern.MatchString(label) {
			return false
		}
	}
	return true
}

var fullSOA = regexp.MustCompile(`(?i)\bSOA\s+(\S+)\s+(\S+)\s*\(?\s*\d+\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
var zoneComment = regexp.MustCompile(`(?m);[^\n]*`)

// Persist a missing SOA using the values already present in the zone file when
// available. The former fallback existed only in the generated file, hiding the
// SOA from the API and the panel. Existing custom records are never replaced.
func ensureZoneEssentials(db *sql.DB, zoneID int, domain string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int
	if err = tx.QueryRow("SELECT id FROM sys_dns_zones WHERE id = ? FOR UPDATE", zoneID).Scan(&id); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT type, content FROM sys_dns_records WHERE zone_id = ? AND name = '@' AND type IN ('NS','SOA')", zoneID)
	if err != nil {
		return err
	}
	var ns []string
	soaCount := 0
	for rows.Next() {
		var kind, content string
		if err = rows.Scan(&kind, &content); err != nil {
			rows.Close()
			return err
		}
		if kind == "NS" {
			ns = append(ns, content)
		} else {
			soaCount++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if soaCount > 1 {
		return fmt.Errorf("zone has multiple apex SOA records")
	}
	servers, err := resolveNameservers(ns)
	if err != nil {
		return err
	}
	if len(ns) == 0 {
		for _, server := range servers {
			if _, err = tx.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'NS', ?)", zoneID, server); err != nil {
				return err
			}
		}
	}
	if soaCount == 0 {
		soa := fmt.Sprintf("%s hostmaster.%s. ( {SERIAL} 3600 1800 604800 86400 )", servers[0], domain)
		if content, e := os.ReadFile(zonesDir + "/db." + domain); e == nil {
			soa = existingSOA(string(content), soa)
		}
		if _, err = tx.Exec("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'SOA', ?)", zoneID, soa); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func existingSOA(content, fallback string) string {
	m := fullSOA.FindStringSubmatch(zoneComment.ReplaceAllString(content, ""))
	if len(m) != 7 {
		return fallback
	}
	return fmt.Sprintf("%s %s ( {SERIAL} %s %s %s %s )", m[1], m[2], m[3], m[4], m[5], m[6])
}

func soaWithSerial(content, serial string) string {
	if !strings.Contains(content, "{SERIAL}") {
		content = existingSOA("SOA "+content, content)
	}
	return strings.Replace(content, "{SERIAL}", serial, 1)
}
