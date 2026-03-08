#!/bin/bash

# ==========================================================================
# Lightweight-Hosting-DNS Installer - Complemento DNS
# Hardware Target: 1vCore, 1GB RAM
# ==========================================================================

# Colores para la salida
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

printf "${GREEN}Iniciando instalación ultra-ligera del servidor DNS (v1.0.5)...${NC}\n"

# Función de limpieza de variables
sanitize_var() {
    # Eliminar espacios, saltos de línea y limpiar restos de comentarios shell/php
    echo "$1" | tr -d '[:space:]\r\n' | sed -E 's|^//.*||; s|^#.*||'
}

# Función para prompts interactivos (compatible con curl | bash)
ask_input() {
    local prompt=$1
    local default=$2
    local var_name=$3
    local val=""
    
    # Si estamos en un pipe, forzamos lectura de /dev/tty para no consumir el script
    if [ ! -t 0 ]; then
        read -p "$prompt" val < /dev/tty
    else
        read -p "$prompt" val
    fi
    
    val=${val:-$default}
    eval "$var_name=\"$val\""
}

# Función para extraer constantes de PHP de forma robusta
get_php_const() {
    local key=$1
    local file=$2
    if [ ! -f "$file" ]; then echo ""; return; fi
    # Buscar línea de define activa (no comentada con // o #) permitiendo espacios
    local line=$(grep -iE "^\s*define\s*\(\s*['\"]$key['\"]" "$file" | head -n 1)
    if [ ! -z "$line" ]; then
        # Extraer el valor entre las segundas comillas (simple o doble)
        echo "$line" | sed -E "s/.*['\"][^'\"]*['\"]\s*,\s*['\"]([^'\"]*)['\"].*/\1/" | tr -d '\r '
    else
        echo ""
    fi
}

# 1. Verificación de usuario root
if [ "$EUID" -ne 0 ]; then 
    printf "${RED}Por favor, ejecuta como root${NC}\n"
    exit 1
fi

# 2. Comprobar si existe Lightweight-Hosting configurado
ADMIN_PATH="/var/www/admin_panel"
CONFIG_FILE="$ADMIN_PATH/config.php"
ENGINE_PATH="/usr/local/bin/hosting"
API_PATH="/var/www/api-dns"

mkdir -p "$ENGINE_PATH"
mkdir -p "$API_PATH"

if [ -f "$CONFIG_FILE" ]; then
    printf "${GREEN}Detectada instalación de Lightweight-Hosting. Reutilizando configuración.${NC}\n"
    DB_PASS=$(get_php_const "DB_PASS" "$CONFIG_FILE")
    DB_USER=$(get_php_const "DB_USER" "$CONFIG_FILE")
    DB_NAME=$(get_php_const "DB_NAME" "$CONFIG_FILE")
    ADMIN_EMAIL=$(get_php_const "ADMIN_EMAIL" "$CONFIG_FILE")
    
    # Intentar recuperar constantes específicas si ya existen
    DNS_HOSTNAME=$(get_php_const "DNS_HOSTNAME" "$CONFIG_FILE")
    DNS_DOMAIN=$(get_php_const "DNS_DOMAIN" "$CONFIG_FILE")
    
    # Limpiar lo recuperado (quitar espacios residuales)
    DB_PASS=$(sanitize_var "$DB_PASS")
    DB_USER=$(sanitize_var "$DB_USER")
    DB_NAME=$(sanitize_var "$DB_NAME")
    ADMIN_EMAIL=$(sanitize_var "$ADMIN_EMAIL")
    DNS_HOSTNAME=$(sanitize_var "$DNS_HOSTNAME")
    DNS_DOMAIN=$(sanitize_var "$DNS_DOMAIN")
    
    HAS_LWH=true
else
    printf "${YELLOW}Lightweight-Hosting no detectado. Preparando entorno...${NC}\n"
    HAS_LWH=false
    DB_USER="dbadmin"
    DB_NAME="dbadmin"
    DB_PASS=$(openssl rand -base64 18)
    ADMIN_EMAIL=""
