package main

import (
	"os"
	"log"
	"net/http"
	"strings"
)

func (api *API) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Este middleware SIEMPRE exige un Bearer válido. Las rutas DDNS que deben ser
		// públicas (register/update/status, que se autentican con el token del propio
		// dispositivo) NO se envuelven con él en registerRoutes; las de administración
		// (list, approve) SÍ. Aquí había antes un atajo por prefijo "/api-dns/ddns/" que
		// dejaba pasar TODO sin token: como list y approve también empiezan por ese
		// prefijo, quedaban accesibles sin autenticación —list volcaba en claro los
		// tokens de todos los dispositivos DDNS y approve dejaba aprobar clientes y
		// asignarles zona/registro a cualquiera—. Se elimina: lo público se decide por
		// no envolver la ruta, no dentro del middleware.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"success":false,"message":"Unauthorized. Provide a valid Bearer Token."}`))
			return
		}
		token := auth[7:]

		scope, ok := api.loadTokenScope(token)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"success":false,"message":"Unauthorized. Token invalid or inactive."}`))
			return
		}

		// El scope viaja en el contexto porque los handlers necesitan mas que un
		// si/no: cual es su zona, que nombres y que tipos. Es el unico sitio donde
		// se rellena, asi que un handler que se registre sin este middleware queda
		// sin restricciones, igual que antes.
		next.ServeHTTP(w, withTokenScope(r, scope))
	}
}

// registerRoutes wires every endpoint onto mux. Split out of main so the routing
// can be asserted in a test: /api-dns/zone/ is a subtree pattern serving the zone
// export, and /api-dns/zone/server sits inside it, so a mistake there would
// silently break either the export or the server assignment.
func (api *API) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api-dns/add", api.AuthMiddleware(api.HandleAdd))
	mux.HandleFunc("/api-dns/delete", api.AuthMiddleware(api.HandleDelete))
	mux.HandleFunc("/api-dns/record/add", api.AuthMiddleware(api.HandleRecordAdd))
	mux.HandleFunc("/api-dns/record/edit", api.AuthMiddleware(api.HandleRecordEdit))
	mux.HandleFunc("/api-dns/record/del", api.AuthMiddleware(api.HandleRecordDel))

	mux.HandleFunc("/api-dns/zones", api.AuthMiddleware(api.HandleGetZones))
	mux.HandleFunc("/api-dns/records/", api.AuthMiddleware(api.HandleGetRecords))
	mux.HandleFunc("/api-dns/zone/server", api.AuthMiddleware(api.HandleZoneServer))
	mux.HandleFunc("/api-dns/zone/server/bulk", api.AuthMiddleware(api.HandleZoneServerBulk))
	mux.HandleFunc("/api-dns/zone/", api.AuthMiddleware(api.HandleZoneExport))
	mux.HandleFunc("/api-dns/status/", api.AuthMiddleware(api.HandleStatus))

	mux.HandleFunc("/api-dns/ddns/register", api.HandleDdnsRegister)
	mux.HandleFunc("/api-dns/ddns/update", api.HandleDdnsUpdate)
	mux.HandleFunc("/api-dns/ddns/status", api.HandleDdnsStatus)
	mux.HandleFunc("/api-dns/ddns/list", api.AuthMiddleware(api.HandleDdnsList))
	mux.HandleFunc("/api-dns/ddns/approve", api.AuthMiddleware(api.HandleDdnsApprove))
}

func main() {
	cfg, err := loadDBConfig()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	db, err := initDB(cfg.User, cfg.Pass, cfg.Host, cfg.Name)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()

	migrate(db)

	// Start engine in background
	go runEngine(db)

	api := &API{DB: db}
	mux := http.NewServeMux()
	api.registerRoutes(mux)

	// Modo autocert: el propio binario sirve HTTPS en :8090 y gestiona el certificado
	// (sin Apache ni certbot). Se activa con TLS_AUTOCERT=1. Sin él, arranca en modo
	// "detrás de proxy" escuchando en loopback, como hasta ahora — así la migración se
	// hace nodo a nodo sin cambiar el binario.
	if os.Getenv("TLS_AUTOCERT") == "1" {
		serveWithAutocert(mux)
		return
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}
	// Por defecto sólo loopback. El binario se publica siempre a través del proxy web
	// (Apache -> 127.0.0.1:PORT), así que escuchar en todas las interfaces dejaba la API
	// alcanzable directamente desde Internet, sin TLS, en los nodos donde ningún firewall
	// lo filtraba. BIND_ADDR permite abrirlo a propósito si alguna vez hiciera falta.
	bind := os.Getenv("BIND_ADDR")
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := bind + ":" + port
	log.Println("Starting Lightweight DNS v2 API on " + addr + "...")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}




