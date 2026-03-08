
---

# Especificaciones Técnicas: Lightweight-Hosting-DNS

## 0. Contexto de Hardware y Optimización

Este sistema formará parte de un cluster de servidores DNS (primarios y secundarios) que correrán en máquinas virtuales o VPS con **recursos muy limitados (1 Core CPU, 1GB RAM)**.
Por este motivo, cada componente (PHP, Base de Datos y BIND9) debe configurarse con las siguientes premisas:
* **PHP Base/API**: Evitar cargar frameworks pesados. Los scripts `/api-dns/*` deben ser archivos PHP nativos muy ligeros apoyados en PDO puro para que respondan en milisegundos sin ahogar la memoria.
* **BIND9**: Debe limitarse la caché en memoria y los logs para evitar que un ataque DNS amplification o el propio uso normal sature la RAM de 1GB.
* **Cron (`sync_dns.php`)**: Debe procesar por lotes (arrays) para aprovechar la memoria eficientemente, liberar las variables pesadas (`unset`) tras cada iteración de un dominio y terminar su ejecución lo más rápido posible.

---

## 1. Arquitectura de Base de Datos (`dbadmin`)

Todos los nombres de tabla deben llevar el prefijo `sys_dns_`.

### Configuración de Conexión

Tanto los scripts de la API como el script en background deberán incluir el archivo centralizado ubicado en `/var/www/admin_panel/config.php` y utilizar su función PDO ya existente.

**Ejemplo de la estructura de `/var/www/admin_panel/config.php` (no escribir esto en la app, solo incluirlo):**

```php
<?php
// Estos valores ya estarán definidos en el servidor
define('DB_HOST', '... ');
define('DB_NAME', 'dbadmin');
define('DB_USER', 'dbadmin');
define('DB_PASS', '... ');
define('ADMIN_EMAIL', '...');
define('DB_MANAGER_DIR', 'phpmyadmin');

function getPDO() {
    static $pdo;
    if (!$pdo) {
        $pdo = new PDO("mysql:host=" . DB_HOST . ";dbname=" . DB_NAME, DB_USER, DB_PASS, [
            PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
            PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC
        ]);
    }
    return $pdo;
}
?>
```

### Tabla: `sys_dns_requests`

Esta tabla gestiona el ciclo de vida de cada solicitud.

* `id`: INT AUTO_INCREMENT (Este será el **Número de Solicitud**).
* `action`: ENUM('add', 'delete') (Tipo de operación).
* `domain`: VARCHAR(255) (Ej: `cliente1.com`).
* `target_ip`: VARCHAR(45) (IP del servidor web remoto, nulo si es delete).
* `status`: ENUM('pending', 'processing', 'completed', 'error').
* `request_date`: DATETIME (Fecha de creación).
* `processed_date`: DATETIME (Fecha en que el cron terminó el trabajo).
* `error_log`: TEXT (En caso de que falle la validación de BIND).

### Tabla: `sys_dns_zones`

Para mantener el registro principal de cada dominio gestionado.

* `id`: INT AUTO_INCREMENT.
* `domain`: VARCHAR(255) UNIQUE.
* `zone_file_path`: VARCHAR(255).
* `is_active`: BOOLEAN.
* `created_at`: DATETIME.
* `updated_at`: DATETIME.

### Tabla: `sys_dns_records`

Para almacenar los registros individuales de cada zona, permitiendo su regeneración completa o modificaciones granulares en el futuro. **Soporta registros Wildcard (`*`)** en el campo `name` para subdominios dinámicos.

* `id`: INT AUTO_INCREMENT.
* `zone_id`: INT (Foreign Key a `sys_dns_zones.id`).
* `name`: VARCHAR(255) (Ej: `@`, `www`, `mail`).
* `type`: ENUM('SOA', 'NS', 'A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV').
* `content`: VARCHAR(255) (El valor del registro, ej: `192.168.1.100`, o cadena TXT).
* `ttl`: INT DEFAULT 3600.
* `priority`: INT NULL (Solo usado para MX o SRV, ej: `10`).

### Tabla: `sys_dns_tokens`

Para autorizar a diferentes clientes, paneles o servidores satélite a usar la API.

* `id`: INT AUTO_INCREMENT.
* `token`: VARCHAR(255) UNIQUE (El Bearer Token).
* `client_name`: VARCHAR(255) (Ej. "Servidor Correo 1", "Panel cPanel Cliente A").
* `is_active`: BOOLEAN DEFAULT 1.
* `created_at`: DATETIME DEFAULT CURRENT_TIMESTAMP.

