# 🌩️ Lightweight-Hosting-DNS

**Servidor DNS ultra-ligero basado en BIND9** diseñado específicamente para operar en entornos de **recursos limitados** (1 vCore, 1GB RAM). Forma parte del ecosistema [Lightweight-Hosting](https://github.com/m4rg4rit4/Lightweight-Hosting).

---

## 🚀 Características Principales

- **⚙️ Optimización Extrema**: Configurado para usar el mínimo de RAM posible mediante PHP nativo con PDO puro y límites estrictos en BIND9.
- **🔌 API Restfull Nativa**: Endpoints ultrarápidos para la gestión dinámica de dominios y registros (A, CNAME, MX, TXT, SPF, DKIM, etc).
- **🔒 Seguridad por Token**: Sistema de autenticación Bearer Token gestionable desde el panel.
- **🔄 Sync Automática**: Worker en background (`sync_dns.php`) que procesa las peticiones de cambio en BIND9 cada 5 minutos de forma segura.
- **💉 Inyección Inteligente de SPF/DKIM**: Lógica automatizada para añadir servidores autorizados a registros SPF existentes sin duplicarlos.
- **🌐 Dual Mode**: Puede funcionar como un módulo de Lightweight-Hosting o como un servidor DNS Standalone.

## 🛠️ Requisitos del Sistema

- **S.O.**: Debian 11/12 o Ubuntu 22.04+ (Recomendado).
- **Hardware**: Mínimo 1 Core CPU y 1GB RAM.
- **Servicios**: MariaDB/MySQL, BIND9, Apache/Nginx + PHP-FPM.

## 📦 Instalación Rápida

Para desplegar el sistema completo en pocos segundos, ejecuta el siguiente comando como **root**:

```bash
curl -sS https://raw.githubusercontent.com/m4rg4rit4/Lightweight-Hosting-DNS/main/install.sh | bash
```

> **Aviso**: El instalador detectará si `Lightweight-Hosting` ya está presente en el servidor. Si es así, compartirá la base de datos `dbadmin` y se integrará en el panel central.

## 📡 Gestión de la API

La API se encuentra en el directorio `/api-dns/` y requiere autenticación Bearer.

### Endpoints Disponibles:

| Método | Endpoint | Descripción |
| :--- | :--- | :--- |
| `POST` | `/api-dns/add` | Registra un nuevo dominio en el cluster DNS. |
| `POST` | `/api-dns/delete` | Elimina una zona DNS completa y sus archivos asociados. |
| `POST` | `/api-dns/record/add` | Añade manualmente registros (tipo A, TXT, MX, SPF...). |
| `GET` | `/api-dns/status/{id}` | Consulta el estado de procesamiento de una tarea. |
| `GET` | `/api-dns/records/{domain}` | Lista todos los registros configurados para un dominio. |

## 📐 Arquitectura

El proyecto está estructurado de la siguiente manera:

- `src/api-dns/`: Scripts PHP de la API REST.
- `src/engine/`: Worker de background (`sync_dns.php`) que interactúa con BIND9.
- `src/admin/`: Interfaz web (`dns_tokens.php`) para la gestión administrativa de tokens.
- `install.sh`: Script de auto-despliegue y configuración del sistema.

## 📄 Licencia

Este proyecto es software libre bajo la licencia MIT.

---

Desarrollado con ❤️ para el proyecto [Lightweight-Hosting](https://github.com/m4rg4rit4/Lightweight-Hosting).
