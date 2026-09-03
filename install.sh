#!/bin/bash

# ==========================================================================
# Lightweight-Hosting-DNS Installer - Nodo DNS (arquitectura Go + autocert)
#
# El binario Go sirve la API por HTTPS en :8090 y gestiona el certificado
# (Let's Encrypt) él mismo, renovándolo en caliente. NO usa Apache, NI certbot,
# NI PHP: escucha :80 (reto ACME + redirección), :8080 (redirección) y :8090.
# Las credenciales de la BD van en un EnvironmentFile, no en config.php.
# Hardware target: 1vCore, 1GB RAM.
# ==========================================================================

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'

# Modo no interactivo
UPDATE_MODE=false
if [[ " $* " == *" /update "* ]] || [[ " $* " == *" /silent "* ]]; then
    UPDATE_MODE=true
    printf "${YELLOW}>>> MODO ACTUALIZACIÓN/SILENCIOSO: instalación no interactiva.${NC}\n"
fi
[[ " $* " == *" /config "* ]] && UPDATE_MODE=false

VERSION=$( [ -f VERSION ] && cat VERSION || echo "2.3.0" )
printf "${GREEN}Instalación del nodo DNS Lightweight (v$VERSION) — Go + autocert${NC}\n"

sanitize_var() { echo "$1" | tr -d '[:space:]\r\n' | sed -E 's|^//.*||; s|^#.*||'; }

ask_input() {
    local prompt=$1 default=$2 var_name=$3 val=""
    if [ "$UPDATE_MODE" = false ]; then
        if [ ! -t 0 ]; then read -p "$prompt" val < /dev/tty; else read -p "$prompt" val; fi
    fi
    val=${val:-$default}
    eval "$var_name=\"$val\""
}

# Lee una constante PHP (solo para reutilizar la BD si coexiste Lightweight-Hosting)
get_php_const() {
    local key=$1 file=$2
    [ -f "$file" ] || { echo ""; return; }
    local line=$(grep -iE "^\s*define\s*\(\s*['\"]$key['\"]" "$file" | head -n1)
    [ -n "$line" ] && echo "$line" | sed -E "s/.*['\"][^'\"]*['\"]\s*,\s*['\"]([^'\"]*)['\"].*/\1/" | tr -d '\r ' || echo ""
}

# Lee una variable de un EnvironmentFile ya existente (reinstalación/actualización)
get_env_var() {
    local key=$1 file=$2
    [ -f "$file" ] || { echo ""; return; }
    grep -E "^$key=" "$file" | head -n1 | cut -d= -f2-
}

[ "$EUID" -ne 0 ] && { printf "${RED}Ejecuta como root.${NC}\n"; exit 1; }

ENV_FILE="/etc/lwh-dns/lwh-dns.env"
LWH_CONFIG="/var/www/admin_panel/config.php"   # config.php del panel de Hosting, si coexiste
BIN_DEST="/usr/local/bin/lwh-dns-v2"
CACHE_DIR="/var/lib/lwh-dns/autocert"

# ---------------------------------------------------------------------------
# 1. Credenciales de la BD: reutilizar EnvironmentFile > config.php de Hosting > generar
# ---------------------------------------------------------------------------
if [ -f "$ENV_FILE" ]; then
    printf "${GREEN}EnvironmentFile existente: reutilizo las credenciales de BD.${NC}\n"
    DB_USER=$(get_env_var DB_USER "$ENV_FILE"); DB_PASS=$(get_env_var DB_PASS "$ENV_FILE")
    DB_NAME=$(get_env_var DB_NAME "$ENV_FILE"); DB_HOST=$(get_env_var DB_HOST "$ENV_FILE")
elif [ -f "$LWH_CONFIG" ]; then
    printf "${GREEN}Detectado Lightweight-Hosting: comparto su base de datos.${NC}\n"
    DB_USER=$(sanitize_var "$(get_php_const DB_USER "$LWH_CONFIG")")
    DB_PASS=$(sanitize_var "$(get_php_const DB_PASS "$LWH_CONFIG")")
    DB_NAME=$(sanitize_var "$(get_php_const DB_NAME "$LWH_CONFIG")")
else
    DB_USER="dbadmin"; DB_NAME="dbadmin"; DB_PASS=$(openssl rand -base64 18)
fi
[ -z "$DB_HOST" ] && DB_HOST="127.0.0.1"

# ---------------------------------------------------------------------------
# 2. Identidad del nodo (el FQDN es el nombre para el que se emite el certificado)
# ---------------------------------------------------------------------------
PREV_FQDN=$(get_env_var TLS_HOSTS "$ENV_FILE")
if [ -n "$PREV_FQDN" ]; then
    FULL_FQDN="$PREV_FQDN"
    printf "${GREEN}FQDN reutilizado del EnvironmentFile: ${YELLOW}$FULL_FQDN${NC}\n"