fi

# Preparar sugerencias inteligentes para los prompts (siempre preguntar)
DNS_HOSTNAME=$(sanitize_var "$DNS_HOSTNAME")
DNS_DOMAIN=$(sanitize_var "$DNS_DOMAIN")

# Si son placeholders del template, vaciarlos para que funcionen las sugerencias
[[ "$DNS_HOSTNAME" == "{{"* ]] && DNS_HOSTNAME=""
[[ "$DNS_DOMAIN" == "{{"* ]] && DNS_DOMAIN=""

SUGGESTED_HOSTNAME=${DNS_HOSTNAME:-$(hostname -s)}
SUGGESTED_DOMAIN=${DNS_DOMAIN:-$(hostname -d)}
[ -z "$SUGGESTED_DOMAIN" ] || [ "$SUGGESTED_DOMAIN" = "." ] && SUGGESTED_DOMAIN="tu-dominio.com"

printf "${YELLOW}Configuración de Identidad del Servidor DNS:${NC}\n"
ask_input "1. Introduce el NOMBRE DEL HOST (ej: ns1) [$SUGGESTED_HOSTNAME]: " "$SUGGESTED_HOSTNAME" "DNS_HOSTNAME"
DNS_HOSTNAME=$(sanitize_var "$DNS_HOSTNAME")

ask_input "2. Introduce el DOMINIO PRINCIPAL (ej: tu-dominio.com) [$SUGGESTED_DOMAIN]: " "$SUGGESTED_DOMAIN" "DNS_DOMAIN"
DNS_DOMAIN=$(sanitize_var "$DNS_DOMAIN" | sed 's/^\.//')

FULL_FQDN="${DNS_HOSTNAME}.${DNS_DOMAIN}"
printf "${GREEN}FQDN configurado como: ${YELLOW}$FULL_FQDN${NC}\n"

# 2.1 Aplicar Identidad al Sistema
printf "${YELLOW}Aplicando identidad al servidor...${NC}\n"
hostnamectl set-hostname "$FULL_FQDN"
if grep -q "127.0.1.1" /etc/hosts; then
    sed -i "s/127.0.1.1.*/127.0.1.1\t$FULL_FQDN\t$DNS_HOSTNAME/" /etc/hosts
else
    printf "127.0.1.1\t$FULL_FQDN\t$DNS_HOSTNAME\n" >> /etc/hosts
fi

# Configuración de Email (siempre preguntar)
DEFAULT_EMAIL=${ADMIN_EMAIL:-"admin@$DNS_DOMAIN"}
ask_input "3. Introduce el email del administrador [$DEFAULT_EMAIL]: " "$DEFAULT_EMAIL" "ADMIN_EMAIL"
ADMIN_EMAIL=$(sanitize_var "$ADMIN_EMAIL")

# Sincronizar constantes de email
DNS_ADMIN_EMAIL=$ADMIN_EMAIL
LETSENCRYPT_EMAIL=$ADMIN_EMAIL
printf "\n"

# 3. Instalación de paquetes y optimización de recursos (Target: 1GB RAM)
printf "${YELLOW}Verificando dependencias del sistema y optimizando recursos...${NC}\n"
apt update -y
apt install -y curl git unzip cron certbot python3-certbot-apache dnsutils ufw

# 3.1 Optimización MariaDB (Low Memory Profile)
if ! command -v mariadb >/dev/null 2>&1; then
    printf "${YELLOW}Instalando MariaDB con perfil de bajo consumo...${NC}\n"
    apt install -y mariadb-server
    
    mkdir -p /etc/mysql/mariadb.conf.d/
    cat <<EOF > /etc/mysql/mariadb.conf.d/99-low-memory.cnf
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
    
    ROOT_DB_PASS_FILE="/root/.hosting_db_root"
    if [ -f "$ROOT_DB_PASS_FILE" ]; then
        DB_ROOT_PASS=$(cat "$ROOT_DB_PASS_FILE")
    else
        DB_ROOT_PASS=$(openssl rand -base64 24)
        echo "$DB_ROOT_PASS" > "$ROOT_DB_PASS_FILE"
        chmod 600 "$ROOT_DB_PASS_FILE"
    fi
    
    if mariadb -e "status" >/dev/null 2>&1; then
        mariadb -e "ALTER USER 'root'@'localhost' IDENTIFIED BY '$DB_ROOT_PASS';"
    fi
    
    cat <<EOF > /root/.my.cnf
