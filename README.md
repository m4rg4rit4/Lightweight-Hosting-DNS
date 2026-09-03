# Lightweight DNS

Servidor DNS ligero pensado para entornos de recursos limitados (objetivo: 1 vCore /
1 GB RAM). Una API en **Go** gestiona las zonas sobre **BIND9** y sirve HTTPS
**gestionando su propio certificado de Let's Encrypt** (vía `autocert`): sin Apache, sin
certbot y sin PHP. El certificado se emite y **se renueva solo, en caliente**, sin
reiniciar el servicio.

## Características

- API REST autenticada con **Bearer token**, con **alcance por zona / nombre / tipo**
  (un token puede limitarse, por ejemplo, a crear solo el `TXT` `_acme-challenge` de una
  zona — útil para clientes ACME).
- **TLS autogestionado** y autorrenovado por el propio binario.
- Generación de ficheros de zona para BIND9 y recarga automática.
- **DDNS**: registro y actualización dinámica de la IP de dispositivos.
- Perfil de bajo consumo (ajustes de MariaDB incluidos).

## Arquitectura y puertos

| Puerto | Servicio |
| :--- | :--- |
| `:8090` | **API HTTPS** (`/api-dns/...`) con certificado de Let's Encrypt |
| `:80` | Reto ACME (emisión/renovación del certificado) + redirección a HTTPS |
| `:8080` | HTTP → redirección a HTTPS |
| `:53` | DNS servido por BIND9 |

La API interna del binario escucha en `127.0.0.1` y MariaDB solo en loopback: ninguna de
las dos se expone a Internet. El puerto `80` **debe** ser accesible desde fuera para que
Let's Encrypt pueda validar y renovar el certificado.

## Requisitos

- Debian 12/13, acceso `root`.
- El FQDN del nodo (p. ej. `ns1.tu-dominio.com`) debe **resolver a la IP pública** del
  servidor antes de instalar (el certificado no se emite hasta que el DNS apunta bien).

## Instalación

El binario se sube manualmente (el repositorio puede ser privado):

1. Compila para Linux:
   ```bash
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o lwh-dns-v2 .
   ```
2. Súbelo al servidor y dale permisos:
   ```bash
   scp lwh-dns-v2 root@servidor:/usr/local/bin/lwh-dns-v2
   ssh root@servidor 'chmod +x /usr/local/bin/lwh-dns-v2'
   ```
3. Copia `install.sh` al servidor y ejecútalo como root:
   ```bash
   sudo ./install.sh
   ```
   Pregunta el hostname, el dominio y el email; crea la base de datos y las tablas,
   escribe `/etc/lwh-dns/lwh-dns.env`, configura el firewall y arranca el servicio. Al
   arrancar, el binario emite el certificado (si el DNS y el puerto 80 están listos).

## Configuración

Las credenciales de la base de datos y los parámetros de TLS viven en
`/etc/lwh-dns/lwh-dns.env` (permisos `600`), que carga el servicio systemd:

```ini
DB_USER=...
DB_PASS=...
DB_HOST=127.0.0.1
DB_NAME=...
# TLS gestionado por el binario (autocert)
TLS_AUTOCERT=1
TLS_HOSTS=ns1.tu-dominio.com
TLS_CACHE_DIR=/var/lib/lwh-dns/autocert
```

Para restringir la API (`:8090`) a un rango de IPs, hazlo en el firewall del host, p. ej.:

```bash
ufw allow from 203.0.113.0/24 to any port 8090 proto tcp
```

## API (resumen)

Todas las rutas de administración requieren la cabecera `Authorization: Bearer <token>`.

| Método | Ruta | Descripción |
| :--- | :--- | :--- |
| `POST` | `/api-dns/add` | Crea una zona |
| `POST` | `/api-dns/delete` | Elimina una zona |
| `POST` | `/api-dns/record/add` | Añade un registro |
| `POST` | `/api-dns/record/edit` | Edita un registro |
| `POST` | `/api-dns/record/del` | Elimina un registro |
| `GET`  | `/api-dns/zones` | Lista las zonas |
| `GET`  | `/api-dns/records/{zona}` | Lista los registros de una zona |
| `GET`  | `/api-dns/zone/{zona}/export` | Exporta el fichero de zona |
| `GET`  | `/api-dns/status/{id}` | Estado de una tarea de la cola |

Rutas DDNS: `POST /api-dns/ddns/register`, `POST /api-dns/ddns/update`,
`GET /api-dns/ddns/status` (autenticadas con el token del propio dispositivo);
`GET /api-dns/ddns/list` y `POST /api-dns/ddns/approve` son de administración y exigen un
token válido.

### Tokens con alcance

Las columnas `scope_zone`, `scope_name`, `scope_types` y `can_manage_zones` de
`sys_dns_tokens` limitan lo que un token puede tocar. Todas a `NULL` = token maestro (sin
límites). Ejemplo, un token que solo puede crear/borrar el TXT de validación ACME de una
zona:

```sql
INSERT INTO sys_dns_tokens (token, client_name, scope_zone, scope_name, scope_types, can_manage_zones)
VALUES ('<token>', 'acme', 'tu-dominio.com', '_acme-challenge', 'TXT', 0);
```

## Desarrollo

```bash
go build ./...
go test ./...
```