else
    SUGGESTED_HOSTNAME=$(hostname -s); SUGGESTED_DOMAIN=$(hostname -d)
    { [ -z "$SUGGESTED_DOMAIN" ] || [ "$SUGGESTED_DOMAIN" = "." ]; } && SUGGESTED_DOMAIN="tu-dominio.com"
    printf "${YELLOW}Identidad del servidor DNS:${NC}\n"
    ask_input "1. NOMBRE DEL HOST (ej: ns1) [$SUGGESTED_HOSTNAME]: " "$SUGGESTED_HOSTNAME" "DNS_HOSTNAME"
    ask_input "2. DOMINIO PRINCIPAL (ej: tu-dominio.com) [$SUGGESTED_DOMAIN]: " "$SUGGESTED_DOMAIN" "DNS_DOMAIN"
    DNS_HOSTNAME=$(sanitize_var "$DNS_HOSTNAME")
    DNS_DOMAIN=$(sanitize_var "$DNS_DOMAIN" | sed 's/^\.//')
    FULL_FQDN="${DNS_HOSTNAME}.${DNS_DOMAIN}"
    printf "${GREEN}FQDN configurado como: ${YELLOW}$FULL_FQDN${NC}\n"
fi

DEFAULT_EMAIL="admin@${FULL_FQDN#*.}"
ask_input "3. Email del administrador (avisos de Let's Encrypt) [$DEFAULT_EMAIL]: " "$DEFAULT_EMAIL" "ADMIN_EMAIL"
ADMIN_EMAIL=$(sanitize_var "$ADMIN_EMAIL")

# Aplicar identidad al sistema
hostnamectl set-hostname "$FULL_FQDN" 2>/dev/null || true
if grep -q "127.0.1.1" /etc/hosts; then
    sed -i "s/127.0.1.1.*/127.0.1.1\t$FULL_FQDN\t${FULL_FQDN%%.*}/" /etc/hosts
else
    printf "127.0.1.1\t$FULL_FQDN\t${FULL_FQDN%%.*}\n" >> /etc/hosts
fi

# ---------------------------------------------------------------------------
# 3. Paquetes (sin apache, sin php, sin certbot)
# ---------------------------------------------------------------------------
printf "${YELLOW}Instalando dependencias...${NC}\n"
apt update -y
[ "$UPDATE_MODE" = true ] && apt upgrade -y
apt install -y curl dnsutils ufw bind9 bind9utils

# 3.1 MariaDB con perfil de bajo consumo
if ! command -v mariadb >/dev/null 2>&1; then
    printf "${YELLOW}Instalando MariaDB (perfil de bajo consumo)...${NC}\n"
    apt install -y mariadb-server
    mkdir -p /etc/mysql/mariadb.conf.d/
    cat > /etc/mysql/mariadb.conf.d/99-low-memory.cnf <<EOF
[mysqld]
performance_schema = OFF
innodb_buffer_pool_size = 128M
innodb_log_file_size = 32M
max_connections = 20
key_buffer_size = 8M
thread_cache_size = 4
query_cache_size = 0
query_cache_type = 0
EOF
    systemctl restart mariadb
fi

# 3.2 BD y usuario (idempotente). La BD escucha solo en loopback.
if mariadb -e "status" >/dev/null 2>&1; then
    mariadb -e "CREATE DATABASE IF NOT EXISTS \`${DB_NAME}\`;"
    mariadb -e "CREATE USER IF NOT EXISTS '${DB_USER}'@'127.0.0.1' IDENTIFIED BY '${DB_PASS}';"
    mariadb -e "ALTER USER '${DB_USER}'@'127.0.0.1' IDENTIFIED BY '${DB_PASS}';"
    mariadb -e "GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${DB_USER}'@'127.0.0.1'; FLUSH PRIVILEGES;"
fi