[client]
user=root
password=$DB_ROOT_PASS
EOF
    chmod 600 /root/.my.cnf
    
    mariadb -e "CREATE DATABASE IF NOT EXISTS \`${DB_NAME}\`;"
    mariadb -e "DROP USER IF EXISTS '${DB_USER}'@'127.0.0.1';"
    mariadb -e "CREATE USER '${DB_USER}'@'127.0.0.1' IDENTIFIED BY '${DB_PASS}';"
    mariadb -e "GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${DB_USER}'@'127.0.0.1';"
    mariadb -e "FLUSH PRIVILEGES;"
    rm -f /root/.my.cnf
fi

# Generar config.php mínimo para la API standalone
if [ "$HAS_LWH" = false ]; then
    mkdir -p "$ADMIN_PATH"
    cat <<EOF > "$CONFIG_FILE"
<?php
define('DB_HOST', 'localhost');
define('DB_NAME', '$DB_NAME');
define('DB_USER', '$DB_USER');
define('DB_PASS', '$DB_PASS');
define('ADMIN_EMAIL', '$ADMIN_EMAIL');
define('DNS_HOSTNAME', '$DNS_HOSTNAME');
define('DNS_DOMAIN', '$DNS_DOMAIN');
define('DNS_ADMIN_EMAIL', '$DNS_ADMIN_EMAIL');
define('LETSENCRYPT_EMAIL', '$LETSENCRYPT_EMAIL');

function getPDO() {
    static \$pdo;
    if (!\$pdo) {
        \$pdo = new PDO("mysql:host=" . DB_HOST . ";dbname=" . DB_NAME, DB_USER, DB_PASS, [
            PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
            PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC
        ]);
    }
    return \$pdo;
}
?>
EOF
fi

# 3.1 Actualizar/Enriquecer config.php con parámetros DNS y Let's Encrypt
if [ "$HAS_LWH" = true ]; then
    printf "${YELLOW}Actualizando configuración en config.php...${NC}\n"
    
    # Función interna para actualizar o añadir constante en PHP
    update_php_const() {
        local key=$1
        local val=$2
        # Si la constante ya existe (comentada o no, con cualquier espacio/comilla)
        if grep -iqE "define\s*\(\s*['\"]$key['\"]" "$CONFIG_FILE"; then
            # Reemplazar la línea que contiene la definición (comentada o no) por una definición limpia y activa
            sed -i "s|^.*define\s*(\s*['\"]$key['\"]\s*,.*|define('$key', '$val');|gI" "$CONFIG_FILE"
        else
            # Si no existe, intentar añadir antes de ?> (considerando espacios antes del ?>)
            if grep -q "?>" "$CONFIG_FILE"; then
                sed -i "s|\s*?>|define('$key', '$val');\n?>|" "$CONFIG_FILE"
            else
                # Si no hay ?>, simplemente añadir al final
                echo "define('$key', '$val');" >> "$CONFIG_FILE"
            fi
        fi
    }

    update_php_const "DNS_HOSTNAME" "$DNS_HOSTNAME"
    update_php_const "DNS_DOMAIN" "$DNS_DOMAIN"
    update_php_const "DNS_ADMIN_EMAIL" "$DNS_ADMIN_EMAIL"
    update_php_const "LETSENCRYPT_EMAIL" "$LETSENCRYPT_EMAIL"
fi

