package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// acmeScope es el token que motiva todo esto: solo TXT, solo bajo
// _acme-challenge, solo en una zona, y sin poder tocar zonas.
func acmeScope() *TokenScope {
	return &TokenScope{Zone: "example.com", Name: "_acme-challenge", Types: []string{"TXT"}}
}

// Un token sin ninguna columna de scope rellenada tiene que comportarse como el
// token maestro de siempre. Es la fila que deja la migracion sobre los tokens ya
// emitidos, asi que equivocarse aqui deja sin API a los paneles en produccion.
func TestParseTokenScopeSinLimites(t *testing.T) {
	s := parseTokenScope(sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullInt64{})

	if !s.CanManageZones() {
		t.Error("un token sin scope tiene que poder gestionar zonas")
	}
	if !s.AllowsRecord("cualquiera.com", "@", "A") {
		t.Error("un token sin scope tiene que poder escribir cualquier registro")
	}
}

func TestParseTokenScopeNormaliza(t *testing.T) {
	s := parseTokenScope(
		sql.NullString{String: " Example.COM. ", Valid: true},
		sql.NullString{String: "_ACME-Challenge", Valid: true},
		sql.NullString{String: "txt, ,TXT ", Valid: true},
		sql.NullInt64{Int64: 0, Valid: true},
	)

	if s.Zone != "example.com" {
		t.Errorf("zona = %q, se esperaba %q", s.Zone, "example.com")
	}
	if s.Name != "_acme-challenge" {
		t.Errorf("nombre = %q, se esperaba %q", s.Name, "_acme-challenge")
	}
	if len(s.Types) != 2 || s.Types[0] != "TXT" {
		t.Errorf("tipos = %v, se esperaba TXT dos veces sin el hueco vacio", s.Types)
	}
	if s.CanManageZones() {
		t.Error("can_manage_zones = 0 tiene que quitar la gestion de zonas")
	}
}

func TestTokenScopeAllowsRecord(t *testing.T) {
	s := acmeScope()

	cases := []struct {
		domain, name, recordType string
		want                     bool
		porque                   string
	}{
		{"example.com", "_acme-challenge", "TXT", true, "el caso normal: certificado de la propia zona"},
		{"example.com", "_acme-challenge.sub", "TXT", true, "certificado de un subdominio; certbot pide este nombre"},
		{"EXAMPLE.com", "_ACME-Challenge", "txt", true, "los nombres DNS no distinguen mayusculas"},
		{"example.com.", "_acme-challenge", "TXT", true, "el punto final es el mismo nombre"},

		{"otra.com", "_acme-challenge", "TXT", false, "otra zona"},
		{"example.com", "www", "TXT", false, "otro nombre"},
		{"example.com", "_acme-challenge", "A", false, "otro tipo"},
		{"example.com", "sub._acme-challenge", "TXT", false, "_acme-challenge a la derecha es otro nombre, no uno que cuelgue"},
		{"example.com", "_acme-challenge-bis", "TXT", false, "mismo prefijo pero sin corte de etiqueta"},
		{"sub.example.com", "_acme-challenge", "TXT", false, "una zona distinta aunque cuelgue de la del scope"},
	}

	for _, c := range cases {
		if got := s.AllowsRecord(c.domain, c.name, c.recordType); got != c.want {
			t.Errorf("AllowsRecord(%q, %q, %q) = %v, se esperaba %v: %s",
				c.domain, c.name, c.recordType, got, c.want, c.porque)
		}
	}
}

// El export y el listado de registros devuelven la zona de una vez, asi que un
// token acotado a unos registros no puede tratarse como si alcanzara la zona.
func TestIsZoneWide(t *testing.T) {
	if acmeScope().IsZoneWide() {
		t.Error("un scope con nombre y tipo no alcanza la zona entera")
	}
	if !(&TokenScope{Zone: "example.com"}).IsZoneWide() {
		t.Error("un scope de solo zona si la alcanza entera")
	}
	if !unrestrictedScope().IsZoneWide() {
		t.Error("un token sin limites alcanza cualquier zona entera")
	}
}

func scopedRequest(method, path, body string, s *TokenScope) *http.Request {
	return withTokenScope(httptest.NewRequest(method, path, strings.NewReader(body)), s)
}