---

## 2. API Endpoints (Desarrollo en PHP)

### `POST /api-dns/add`

* **Función**: Recibe `domain` e `ip`.
* **Lógica**:
1. Validar formato de dominio y que la IP sea válida.
2. Comprobar si el dominio ya existe en `sys_dns_zones`.
3. Insertar en `sys_dns_requests` con status `pending`.
4. **Retornar**: El `id` de la solicitud (Número de seguimiento).



### `POST /api-dns/delete`

* **Función**: Recibe `domain` para su eliminación completa.
* **Lógica**: Insertar en `sys_dns_requests` con status `pending` y action `delete`.

### `POST /api-dns/record/add`

* **Función**: Añade un registro individual (ej. TXT, MX, A) a un dominio ya existente. Acepta opcionalmente un parámetro `ttl` para tener un TTL granular por registro. Especialmente diseñado para la configuración de correo:
  * **DKIM**: Inserta un registro TXT con nombre `{selector}._domainkey`. Permite múltiples entradas sin colisión.
  * **DMARC**: Inserta un registro TXT con nombre `_dmarc`. Define la política del dominio (ej. `v=DMARC1; p=reject;`). ¡El DMARC **sí** se genera o configura completamente desde esta API!
  * **SPF**: Actualiza o inserta un registro TXT en el root (`@`) que empiece por `v=spf1`. **Lógica especial**: Un dominio solo puede tener un SPF. Si se envía una nueva IP o servidor para autorizar, la API debe leer el SPF actual y hacerle un *append* (añadiendo el nuevo `ip4:` o `include:`) y regrabando el registro.
* **Validación CNAME**: Si se intenta añadir cualquier registro a un nombre que ya tiene un CNAME, o se intenta añadir un CNAME a un nombre que ya tiene otros registros, la API devolverá HTTP 400 (Regla de Oro DNS).
* **Lógica**: Insertar en `sys_dns_records` el nuevo campo y generar una petición `update` en `sys_dns_requests` para que el cron regenere el archivo de zona y haga reload.

### `POST /api-dns/record/del`

* **Función**: Elimina un registro DNS específico de un dominio (ej. un `MX` antiguo, un `A` de un subdominio o revocar un `DKIM`).
* **Lógica**:
1. Validar que el registro existe recibiendo su `id` (de `sys_dns_records`) o buscando por `domain`, `name` y `type`.
2. Para el **SPF**, aplicar lógica inversa: en lugar de borrar todo el registro de `sys_dns_records`, eliminar solo el segmento `ip4` o `include` específico y actualizar el registro. Solo borrar la fila en BD si se eliminan todos los includes/ips.
3. Eliminar la fila de `sys_dns_records` (si no es una reducción de SPF).
4. Insertar en `sys_dns_requests` con status `pending` y action `update` para que el cron regenere la zona.

### `GET /api-dns/status/{id}`

* **Función**: Consultar si el cambio se ha implementado.
* **Lógica**: Buscar el `id` y retornar el campo `status`. Si es `error`, incluir el `error_log`.

### `GET /api-dns/records/{domain}`

* **Función**: Consultar todos los registros DNS configurados para un dominio (útil para comprobar firmas DKIM, registros TXT, MX, etc.).
* **Lógica**: Buscar en `sys_dns_zones` el dominio para obtener el `zone_id`, y luego retornar todos los registros asociados en `sys_dns_records`.

### `GET /api-dns/query/{fqdn}`

* **Función**: Consulta rápida para obtener el valor directo (ej. IP o CNAME) de un subdominio o registro exacto (ej. `host9.ingeniacom.com`).
* **Lógica**: Buscar el valor correspondiente al FQDN (cruzando `sys_dns_records` con `sys_dns_zones`) y retornar directamente el campo `content` (habitualmente el tipo `A`).

---

## 3. El Proceso de Background: `/usr/local/bin/hosting/sync_dns.php`

Este script será ejecutado por el cron cada 5 minutos. Debe realizar las siguientes tareas en orden:

1. **Lectura**: Buscar en `sys_dns_requests` todos los registros con status `pending`, **ordenados por `id` ASC o `request_date` ASC** para garantizar que las operaciones sobre un mismo dominio (ej. borrar y luego añadir) se ejecuten en el orden exacto en que llegaron.
2. **Operación según `action`**:
* **Si es `add`**:
  * Crear ruta de zona en `/etc/bind/zones/db.{domain}` basado en una plantilla PHP.
  * Añadir la configuración al archivo `/etc/bind/autogenerated_zones.conf` (verificando antes que no exista).