# 3.2 Optimización Web (Apache2 + PHP-FPM)
if ! command -v apache2 >/dev/null 2>&1; then
    printf "${YELLOW}Instalando Apache2 y PHP-FPM (MPM Event)...${NC}\n"
    apt install -y apache2 php-fpm php-cli php-mysql php-curl php-gd php-mbstring php-xml
    
    # Configurar MPM Event para bajo consumo (1GB RAM)
    cat <<EOF > /etc/apache2/mods-available/mpm_event.conf
<IfModule mpm_event_module>
    StartServers             1
    MinSpareThreads          5
    MaxSpareThreads         10
    ThreadLimit             64
    ThreadsPerChild         20
    MaxRequestWorkers       40
    MaxConnectionsPerChild   1000
</IfModule>
EOF
    
    # Activar módulos esenciales
    a2dismod mpm_prefork 2>/dev/null || true
    a2enmod mpm_event proxy_fcgi setenvif rewrite ssl http2
    
    # Optimizar PHP-FPM (Modo OnDemand)
    PHP_VER=$(ls /etc/php/ | grep -E '^[0-9.]+$' | head -n 1)
    if [ ! -z "$PHP_VER" ]; then
        POOL_FILE="/etc/php/${PHP_VER}/fpm/pool.d/www.conf"
        if [ -f "$POOL_FILE" ]; then
            sed -i 's/^pm = dynamic/pm = ondemand/' $POOL_FILE
            sed -i 's/^pm.max_children = 5/pm.max_children = 10/' $POOL_FILE
            sed -i 's/^;pm.process_idle_timeout = 10s;/pm.process_idle_timeout = 30s;/' $POOL_FILE
        fi
        systemctl restart php${PHP_VER}-fpm
    fi
    
    systemctl restart apache2
fi

# 3.3 Configurar VirtualHost si es Stand-alone (No hay Lightweight-Hosting)
if [ "$HAS_LWH" = false ]; then
    printf "${YELLOW}Configurando VirtualHost para DNS API (Stand-alone)...${NC}\n"
    
    # Detección del socket de PHP
    REAL_PHP_SOCKET=$(ls /run/php/php*-fpm.sock 2>/dev/null | head -n 1)
    
    cat <<EOF > /etc/apache2/sites-available/dns-api.conf
<VirtualHost *:80>
    ServerName $DNS_HOSTNAME.$DNS_DOMAIN
    DocumentRoot /var/www/api-dns
    DirectoryIndex index.php

    <Directory /var/www/api-dns>
        Options -Indexes +FollowSymLinks
        AllowOverride All
        Require all granted
        <FilesMatch \.php$>
            SetHandler "proxy:unix:$REAL_PHP_SOCKET|fcgi://localhost/"
        </FilesMatch>
    </Directory>

    # Alias para el panel de tokens
    Alias /admin /var/www/admin_panel
    <Directory /var/www/admin_panel>
        Options -Indexes +FollowSymLinks
        AllowOverride All
        Require all granted
        <FilesMatch \.php$>
            SetHandler "proxy:unix:$REAL_PHP_SOCKET|fcgi://localhost/"
        </FilesMatch>
    </Directory>
</VirtualHost>
EOF

    a2ensite dns-api.conf
    a2dissite 000-default.conf
    systemctl restart apache2
fi

# 4. Instalación del Servidor DNS (BIND9)
printf "${YELLOW}Instalando BIND9...${NC}\n"
apt install -y bind9 bind9utils bind9-doc dnsutils

# Configurar autoloader de zonas
BIND_ZONES_DIR="/etc/bind/zones"
AUTO_ZONES_FILE="/etc/bind/autogenerated_zones.conf"

mkdir -p "$BIND_ZONES_DIR"
touch "$AUTO_ZONES_FILE"
chown -R bind:bind "$BIND_ZONES_DIR"

# Incluir zonas dinámicas en BIND si no está ya
if ! grep -q "autogenerated_zones.conf" /etc/bind/named.conf.local; then
    echo 'include "/etc/bind/autogenerated_zones.conf";' >> /etc/bind/named.conf.local
fi

# 5. Desplegar Tablas DNS
printf "${YELLOW}Desplegando esquema DNS en MariaDB...${NC}\n"

