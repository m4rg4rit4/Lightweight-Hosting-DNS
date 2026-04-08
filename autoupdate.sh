#!/bin/bash
# Autoupdate script for Lightweight-DNS

REPO_RAW="https://raw.githubusercontent.com/m4rg4rit4/Lightweight-Hosting-DNS/main"
VERSION_LOCK="/var/www/admin_panel/config.php"

# Colores
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

# Obtener version local desde config.php
if [ -f "$VERSION_LOCK" ]; then
    LOCAL_VERSION=$(grep "SYSTEM_VERSION" "$VERSION_LOCK" | cut -d"'" -f4)
else
    # Fallback: intentamos leer VERSION si existe localmente (en una actualización previa)
    if [ -f "/usr/local/bin/hosting/VERSION" ]; then
        LOCAL_VERSION=$(cat "/usr/local/bin/hosting/VERSION")
    else
        LOCAL_VERSION="1.2.18"
    fi
fi

# Obtener version remota desde GitHub
REMOTE_VERSION=$(curl -sSL "$REPO_RAW/VERSION" | tr -d '[:space:]')

if [ -z "$REMOTE_VERSION" ]; then
    echo -e "${RED}Error: No se pudo obtener la versión remota.${NC}"
    exit 1
fi

echo -e "${YELLOW}Comprobando actualizaciones de Lightweight-DNS...${NC}"
echo -e "Versión local:  ${GREEN}$LOCAL_VERSION${NC}"
echo -e "Versión remota: ${GREEN}$REMOTE_VERSION${NC}"

# Comparar versiones (si son diferentes, actualizar)
if [ "$LOCAL_VERSION" != "$REMOTE_VERSION" ]; then
    echo -e "${YELLOW}¡Nueva versión detectada! Iniciando actualización...${NC}"
    
    TEMP_INSTALL="/tmp/install_dns_update.sh"
    curl -sSL "$REPO_RAW/install.sh" -o "$TEMP_INSTALL"
    
    if [ -f "$TEMP_INSTALL" ]; then
        bash "$TEMP_INSTALL" /update
        echo -e "${GREEN}Actualización completada a la versión $REMOTE_VERSION.${NC}"
    else
        echo -e "${RED}Error al descargar el instalador.${NC}"
    fi
else
    echo -e "${GREEN}El sistema ya está actualizado.${NC}"
fi