# ---------------------------------------------------------------------------
# 4. Esquema DNS en MariaDB (incluye las columnas de alcance de tokens)
# ---------------------------------------------------------------------------
printf "${YELLOW}Desplegando el esquema DNS...${NC}\n"
mariadb -h 127.0.0.1 -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" <<'SQLEOF'
CREATE TABLE IF NOT EXISTS sys_dns_requests (
    id INT AUTO_INCREMENT PRIMARY KEY,
    action ENUM('add', 'delete') NOT NULL,
    domain VARCHAR(255) NOT NULL,
    target_ip VARCHAR(45) NULL,
    status ENUM('pending', 'processing', 'completed', 'error') DEFAULT 'pending',
    request_date DATETIME DEFAULT CURRENT_TIMESTAMP,
    processed_date DATETIME NULL,
    error_log TEXT NULL
);
CREATE TABLE IF NOT EXISTS sys_dns_zones (
    id INT AUTO_INCREMENT PRIMARY KEY,
    domain VARCHAR(255) UNIQUE NOT NULL,
    zone_file_path VARCHAR(255) NOT NULL,
    is_active BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sys_dns_records (
    id INT AUTO_INCREMENT PRIMARY KEY,
    zone_id INT NOT NULL,
    name VARCHAR(255) NOT NULL,
    type ENUM('SOA', 'NS', 'A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV') NOT NULL,
    content VARCHAR(255) NOT NULL,
    ttl INT DEFAULT 3600,
    priority INT NULL,
    sort_order INT DEFAULT 0,
    FOREIGN KEY (zone_id) REFERENCES sys_dns_zones(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS sys_dns_tokens (
    id INT AUTO_INCREMENT PRIMARY KEY,
    token VARCHAR(64) NOT NULL UNIQUE,
    client_name VARCHAR(128) NOT NULL,
    is_active TINYINT(1) DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sys_ddns_clients (
    id INT AUTO_INCREMENT PRIMARY KEY,
    token VARCHAR(64) NOT NULL UNIQUE,
    hostname VARCHAR(128) NOT NULL,
    zone_id INT DEFAULT NULL,
    record_name VARCHAR(255) DEFAULT NULL,
    status ENUM('pending', 'approved') DEFAULT 'pending',
    last_ip VARCHAR(45) DEFAULT NULL,
    last_seen DATETIME DEFAULT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (zone_id) REFERENCES sys_dns_zones(id) ON DELETE CASCADE
);

-- Migraciones idempotentes (el binario aplica lo mismo en migrate() al arrancar).
ALTER TABLE sys_dns_records ADD COLUMN IF NOT EXISTS sort_order INT DEFAULT 0;
ALTER TABLE sys_dns_requests ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
ALTER TABLE sys_dns_requests ADD COLUMN IF NOT EXISTS started_at DATETIME NULL;
ALTER TABLE sys_dns_requests ADD INDEX IF NOT EXISTS idx_status (status);
ALTER TABLE sys_dns_zones ADD COLUMN IF NOT EXISTS server_slug VARCHAR(64) NULL;
ALTER TABLE sys_dns_zones ADD INDEX IF NOT EXISTS idx_server_slug (server_slug);

-- Alcance de tokens: un token puede limitarse a una zona, un nombre y unos tipos, y
-- perder la gestión de zonas. NULL = sin límite (token maestro de siempre).
ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_zone VARCHAR(255) NULL;
ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_name VARCHAR(255) NULL;
ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS scope_types VARCHAR(128) NULL;
ALTER TABLE sys_dns_tokens ADD COLUMN IF NOT EXISTS can_manage_zones TINYINT(1) NOT NULL DEFAULT 1;
SQLEOF

# 4.1 Token maestro de la API (se genera una vez)
EXISTING_MASTER=$(mariadb -h 127.0.0.1 -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" -ss -e "SELECT token FROM sys_dns_tokens WHERE client_name = 'Master Admin Token' LIMIT 1;")
if [ -n "$EXISTING_MASTER" ]; then
    MASTER_TOKEN="$EXISTING_MASTER"
    printf "${YELLOW}Token maestro existente recuperado.${NC}\n"
else
    MASTER_TOKEN=$(openssl rand -hex 16)
    mariadb -h 127.0.0.1 -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" -e "INSERT INTO sys_dns_tokens (token, client_name) VALUES ('$MASTER_TOKEN', 'Master Admin Token');"
    printf "${GREEN}Nuevo token maestro generado.${NC}\n"
fi

# ---------------------------------------------------------------------------
# 5. BIND9 (autoloader de zonas)
# ---------------------------------------------------------------------------
printf "${YELLOW}Configurando BIND9...${NC}\n"
BIND_ZONES_DIR="/etc/bind/zones"
AUTO_ZONES_FILE="/etc/bind/autogenerated_zones.conf"
mkdir -p "$BIND_ZONES_DIR"
touch "$AUTO_ZONES_FILE"
chown -R bind:bind "$BIND_ZONES_DIR" 2>/dev/null || true
grep -q "autogenerated_zones.conf" /etc/bind/named.conf.local 2>/dev/null || \
    echo 'include "/etc/bind/autogenerated_zones.conf";' >> /etc/bind/named.conf.local

# ---------------------------------------------------------------------------
# 6. Binario del servidor (subida manual: el repo puede ser privado)
# ---------------------------------------------------------------------------
if [ ! -x "$BIN_DEST" ]; then
    printf "${RED}Falta el binario en $BIN_DEST.${NC}\n"
    printf "${YELLOW}Súbelo manualmente al servidor (scp/sftp) a $BIN_DEST, dale permisos\n"
    printf "   chmod +x $BIN_DEST\n"
    printf "y vuelve a ejecutar este instalador.${NC}\n"
    exit 1
fi
chmod +x "$BIN_DEST"; chown root:root "$BIN_DEST"

# ---------------------------------------------------------------------------
# 7. EnvironmentFile (BD + configuración de TLS/autocert). Sustituye a config.php.
# ---------------------------------------------------------------------------
printf "${YELLOW}Escribiendo $ENV_FILE...${NC}\n"
mkdir -p /etc/lwh-dns && chmod 700 /etc/lwh-dns
cat > "$ENV_FILE" <<EOF
# Credenciales de la base de datos del nodo DNS (rota aquí la contraseña).
DB_USER=$DB_USER
DB_PASS=$DB_PASS
DB_HOST=$DB_HOST
DB_NAME=$DB_NAME
# TLS gestionado por el propio binario (Let's Encrypt vía autocert): sin Apache ni certbot.
# El binario escucha :80 (reto ACME + redirección), :8080 (redirección) y :8090 (API HTTPS).
TLS_AUTOCERT=1
TLS_HOSTS=$FULL_FQDN
TLS_CACHE_DIR=$CACHE_DIR
EOF
chmod 600 "$ENV_FILE"
mkdir -p "$CACHE_DIR" && chmod 700 "$CACHE_DIR"

# ---------------------------------------------------------------------------
# 8. Firewall. El 80 es imprescindible para emitir/renovar el certificado (HTTP-01).
# ---------------------------------------------------------------------------
printf "${YELLOW}Configurando firewall (ufw)...${NC}\n"
ufw allow 22/tcp    >/dev/null 2>&1   # SSH (permitir ANTES de habilitar ufw)
ufw allow 53        >/dev/null 2>&1   # DNS
ufw allow 80/tcp    >/dev/null 2>&1   # reto ACME de Let's Encrypt (obligatorio)
ufw allow 8080/tcp  >/dev/null 2>&1   # HTTP -> redirección a HTTPS
# El 8090 es la API. Para limitarla a tu rango, sustituye la línea de abajo por, p. ej.:
#   ufw allow from 203.0.113.0/24 to any port 8090 proto tcp
ufw allow 8090/tcp  >/dev/null 2>&1   # API HTTPS
yes | ufw enable    >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# 9. Servicio systemd (arranca el binario, que escucha 80/8080/8090 como root)
# ---------------------------------------------------------------------------
printf "${YELLOW}Configurando el servicio lwh-dns...${NC}\n"
cat > /etc/systemd/system/lwh-dns.service <<EOF
[Unit]
Description=Lightweight DNS v2 API Engine
After=network.target mariadb.service

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
Restart=always
RestartSec=5
ExecStart=$BIN_DEST
WorkingDirectory=/var/lib/lwh-dns

[Install]
WantedBy=multi-user.target
EOF
mkdir -p /var/lib/lwh-dns
systemctl daemon-reload
systemctl enable lwh-dns
systemctl restart lwh-dns
systemctl restart bind9 2>/dev/null || systemctl restart named 2>/dev/null || true

# ---------------------------------------------------------------------------
# 10. Aviso sobre la emisión del certificado
# ---------------------------------------------------------------------------
PUBLIC_IP=$(curl -s https://api.ipify.org 2>/dev/null)
FQDN_IP=$(dig @8.8.8.8 +short "$FULL_FQDN" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | tail -n1)
if [ -n "$PUBLIC_IP" ] && [ "$PUBLIC_IP" != "$FQDN_IP" ]; then
    printf "${YELLOW}Aviso: $FULL_FQDN resuelve a '${FQDN_IP:-nada}' pero la IP de este servidor es '$PUBLIC_IP'.\n"
    printf "El certificado no se emitirá hasta que el DNS apunte aquí y el puerto 80 sea accesible desde Internet.${NC}\n"
fi

printf "${GREEN}====================================================${NC}\n"
printf "${GREEN} NODO DNS INSTALADO — Go + autocert${NC}\n"
printf "  API:          https://$FULL_FQDN:8090/api-dns\n"
printf "  Master token: ${GREEN}$MASTER_TOKEN${NC}\n"
printf "  El certificado TLS se emite y renueva solo (Let's Encrypt), sin Apache ni certbot.\n"
printf "${GREEN}====================================================${NC}\n"