* **Si es `delete`**:
  * Eliminar el archivo de zona `/etc/bind/zones/db.{domain}` (si existe).
  * Eliminar el bloque de configuración del dominio en `/etc/bind/autogenerated_zones.conf`.


4. **Validación de Seguridad**:
* Ejecutar: `exec("named-checkconf /etc/bind/named.conf", $output, $return_var);`
* Si `$return_var !== 0`, marcar el status como `error`, guardar el `$output` en la BD y **no hacer reload**.


5. **Aplicación de Cambios**:
* Si la validación es correcta, ejecutar: `exec("rndc reload");`
* Actualizar el status a `completed` y llenar `processed_date`.



---

## 4. Tareas Críticas para el Equipo

### Tarea 1: Ejecución como Root (`/usr/local/bin/hosting/sync_dns.php`)

El script de background `/usr/local/bin/hosting/sync_dns.php` debe ser ejecutado por el cron del usuario `root`. Por lo tanto, **NO hace falta configurar sudoers** para reiniciar el servicio BIND. El script tendrá permisos nativos para escribir en `/etc/bind/` y ejecutar `rndc reload` y `named-checkconf` directamente.

### Tarea 2: Manejo de Plantillas de Zona

Crear un archivo `template.zone.php` que contenga los registros base (SOA, NS, A, MX por defecto) para que todos los dominios nuevos salgan con la misma estructura.

### Tarea 3: Control de Concurrencia

Dado que el cron corre cada 5 minutos, si un proceso tarda más de ese tiempo, podría solaparse con el siguiente.

* **Solución**: Al inicio de `/usr/local/bin/hosting/sync_dns.php`, crear un archivo "lock" o usar `flock()` para asegurar que solo haya una instancia procesando cambios a la vez.

## 5. Instalación y Despliegue (`install.sh`)

Para asegurar la compatibilidad con el entorno de **[Lightweight-Hosting](https://github.com/m4rg4rit4/Lightweight-Hosting)** o funcionar de manera independiente, se ha habilitado un script de instalación bash (`install.sh`) que se ejecuta directamente desde GitHub (ej. vía `curl | bash`).

Este instalador tiene como objetivos:
1. **Reutilización de Recursos**: Detectar si `Lightweight-Hosting` ya está instalado leyendo `/var/www/admin_panel/config.php`. Si existe, reutiliza la base de datos `dbadmin` actual, ahorrando recursos.
2. **Instalación Condicional**: Si detecta un servidor limpio (Standalone), instala MariaDB, Apache2 y PHP-FPM con un perfil bajo de consumo de RAM (1GB) y genera automáticamente un usuario y base de datos `dbadmin`.
3. **Instalación DNS**: Descarga e instala `bind9`, creando los directorios para zonas dinámicas (`/etc/bind/zones`) e incluyendo `autogenerated_zones.conf`.
4. **Despliegue de Tablas**: Genera automáticamente el esquema de BBDD (`sys_dns_requests`, `sys_dns_zones`, `sys_dns_records`).
5. **Programación Automática**: Añade el cronjob para el worker de BIND (`sync_dns.php`) ejecutándose cada 5 minutos.
6. **Interfaz de Gestión Web**: Instala una página nativa PHP (`dns_tokens.php`) basada en Bootstrap 5, copiada a `/var/www/admin_panel` para permitir la administración gráfica completa de los tokens OAuth/Bearer.
---

## 6. Seguridad de la API

Dado que estos endpoints tienen la capacidad de alterar el ecosistema DNS al completo, es imperativo proteger su acceso:

1. **Autenticación Inteligente (Multi-Token)**: Todas las peticiones a `/api-dns/*` deben validarse mediante un Bearer token en las cabeceras (`Authorization: Bearer <TOKEN>`). La API buscará ese token en la tabla `sys_dns_tokens` de la base de datos para verificar que es válido y está activo. (Adicionalmente, se puede mantener un token maestro en `config.php`).
2. **Restricción de IP (Opcional/Recomendado)**: Limitar la ejecución de los endpoints en el servidor web para que solo acepten peticiones provenientes de las IPs conocidas.

---

## Ejemplo de Respuesta de la API (JSON)

Cuando el servidor remoto haga la petición:

```json
{
    "success": true,
    "request_id": 452,
    "status": "pending",
    "message": "La solicitud ha sido recibida y se procesará en el próximo ciclo (máx 5 min)."
}

```

¿Algún detalle específico sobre la **seguridad del API** (tokens, IPs permitidas) que quieras que añada a la lista de tareas?