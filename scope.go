package main

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

// TokenScope limita lo que puede tocar un token. Los campos vacios significan "sin
// limite", que es como se quedan los tokens que ya existian.
//
// Sin esto un token solo tenia dos estados, valido o no, y valido daba escritura
// sobre cualquier zona mas la posibilidad de encolar el borrado de cualquiera de
// ellas: no habia forma de dar acceso a una aplicacion que solo necesita su
// registro _acme-challenge (validacion DNS-01 de Let's Encrypt) sin entregarle el
// DNS entero.
type TokenScope struct {
	Zone        string   // unica zona permitida; "" = todas
	Name        string   // nombre permitido dentro de la zona; "" = cualquiera
	Types       []string // tipos de registro permitidos; vacio = todos
	ManageZones bool     // crear y borrar zonas, y reasignarlas de servidor
}

// unrestrictedScope es el token de siempre: sin limites y con gestion de zonas.
func unrestrictedScope() *TokenScope {
	return &TokenScope{ManageZones: true}
}

func parseTokenScope(zone, name, types sql.NullString, manageZones sql.NullInt64) *TokenScope {
	s := &TokenScope{
		Zone: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone.String), ".")),
		Name: strings.ToLower(strings.TrimSpace(name.String)),
		// Una fila sin valor se lee como el comportamiento antiguo, no como el
		// restrictivo: la columna se anade por migracion sobre tokens que ya estaban
		// en uso y quitarles la gestion de zonas en caliente romperia los paneles.
		ManageZones: !manageZones.Valid || manageZones.Int64 != 0,
	}
	for _, t := range strings.Split(types.String, ",") {
		if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
			s.Types = append(s.Types, t)
		}
	}
	return s
}

// AllowsZone compara en minusculas y sin el punto final porque los nombres DNS no
// distinguen mayusculas: si no, "Example.com" o "example.com." esquivarian un scope
// escrito en minusculas.
func (s *TokenScope) AllowsZone(domain string) bool {
	if s == nil || s.Zone == "" {
		return true
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), ".")) == s.Zone
}

// AllowsName acepta el nombre exacto y cualquiera que cuelgue de el por la
// izquierda. Certbot pide "_acme-challenge" para la zona y "_acme-challenge.sub"
// para un subdominio, y las dos formas tienen que caber en el mismo scope.
func (s *TokenScope) AllowsName(name string) bool {
	if s == nil || s.Name == "" {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	return n == s.Name || strings.HasPrefix(n, s.Name+".")
}

func (s *TokenScope) AllowsType(recordType string) bool {
	if s == nil || len(s.Types) == 0 {
		return true
	}
	t := strings.ToUpper(strings.TrimSpace(recordType))
	for _, allowed := range s.Types {
		if allowed == t {
			return true
		}
	}
	return false
}

// AllowsRecord espera el name ya pasado por SanitizeRecord. Comprobarlo antes de
// sanear dejaria pasar el FQDN ("_acme-challenge.example.com") contra un scope de
// un solo nombre, porque no coincide con ninguna de las dos formas que se guardan.
func (s *TokenScope) AllowsRecord(domain, name, recordType string) bool {
	return s.AllowsZone(domain) && s.AllowsName(name) && s.AllowsType(recordType)
}

func (s *TokenScope) CanManageZones() bool {
	return s == nil || s.ManageZones
}

// IsZoneWide dice si el token alcanza la zona entera y no solo unos registros. Lo
// que devuelve la zona completa de una vez (el export, el listado de registros) se
// mide con esto: comprobar solo la zona dejaria que un token de un unico TXT se
// leyera todo lo que tiene alrededor.
func (s *TokenScope) IsZoneWide() bool {
	return s == nil || (s.Name == "" && len(s.Types) == 0)
}

type scopeCtxKey struct{}

func withTokenScope(r *http.Request, s *TokenScope) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), scopeCtxKey{}, s))
}

// scopeOf devuelve el scope que dejo AuthMiddleware. Sin scope en el contexto se
// asume sin restriccion, que es el comportamiento exacto que tenia la API antes de
// que existieran los scopes; el unico camino hasta estos handlers pasa por el
// middleware, que siempre lo pone.
func scopeOf(r *http.Request) *TokenScope {
	if s, ok := r.Context().Value(scopeCtxKey{}).(*TokenScope); ok && s != nil {
		return s
	}
	return unrestrictedScope()
}

func denyScope(w http.ResponseWriter) {
	http.Error(w, `{"success":false,"message":"Token scope does not allow this operation"}`, http.StatusForbidden)
}

// loadTokenScope valida el token y devuelve sus limites.
//
// Si las columnas de scope no existen todavia se repite la consulta antigua en
// lugar de rechazar la peticion: migrate() registra un ALTER fallido pero no aborta
// el arranque, y sin esta salida un ALTER que no llegara a aplicarse dejaria toda
// la API devolviendo 401. La distincion la da sql.ErrNoRows, que solo puede venir
// de un token que de verdad no esta o no esta activo.
func (api *API) loadTokenScope(token string) (*TokenScope, bool) {
	var (
		id                int
		zone, name, types sql.NullString
		manageZones       sql.NullInt64
	)
	err := api.DB.QueryRow(`
		SELECT id, scope_zone, scope_name, scope_types, can_manage_zones
		FROM sys_dns_tokens WHERE token = ? AND is_active = 1`, token).
		Scan(&id, &zone, &name, &types, &manageZones)

	switch err {
	case nil:
		return parseTokenScope(zone, name, types, manageZones), true
	case sql.ErrNoRows:
		return nil, false
	}

	if err := api.DB.QueryRow("SELECT id FROM sys_dns_tokens WHERE token = ? AND is_active = 1", token).Scan(&id); err != nil {
		return nil, false
	}
	return unrestrictedScope(), true
}
