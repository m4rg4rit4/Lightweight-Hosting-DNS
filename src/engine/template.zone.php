<?php
/**
 * Plantilla de Zona DNS (BIND9)
 * Variables esperadas:
 * $domain (string)
 * $serial (formato YYYYMMDDNN)
 * $records (array de arrays con name, type, content, ttl, priority)
 */
echo '$ORIGIN ' . $domain . ".\n";
echo '$TTL 3600' . "\n\n";

// Buscar el SOA
$soa = null;
foreach ($records as $r) {
    if ($r['type'] === 'SOA') {
        $soa = $r;
        break;
    }
}

// Valores por defecto o Globales
$ns1 = (defined('DNS_HOSTNAME') && defined('DNS_DOMAIN')) ? DNS_HOSTNAME . '.' . DNS_DOMAIN : 'ns1.' . $domain;
$admin_email_raw = defined('DNS_ADMIN_EMAIL') ? DNS_ADMIN_EMAIL : (defined('ADMIN_EMAIL') ? ADMIN_EMAIL : 'admin.' . $domain);
$admin_email = str_replace('@', '.', $admin_email_raw);

if ($soa) {
    // Si viene contenido completo
    echo "@\tIN\tSOA\t{$soa['content']}\n\n";
} else {
    // Generar SOA por defecto
    echo "@\tIN\tSOA\t{$ns1}. {$admin_email}. (\n";
    echo "\t\t\t{$serial}\t; Serial\n";
    echo "\t\t\t3600\t\t; Refresh\n";
    echo "\t\t\t1800\t\t; Retry\n";
    echo "\t\t\t604800\t\t; Expire\n";
    echo "\t\t\t86400\t\t; Minimum TTL\n";
    echo ")\n\n";
}

// Pintar el resto de registros
foreach ($records as $r) {
    if ($r['type'] === 'SOA') continue;
    
    $name = $r['name'] === '@' ? '' : $r['name'];
    $ttl = $r['ttl'] ? $r['ttl'] : '';
    
    if ($r['type'] === 'MX' || $r['type'] === 'SRV') {
        $priority = $r['priority'] !== null ? $r['priority'] : 10;
        echo "{$name}\t{$ttl}\tIN\t{$r['type']}\t{$priority}\t{$r['content']}\n";
    } else {
        // Encerramos el TXT entre comillas si no las lleva
        $content = $r['content'];
        if ($r['type'] === 'TXT' && strpos($content, '"') !== 0) {
            $content = '"' . $content . '"';
        }
        echo "{$name}\t{$ttl}\tIN\t{$r['type']}\t{$content}\n";
    }
}
echo "\n";
?>
