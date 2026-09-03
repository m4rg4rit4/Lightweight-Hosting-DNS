package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// El atajo por prefijo "/api-dns/ddns/" que había en AuthMiddleware dejaba pasar sin
// token cualquier ruta bajo ese prefijo, incluidas list y approve (administración).
// Este test fija que el middleware, allí donde se aplica, SIEMPRE exige token — sin él,
// el handler envuelto no llega a ejecutarse.
func TestAuthMiddlewareSiempreExigeToken(t *testing.T) {
	api := &API{} // el 401 se decide antes de tocar la BD
	called := false
	h := api.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	// una ruta ddns: es justo la que antes se colaba
	h(rec, httptest.NewRequest(http.MethodGet, "/api-dns/ddns/list", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("sin token = %d, se esperaba 401", rec.Code)
	}
	if called {
		t.Error("el handler no debe ejecutarse sin token válido")
	}
}

// Comprueba el reparto público/privado de las rutas DDNS tal como quedan cableadas en
// registerRoutes: list y approve exigen token; register (y las demás de dispositivo) no
// pasan por el middleware.
func TestDdnsAuthRouting(t *testing.T) {
	api := &API{}
	mux := http.NewServeMux()
	api.registerRoutes(mux)

	for _, p := range []string{"/api-dns/ddns/list", "/api-dns/ddns/approve"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s sin token = %d, se esperaba 401 (protegida por el middleware)", p, rec.Code)
		}
	}

	// register no está tras el middleware: su propio handler responde (400 por falta del
	// token de dispositivo), nunca el 401 del middleware. Lo que importa es que NO sea 401.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api-dns/ddns/register", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("register no debería estar tras el middleware; code=%d", rec.Code)
	}
}

// El registro A del cliente se escribe con la IP que devuelve clientIP, así que
// falsificarla vía X-Forwarded-For permitiría apuntar el registro a una IP ajena.
func TestClientIP(t *testing.T) {
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ name, remote, xff, want string }{
		{"tras proxy loopback, un XFF", "127.0.0.1:5555", "203.0.113.7", "203.0.113.7"},
		// El cliente antepone una IP falsa; el proxy añade la real al final: se toma la última.
		{"tras proxy, XFF falsificado + real", "127.0.0.1:5555", "1.1.1.1, 203.0.113.7", "203.0.113.7"},
		// Acceso directo al binario (sin proxy): la IP es la de la conexión, XFF se ignora.
		{"acceso directo ignora XFF", "203.0.113.9:4444", "1.1.1.1", "203.0.113.9"},
		{"sin XFF usa la conexión", "127.0.0.1:5555", "", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := clientIP(mk(c.remote, c.xff)); got != c.want {
			t.Errorf("%s: clientIP = %q, se esperaba %q", c.name, got, c.want)
		}
	}
}

// En el modo autocert, :80 (salvo el reto ACME) y :8080 redirigen a https:8090
// conservando ruta y query, y nunca sirven la API en claro.
func TestRedirectToHTTPS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://x/api-dns/zones?a=1", nil)
	req.Host = "ns1.example.com:8080"
	rec := httptest.NewRecorder()
	redirectToHTTPS(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("code = %d, se esperaba 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://ns1.example.com:8090/api-dns/zones?a=1" {
		t.Errorf("Location = %q", loc)
	}
}
