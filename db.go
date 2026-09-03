package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type DNSZone struct {
	ID           int
	Domain       string
	ZoneFilePath string
	IsActive     bool
	UpdatedAt    string
}

type DNSRecord struct {
	ID        int
	ZoneID    int
	Name      string
	Type      string
	Content   string
	TTL       int
	Priority  sql.NullInt32
	SortOrder int
}

type DNSRequest struct {
	ID     int
	Action string
	Domain string
	Status string
}

func initDB(user, pass, host, dbname string) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", user, pass, host, dbname)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Minute * 5)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// migrate applies the schema changes this build needs. Every statement is
// idempotent, so it can run on every boot: install.sh carries the same DDL for
// fresh installs, and this covers nodes that only get the new binary.
func migrate(db *sql.DB) {
	stmts := []string{
		// Requests were claimed with no record of when or how many times, so a row
		// left in 'processing' by a crash, or moved to 'error' by a transient
		// named-checkconf failure, was never picked up again.
		"ALTER TABLE sys_dns_requests ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0",
		"ALTER TABLE sys_dns_requests ADD COLUMN IF NOT EXISTS started_at DATETIME NULL",
		"ALTER TABLE sys_dns_requests ADD INDEX IF NOT EXISTS idx_status (status)",

		// Servidor de hosting al que pertenece la zona. Es solo una etiqueta para
		// que cada panel filtre su vista; no restringe nada. Se guarda el slug y no
		// un id porque cada nodo del cluster tiene su propia BD y sus propios
		// AUTO_INCREMENT: un id no significaria lo mismo en un nodo que en otro.
		"ALTER TABLE sys_dns_zones ADD COLUMN IF NOT EXISTS server_slug VARCHAR(64) NULL",
		"ALTER TABLE sys_dns_zones ADD INDEX IF NOT EXISTS idx_server_slug (server_slug)",

		// Alcance del token. Hasta aqui un token solo podia estar activo o no, y
		// activo significaba escritura sobre todas las zonas: para que una
		// aplicacion externa pudiera crear su TXT de validacion ACME habia que
		// darle un token que tambien podia borrar cualquier zona. Todo NULL, que es
		// como quedan los tokens ya emitidos, sigue siendo "sin limite".
		"ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_zone VARCHAR(255) NULL",
		"ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_name VARCHAR(255) NULL",
		"ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_types VARCHAR(128) NULL",
		"ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS can_manage_zones TINYINT(1) NOT NULL DEFAULT 1",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Printf("migrate: %q: %v", s, err)
		}
	}
}
