package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// serveWithAutocert atiende la API por HTTPS en :8090 con un certificado que el propio
// binario pide y renueva solo (Let's Encrypt, vía autocert), sin Apache ni certbot
// delante. Escucha además:
//   - :80   imprescindible para el reto HTTP-01 (Let's Encrypt lo valida siempre en el
//           80, nunca en 8090); autocert responde ahí el desafío y el resto se redirige.
//   - :8080 HTTP a secas, sólo redirige a HTTPS — nunca sirve la API en claro, para no
//           exponer los tokens Bearer.
//
// Variables de entorno:
//   TLS_HOSTS      hostnames permitidos, separados por comas (obligatoria).
//   TLS_CACHE_DIR  dónde se guardan los certificados (por defecto /var/lib/lwh-dns/autocert).
//   ACME_STAGING=1 usa el entorno de pruebas de Let's Encrypt (validar sin gastar los
//                  límites de emisión de producción).
func serveWithAutocert(mux *http.ServeMux) {
	var hosts []string
	for _, h := range strings.Split(os.Getenv("TLS_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		log.Fatal("TLS_AUTOCERT=1 requiere TLS_HOSTS con al menos un hostname")
	}
	cacheDir := os.Getenv("TLS_CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "/var/lib/lwh-dns/autocert"
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		log.Fatalf("no se pudo crear TLS_CACHE_DIR %q: %v", cacheDir, err)
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(hosts...),
		Cache:      autocert.DirCache(cacheDir),
	}
	if os.Getenv("ACME_STAGING") == "1" {
		m.Client = &acme.Client{DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory"}
		log.Println("autocert: usando el entorno de STAGING de Let's Encrypt")
	}

	// :80 — el reto HTTP-01 lo contesta autocert; todo lo demás se redirige a HTTPS.
	go func() {
		if err := http.ListenAndServe(":80", m.HTTPHandler(http.HandlerFunc(redirectToHTTPS))); err != nil {
			log.Fatalf("listener :80: %v", err)
		}
	}()

	// :8080 — HTTP, sólo redirección.
	go func() {
		if err := http.ListenAndServe(":8080", http.HandlerFunc(redirectToHTTPS)); err != nil {
			log.Fatalf("listener :8080: %v", err)
		}
	}()

	// :8090 — la API por HTTPS, con el certificado gestionado por autocert.
	srv := &http.Server{
		Addr:      ":8090",
		Handler:   mux,
		TLSConfig: m.TLSConfig(),
	}
	log.Println("Lightweight DNS: HTTPS en :8090 con autocert (hosts: " + strings.Join(hosts, ", ") + ")")
	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("listener :8090 TLS: %v", err)
	}
}

// redirectToHTTPS reenvía la petición al mismo host en https:8090, conservando ruta y
// query. Se usa en :80 (para lo que no es el reto ACME) y en :8080.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	http.Redirect(w, r, "https://"+host+":8090"+r.URL.RequestURI(), http.StatusMovedPermanently)
}