mariadb -h 127.0.0.1 -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" <<EOF
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
    FOREIGN KEY (zone_id) REFERENCES sys_dns_zones(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS sys_dns_tokens (
    id INT AUTO_INCREMENT PRIMARY KEY,
    token VARCHAR(255) UNIQUE NOT NULL,
    client_name VARCHAR(255) NOT NULL,
    is_active BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
EOF

# Generar y mostrar un primer token maestro para la API
MASTER_TOKEN=$(openssl rand -hex 16)
mariadb -h 127.0.0.1 -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" -e "INSERT IGNORE INTO sys_dns_tokens (token, client_name) VALUES ('$MASTER_TOKEN', 'Master Admin Token');"

# 6. Descarga de archivos desde GitHub
printf "${YELLOW}Descargando archivos del servidor DNS desde GitHub...${NC}\n"

TEMP_DIR=$(mktemp -d /tmp/dns_XXXXXX)
REPO_RAW="https://raw.githubusercontent.com/m4rg4rit4/Lightweight-Hosting-DNS/main"

# Descargar archivos a /tmp primero
curl -sSL "$REPO_RAW/src/admin/dns_tokens.php" -o "$TEMP_DIR/dns_tokens.php"
curl -sSL "$REPO_RAW/src/api-dns/index.php" -o "$TEMP_DIR/index.php"
curl -sSL "$REPO_RAW/src/engine/sync_dns.php" -o "$TEMP_DIR/sync_dns.php"
curl -sSL "$REPO_RAW/src/engine/template.zone.php" -o "$TEMP_DIR/template.zone.php"

if [ ! -f "$TEMP_DIR/dns_tokens.php" ] || [ ! -f "$TEMP_DIR/index.php" ] || [ ! -f "$TEMP_DIR/sync_dns.php" ]; then
    printf "${RED}Error: No se pudieron descargar los archivos esenciales desde GitHub.${NC}\n"
    rm -rf "$TEMP_DIR"
    exit 1
fi

# Mover archivos a su destino final
cp "$TEMP_DIR/dns_tokens.php" "$ADMIN_PATH/dns_tokens.php"
cp "$TEMP_DIR/index.php" "$API_PATH/index.php"
cp "$TEMP_DIR/sync_dns.php" "$ENGINE_PATH/sync_dns.php"
cp "$TEMP_DIR/template.zone.php" "$ENGINE_PATH/template.zone.php"

# Permisos y limpieza
chown -R www-data:www-data "$ADMIN_PATH" "$API_PATH"
chmod 644 "$ADMIN_PATH/dns_tokens.php" "$API_PATH/index.php" "$ENGINE_PATH/template.zone.php"
chmod 700 "$ENGINE_PATH/sync_dns.php"
rm -rf "$TEMP_DIR"

# 7. Configurar Cron Job
printf "${YELLOW}Configurando Cron Job para resolver DNS...${NC}\n"
CRON_SCRIPT="$ENGINE_PATH/sync_dns.php"

if ! crontab -l 2>/dev/null | grep -q "sync_dns.php"; then
    (crontab -l 2>/dev/null; echo "*/5 * * * * /usr/bin/php $CRON_SCRIPT > /dev/null 2>&1") | crontab -
    printf "${GREEN}Cron de sync_dns configurado.${NC}\n"
fi

# 8. Activar Configuraciones de BIND
systemctl restart bind9

printf "${GREEN}====================================================${NC}\n"
printf "${GREEN} INSTALACIÓN DNS COMPLETADA CON ÉXITO${NC}\n"
printf "${GREEN}====================================================${NC}\n"
if [ "$HAS_LWH" = false ]; then
    printf "${YELLOW}Stand-alone Setup: DB dbadmin guardada con pass: $DB_PASS${NC}\n"
fi
printf "${YELLOW}API Master Token: ${GREEN}$MASTER_TOKEN${NC}\n"
printf "${GREEN}Los scripts han sido desplegados automáticamente en $ENGINE_PATH y $API_PATH${NC}\n"
printf "${GREEN}====================================================${NC}\n"