// Lo que se rechaza por scope tiene que rechazarse antes de tocar la base de
// datos, asi que el API va sin DB a proposito: si algun handler colase la
// comprobacion por detras de una consulta, el test revienta en vez de pasar.
func TestHandlersRechazanFueraDeScope(t *testing.T) {
	api := &API{}

	cases := []struct {
		nombre  string
		handler http.HandlerFunc
		req     *http.Request
	}{
		{"crear zona", api.HandleAdd,
			scopedRequest(http.MethodPost, "/api-dns/add", `{"domain":"example.com"}`, acmeScope())},
		{"borrar zona", api.HandleDelete,
			scopedRequest(http.MethodPost, "/api-dns/delete", `{"domain":"example.com"}`, acmeScope())},
		{"registro en otra zona", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"otra.com","name":"_acme-challenge","type":"TXT","content":"x"}`, acmeScope())},
		{"otro nombre en su zona", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"example.com","name":"www","type":"TXT","content":"x"}`, acmeScope())},
		{"otro tipo en su nombre", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"example.com","name":"_acme-challenge","type":"A","content":"127.0.0.1"}`, acmeScope())},
		// El FQDN es la forma de saltarse un scope de un solo nombre si la
		// comprobacion se hace antes de sanear: aqui tiene que acabar en 403 igual.
		{"nombre en FQDN de otra zona", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"example.com","name":"www.example.com","type":"TXT","content":"x"}`, acmeScope())},
		{"leer registros de otra zona", api.HandleGetRecords,
			scopedRequest(http.MethodGet, "/api-dns/records/otra.com", "", acmeScope())},
		{"exportar otra zona", api.HandleZoneExport,
			scopedRequest(http.MethodGet, "/api-dns/zone/otra.com/export", "", acmeScope())},
		// El export vuelca la zona entera y no se puede recortar a un scope, asi
		// que ni siquiera en su propia zona lo ve un token de un solo registro.
		{"exportar la propia zona con scope de un registro", api.HandleZoneExport,
			scopedRequest(http.MethodGet, "/api-dns/zone/example.com/export", "", acmeScope())},
		{"reasignar servidor", api.HandleZoneServer,
			scopedRequest(http.MethodPost, "/api-dns/zone/server", `{"domain":"example.com","server":"web1"}`, acmeScope())},
		{"reasignar servidor en bloque", api.HandleZoneServerBulk,
			scopedRequest(http.MethodPost, "/api-dns/zone/server/bulk", `{"domains":["example.com"],"server":"web1"}`, acmeScope())},
	}

	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.handler(rec, c.req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: codigo %d, se esperaba 403", c.nombre, rec.Code)
		}
	}
}

// La otra mitad: un scope no puede cerrar lo que si entra en el. Con una BD que no
// levanta, lo que pasa el filtro muere mas adelante con un error de base de datos,
// que es justo lo que distingue "denegado" de "permitido pero sin BD".
func TestHandlersDejanPasarDentroDeScope(t *testing.T) {
	db, err := sql.Open("mysql", "nadie:nada@tcp(127.0.0.1:1)/inexistente")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	api := &API{DB: db}

	cases := []struct {
		nombre  string
		handler http.HandlerFunc
		req     *http.Request
	}{
		{"su TXT en su zona", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"example.com","name":"_acme-challenge","type":"TXT","content":"x"}`, acmeScope())},
		{"su TXT en un subdominio", api.HandleRecordAdd,
			scopedRequest(http.MethodPost, "/api-dns/record/add", `{"domain":"example.com","name":"_acme-challenge.sub","type":"TXT","content":"x"}`, acmeScope())},
		{"listar su propia zona", api.HandleGetRecords,
			scopedRequest(http.MethodGet, "/api-dns/records/example.com", "", acmeScope())},
		{"un token maestro crea zonas", api.HandleAdd,
			scopedRequest(http.MethodPost, "/api-dns/add", `{"domain":"example.com"}`, unrestrictedScope())},
	}

	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.handler(rec, c.req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s: 403, tendria que haber pasado el filtro de scope", c.nombre)
		}
	}
}
