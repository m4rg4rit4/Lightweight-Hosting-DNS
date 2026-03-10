<?php
/**
 * Lightweight-Hosting-DNS API
 * Enrutador principal que gestiona todas las peticiones a /api-dns/
 */

// Cabeceras CORS y tipo de contenido JSON
header('Content-Type: application/json; charset=utf-8');
header('Access-Control-Allow-Origin: *');
header('Access-Control-Allow-Methods: GET, POST, OPTIONS');
header('Access-Control-Allow-Headers: Authorization, Content-Type');

if ($_SERVER['REQUEST_METHOD'] === 'OPTIONS') {
    http_response_code(200);
    exit();
}

// Incluir configuración global y conexión PDO
$configFile = '/var/www/admin_panel/config.php';
if (file_exists($configFile)) {
    require_once $configFile;
} else {
    // Modo desarrollo / fallback local
    $localConfig = __DIR__ . '/../../config.php';
    if (file_exists($localConfig)) {
        require_once $localConfig;
    } else {
        response(500, false, "El archivo de configuración config.php no se encuentra.");
    }
}

$pdo = getPDO();

// Buscar cabecera de autorización de forma robusta
$authHeader = '';
if (isset($_SERVER['HTTP_AUTHORIZATION'])) {
    $authHeader = $_SERVER['HTTP_AUTHORIZATION'];
} elseif (isset($_SERVER['REDIRECT_HTTP_AUTHORIZATION'])) {
    $authHeader = $_SERVER['REDIRECT_HTTP_AUTHORIZATION'];
} elseif (isset($_SERVER['Redirect_HTTP_AUTHORIZATION'])) {
    $authHeader = $_SERVER['Redirect_HTTP_AUTHORIZATION'];
} elseif (function_exists('getallheaders')) {
    $headers = getallheaders();
    foreach ($headers as $key => $value) {
        if (strcasecmp($key, 'Authorization') === 0) {
            $authHeader = $value;
            break;
        }
    }
}

$providedToken = '';

if (strpos($authHeader, 'Bearer ') === 0) {
    $providedToken = substr($authHeader, 7);
}

// Permitir 'dev' temporalmente solo si la constante DNS_API_KEY='dev' existe.
$isMasterEnvToken = defined('DNS_API_KEY') && DNS_API_KEY === 'dev' && $providedToken === 'dev';

if (!$isMasterEnvToken) {
    if (empty($providedToken)) {
        response(401, false, "No autorizado. Proporciona un Bearer Token válido.");
    }

    $stmt = $pdo->prepare("SELECT id, client_name FROM sys_dns_tokens WHERE token = ? AND is_active = 1");
    $stmt->execute([$providedToken]);
    $tokenCheck = $stmt->fetch();

    if (!$tokenCheck) {
        response(401, false, "No autorizado. Token inválido o inactivo.");
    }
}

// Parsear la URL solicitada
// Se espera que el VirtualHost o .htaccess redirija todo a index.php?route=...
// Ejemplo: /api-dns/add -> $_GET['route'] = 'add'
$requestUri = $_SERVER['REQUEST_URI'];
$basePath = '/api-dns/';
$routeString = '';

if (strpos($requestUri, $basePath) !== false) {
    $routeParts = explode($basePath, parse_url($requestUri, PHP_URL_PATH));
    $routeString = isset($routeParts[1]) ? trim($routeParts[1], '/') : '';
} elseif (isset($_GET['route'])) {
    $routeString = trim($_GET['route'], '/');
}

$method = $_SERVER['REQUEST_METHOD'];

try {
    // Leer el body JSON si es POST
    $input = json_decode(file_get_contents('php://input'), true);
    if (!is_array($input)) $input = $_POST;

    // Enrutador Simple
    if ($method === 'POST') {
        if ($routeString === 'add') {
            handlePostAdd($pdo, $input);
        } elseif ($routeString === 'delete') {
            handlePostDelete($pdo, $input);
        } elseif ($routeString === 'record/add') {
            handlePostRecordAdd($pdo, $input);
        } elseif ($routeString === 'record/edit') {
            handlePostRecordEdit($pdo, $input);
        } elseif ($routeString === 'record/del') {
            handlePostRecordDel($pdo, $input);
        } else {
            response(404, false, "Ruta POST no encontrada: $routeString");
        }
    } elseif ($method === 'GET') {
        if (strpos($routeString, 'status/') === 0) {
            $id = intval(substr($routeString, 7));
            handleGetStatus($pdo, $id);
        } elseif ($routeString === 'zones') {
            handleGetZones($pdo);
        } elseif (preg_match('/^zone\/(.*)\/export$/', $routeString, $matches)) {
            $domain = $matches[1];
            handleGetZoneExport($pdo, $domain);
        } elseif (strpos($routeString, 'records/') === 0) {
            $domain = substr($routeString, 8);
            handleGetRecords($pdo, $domain);
        } elseif (strpos($routeString, 'query/') === 0) {
            $fqdn = substr($routeString, 6);
            handleGetQuery($pdo, $fqdn);
        } elseif (strpos($routeString, 'verify/') === 0) {
            $domain = substr($routeString, 7);
            handleGetVerify($pdo, $domain);
        } elseif ($routeString === 'status/pending') {
            handleGetPendingStatus($pdo);
        } elseif ($routeString === '' || $routeString === '/') {
            response(200, true, "Lightweight DNS API is active.", [
                'endpoints' => [
                    'GET /api-dns/zones' => 'List all managed domains',
                    'GET /api-dns/records/{domain}' => 'List records for a domain',
                    'GET /api-dns/query/{fqdn}' => 'Queries which zone handles a given FQDN',
                    'GET /api-dns/zone/{domain}/export' => 'Export zone in BIND format',
                    'POST /api-dns/add' => 'Add a new authoritative zone',
                    'POST /api-dns/record/add' => 'Add a DNS record',
                    'POST /api-dns/record/edit' => 'Edit a DNS record',
                    'POST /api-dns/record/del' => 'Delete a DNS record'
                ]
            ]);
        } else {
            response(404, false, "Ruta GET no encontrada: $routeString");
        }
    } else {
        response(405, false, "Método no permitido.");
    }
} catch (Exception $e) {
    response(500, false, "Error interno del servidor: " . $e->getMessage());
}

// --- Controladores (Handlers) ---

function handlePostAdd($pdo, $input) {
    $domain = trim($input['domain'] ?? '');
    $ip = trim($input['ip'] ?? '');

    if (empty($domain) || empty($ip) || !filter_var($ip, FILTER_VALIDATE_IP)) {
        response(400, false, "Parámetros inválidos. Se requiere 'domain' e 'ip' válida.");
    }

    // Comprobar si ya existe
    $stmt = $pdo->prepare("SELECT id FROM sys_dns_zones WHERE domain = ?");
    $stmt->execute([$domain]);
    if ($stmt->fetch()) {
        response(400, false, "El dominio ya existe en el sistema.");
    }

    // Insertar zona
    $stmt = $pdo->prepare("INSERT INTO sys_dns_zones (domain, zone_file_path, is_active) VALUES (?, ?, 1)");
    $zoneFile = "/etc/bind/zones/db." . $domain;
    $stmt->execute([$domain, $zoneFile]);
    $zoneId = $pdo->lastInsertId();

    // Insertar registros base (NS y A del dominio)
    $ns1 = (defined('DNS_HOSTNAME') && defined('DNS_DOMAIN')) ? DNS_HOSTNAME . '.' . DNS_DOMAIN : 'ns1.' . $domain;
    $stmt = $pdo->prepare("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'NS', ?)");
    $stmt->execute([$zoneId, $ns1 . "."]);

    $stmt = $pdo->prepare("INSERT INTO sys_dns_records (zone_id, name, type, content) VALUES (?, '@', 'A', ?)");
    $stmt->execute([$zoneId, $ip]);

    // Encolar generación de zona
    queueZoneUpdate($pdo, $domain);

    // Verificación de delegación para informar
    $verification = checkDomainDelegation($domain);

    response(200, true, "Dominio añadido correctamente. Configure sus DNS para apuntar a $ns1", [
        'domain' => $domain,
        'dns_delegated' => $verification['delegated'],
        'verification' => $verification
    ]);
}

function handlePostDelete($pdo, $input) {
    $domain = trim($input['domain'] ?? '');

    if (empty($domain)) {
        response(400, false, "Se requiere el parámetro 'domain'.");
    }

    // Opcional: Validar si existe en sys_dns_zones antes de encolar
    $stmt = $pdo->prepare("SELECT id FROM sys_dns_zones WHERE domain = ?");
    $stmt->execute([$domain]);
    if (!$stmt->fetch()) {
        response(404, false, "El dominio no se encuentra en el sistema.");
    }

    $stmt = $pdo->prepare("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('delete', ?, 'pending')");
    $stmt->execute([$domain]);
    $reqId = $pdo->lastInsertId();

    response(200, true, "La solicitud de borrado ha sido recibida.", ['request_id' => $reqId, 'status' => 'pending']);
}

function handlePostRecordAdd($pdo, $input) {
    $domain = trim($input['domain'] ?? '');
    $name = trim($input['name'] ?? ''); 
    $type = strtoupper(trim($input['type'] ?? '')); 
    $content = trim($input['content'] ?? '');
    $ttl = intval($input['ttl'] ?? 3600);
    $priority = isset($input['priority']) ? intval($input['priority']) : null;

    if (empty($domain) || empty($name) || empty($type) || empty($content)) {
        response(400, false, "Faltan parámetros: domain, name, type, content son obligatorios.");
    }

    // Obtener zone_id y verificar existencia
    $stmt = $pdo->prepare("SELECT id FROM sys_dns_zones WHERE domain = ?");
    $stmt->execute([$domain]);
    $zone = $stmt->fetch();
    if (!$zone) {
        response(400, false, "Error: El dominio principal '$domain' debe ser añadido primero mediante /api-dns/add");
    }
    $zoneId = $zone['id'];

    // Verificación de delegación (Aviso suave)
    $verification = checkDomainDelegation($domain);
    $warning = "";
    if (!$verification['delegated']) {
        $warning = " (Aviso: El dominio principal aún no parece estar correctamente delegado)";
    }

    // Validación CNAME: Regla de oro
    if ($type === 'CNAME') {
        $stmt = $pdo->prepare("SELECT id FROM sys_dns_records WHERE zone_id = ? AND name = ?");
        $stmt->execute([$zoneId, $name]);
        if ($stmt->fetch()) {
            response(400, false, "Un registro CNAME no puede coexistir con otros registros para el mismo nombre ($name).");
        }
    } else {
        $stmt = $pdo->prepare("SELECT id FROM sys_dns_records WHERE zone_id = ? AND name = ? AND type = 'CNAME'");
        $stmt->execute([$zoneId, $name]);
        if ($stmt->fetch()) {
            response(400, false, "Ya existe un CNAME para '$name', no se pueden añadir registros de tipo $type.");
        }
    }

    // Lógica especial SPF
    if ($type === 'TXT' && $name === '@' && strpos($content, 'v=spf1') === 0) {
        processSpfUpdate($pdo, $zoneId, $domain, $content, $ttl);
    } else {
        // Inserción normal
        $stmt = $pdo->prepare("INSERT INTO sys_dns_records (zone_id, name, type, content, ttl, priority) VALUES (?, ?, ?, ?, ?, ?)");
        $stmt->execute([$zoneId, $name, $type, $content, $ttl, $priority]);
        
        queueZoneUpdate($pdo, $domain);
        response(200, true, "Registro $type añadido a $domain$warning.");
    }
}

function processSpfUpdate($pdo, $zoneId, $domain, $newContent, $ttl) {
    // Extraer mecánicos (includes, ip4) de $newContent si solo se pasa la porción, o asumir merge.
    // Lógica simplificada: si envían un v=spf1 completo, parseamos.
    $stmt = $pdo->prepare("SELECT id, content FROM sys_dns_records WHERE zone_id = ? AND type = 'TXT' AND name = '@' AND content LIKE 'v=spf1%'");
    $stmt->execute([$zoneId]);
    $existing = $stmt->fetch();

    if ($existing) {
        $actualContent = $existing['content'];
        // Si ya hay SPF, intentamos inyectar de forma inteligente.
        // Extraemos los bloques del nuevo
        preg_match_all('/(include:[^\s]+|ip4:[^\s]+|ip6:[^\s]+|a|mx)/', $newContent, $matches);
        $newMechanisms = $matches[0];
        
        $mergedContent = $actualContent;
        // Quitar la política final (~all o -all) temporalmente
        $policyMatch = '';
        if (preg_match('/(\~all|\-all|\?all)$/', $mergedContent, $pm)) {
            $policyMatch = $pm[1];
            $mergedContent = preg_replace('/\s*(\~all|\-all|\?all)$/', '', $mergedContent);
        } else {
            $policyMatch = '?all'; // Default fallback
        }

        foreach ($newMechanisms as $mech) {
            if (strpos($mergedContent, $mech) === false) {
                $mergedContent .= ' ' . $mech;
            }
        }
        $mergedContent .= ' ' . $policyMatch;

        $upStmt = $pdo->prepare("UPDATE sys_dns_records SET content = ? WHERE id = ?");
        $upStmt->execute([$mergedContent, $existing['id']]);
    } else {
        $inStmt = $pdo->prepare("INSERT INTO sys_dns_records (zone_id, name, type, content, ttl) VALUES (?, '@', 'TXT', ?, ?)");
        $inStmt->execute([$zoneId, $newContent, $ttl]);
    }

    queueZoneUpdate($pdo, $domain);
    response(200, true, "Registro SPF actualizado.");
}

function handlePostRecordEdit($pdo, $input) {
    $id = intval($input['id'] ?? 0);
    $name = trim($input['name'] ?? ''); 
    $type = strtoupper(trim($input['type'] ?? '')); 
    $content = trim($input['content'] ?? '');
    $ttl = intval($input['ttl'] ?? 3600);
    $priority = isset($input['priority']) ? intval($input['priority']) : null;

    if (!$id || empty($name) || empty($type) || empty($content)) {
        response(400, false, "Faltan parámetros: id, name, type, content son obligatorios.");
    }

    // Obtener el dominio para encolar actualización
    $stmt = $pdo->prepare("SELECT z.domain FROM sys_dns_records r JOIN sys_dns_zones z ON r.zone_id = z.id WHERE r.id = ?");
    $stmt->execute([$id]);
    $res = $stmt->fetch();
    if (!$res) response(404, false, "Registro no encontrado.");
    $domain = $res['domain'];

    // Actualizar
    $upStmt = $pdo->prepare("UPDATE sys_dns_records SET name = ?, type = ?, content = ?, ttl = ?, priority = ? WHERE id = ?");
    $upStmt->execute([$name, $type, $content, $ttl, $priority, $id]);

    queueZoneUpdate($pdo, $domain);
    response(200, true, "Registro actualizado correctamente.");
}

function handlePostRecordDel($pdo, $input) {
    $id = isset($input['id']) ? intval($input['id']) : 0;
    
    // Si no pasan ID, podrían pasar domain, name y type
    if ($id === 0) {
        $domain = trim($input['domain'] ?? '');
        $name = trim($input['name'] ?? '');
        $type = strtoupper(trim($input['type'] ?? ''));
        
        if (empty($domain) || empty($name) || empty($type)) {
            response(400, false, "Debe proporcionar 'id' del registro o 'domain', 'name' y 'type'.");
        }
        
        $stmt = $pdo->prepare("SELECT r.id, z.domain FROM sys_dns_records r JOIN sys_dns_zones z ON r.zone_id = z.id WHERE z.domain = ? AND r.name = ? AND r.type = ?");
        $stmt->execute([$domain, $name, $type]);
        $record = $stmt->fetch();
        if (!$record) {
            response(404, false, "Registro no encontrado.");
        }
        $id = $record['id'];
        $domainToUpdate = $record['domain'];
    } else {
        $stmt = $pdo->prepare("SELECT z.domain FROM sys_dns_records r JOIN sys_dns_zones z ON r.zone_id = z.id WHERE r.id = ?");
        $stmt->execute([$id]);
        $res = $stmt->fetch();
        if (!$res) response(404, false, "Registro ID no encontrado.");
        $domainToUpdate = $res['domain'];
    }

    $stmt = $pdo->prepare("DELETE FROM sys_dns_records WHERE id = ?");
    $stmt->execute([$id]);
    
    queueZoneUpdate($pdo, $domainToUpdate);
    response(200, true, "Registro eliminado correctamente.");
}

function handleGetPendingStatus($pdo) {
    $stmt = $pdo->query("SELECT COUNT(*) FROM sys_dns_requests WHERE status IN ('pending', 'processing')");
    $count = $stmt->fetchColumn();
    response(200, true, "Tareas pendientes recuperadas.", ['pending_count' => (int)$count]);
}

function handleGetStatus($pdo, $id) {
    $stmt = $pdo->prepare("SELECT status, error_log, domain, request_date, processed_date FROM sys_dns_requests WHERE id = ?");
    $stmt->execute([$id]);
    $res = $stmt->fetch();
    
    if (!$res) response(404, false, "Solicitud no encontrada.");
    response(200, true, "Estado recuperado.", $res);
}

function handleGetRecords($pdo, $domain) {
    $stmt = $pdo->prepare("
        SELECT r.id, r.name, r.type, r.content, r.ttl, r.priority 
        FROM sys_dns_records r 
        JOIN sys_dns_zones z ON r.zone_id = z.id 
        WHERE z.domain = ?
    ");
    $stmt->execute([$domain]);
    $records = $stmt->fetchAll();
    
    response(200, true, "Registros recuperados.", ['domain' => $domain, 'records' => $records]);
}

function handleGetQuery($pdo, $fqdn) {
    // Ejemplo: host9.ingeniacom.com -> necesitamos ver a qué zona pertenece.
    // Buscamos zonas que sean sufijo del FQDN
    $stmt = $pdo->query("SELECT id, domain FROM sys_dns_zones");
    $zones = $stmt->fetchAll();
    
    $foundZone = null;
    $foundName = null;
    
    foreach ($zones as $zone) {
        $zDomain = $zone['domain'];
        if ($fqdn === $zDomain) {
            $foundZone = $zone['id'];
            $foundName = '@';
            break;
        } elseif (preg_match('/^(.*)\.' . preg_quote($zDomain, '/') . '$/', $fqdn, $matches)) {
            $foundZone = $zone['id'];
            $foundName = $matches[1];
            break;
        }
    }
    
    if (!$foundZone) {
        response(404, false, "$fqdn no pertenece a ninguna zona gestionada.");
    }
    
    $stmt = $pdo->prepare("SELECT type, content FROM sys_dns_records WHERE zone_id = ? AND name = ?");
    $stmt->execute([$foundZone, $foundName]);
    $records = $stmt->fetchAll();
    
    if (!$records) response(404, false, "No hay registros para $fqdn.");
    
    response(200, true, "Consulta resuelta.", [
        'fqdn' => $fqdn, 
        'zone' => $zDomain, 
        'name' => $foundName, 
        'results' => $records
    ]);
}

function handleGetZones($pdo) {
    $stmt = $pdo->query("SELECT id, domain, created_at, updated_at FROM sys_dns_zones ORDER BY domain ASC");
    $zones = $stmt->fetchAll();
    
    foreach ($zones as &$zone) {
        $verification = checkDomainDelegation($zone['domain']);
        $zone['delegated'] = $verification['delegated'];
        $zone['details'] = $verification;
    }

    response(200, true, "Lista de zonas recuperada.", ['zones' => $zones]);
}

function handleGetZoneExport($pdo, $domain) {
    // Obtener la zona
    $stmt = $pdo->prepare("SELECT id FROM sys_dns_zones WHERE domain = ?");
    $stmt->execute([$domain]);
    $zone = $stmt->fetch();
    
    if (!$zone) {
        response(404, false, "La zona $domain no existe.");
    }

    // Obtener registros
    $stmt = $pdo->prepare("SELECT name, type, content, ttl, priority FROM sys_dns_records WHERE zone_id = ? ORDER BY type='SOA' DESC, type='NS' DESC, name ASC");
    $stmt->execute([$zone['id']]);
    $records = $stmt->fetchAll();

    // Generar formato BIND usando la plantilla si está disponible localmente, sino manualmente
    $templateFile = __DIR__ . '/../engine/template.zone.php';
    $serial = date('Ymd') . '01'; 
    
    header('Content-Type: text/plain; charset=utf-8');
    if (file_exists($templateFile)) {
        include $templateFile;
    } else {
        // Fallback básico si no hay template
        echo "; Full Zone Export for $domain\n";
        echo "\$ORIGIN $domain.\n";
        echo "\$TTL 3600\n\n";
        foreach ($records as $r) {
            $priority = ($r['type'] === 'MX' || $r['type'] === 'SRV') ? ($r['priority'] ?? 10) . "\t" : "";
            echo "{$r['name']}\t{$r['ttl']}\tIN\t{$r['type']}\t{$priority}{$r['content']}\n";
        }
    }
    exit();
}

function handleGetVerify($pdo, $domain) {
    if (empty($domain)) {
        response(400, false, "Se requiere el dominio.");
    }

    $verification = checkDomainDelegation($domain);
    
    if ($verification['delegated']) {
        $msg = "El dominio $domain está correctamente delegado a tus nameservers.";
    } else {
        $msg = "El dominio $domain NO está apuntando a tus nameservers o la propagación está en curso.";
        if (isset($verification['found_ns'])) {
            $msg .= " Nameservers encontrados: " . implode(', ', $verification['found_ns']);
        }
    }

    response(200, true, $msg, [
        'domain' => $domain,
        'delegated' => $verification['delegated'],
        'details' => $verification
    ]);
}

// Helpers

function checkDomainDelegation($domain) {
    $expectedNS = '';
    if (defined('DNS_HOSTNAME') && defined('DNS_DOMAIN')) {
        $expectedNS = strtolower(DNS_HOSTNAME . '.' . DNS_DOMAIN);
    }

    if (empty($expectedNS)) {
        return ['delegated' => false, 'error' => 'DNS_HOSTNAME o DNS_DOMAIN no definidos en config.php'];
    }

    // Intentar obtener registros NS
    $nsRecords = dns_get_record($domain, DNS_NS);
    $foundNS = [];
    $isDelegated = false;

    if ($nsRecords) {
        foreach ($nsRecords as $record) {
            $target = strtolower($record['target']);
            $foundNS[] = $target;
            if (strpos($target, $expectedNS) !== false) {
                $isDelegated = true;
            }
        }
    }

    // Si no hay NS, probar con SOA (a veces indicativo)
    if (!$isDelegated) {
        $soaRecords = dns_get_record($domain, DNS_SOA);
        if ($soaRecords) {
            foreach ($soaRecords as $record) {
                $mname = strtolower($record['mname']);
                if (strpos($mname, $expectedNS) !== false) {
                    $isDelegated = true;
                    $foundNS[] = $mname . " (SOA)";
                }
            }
        }
    }

    return [
        'delegated' => $isDelegated,
        'expected_ns' => $expectedNS,
        'found_ns' => array_unique($foundNS)
    ];
}

function queueZoneUpdate($pdo, $domain) {
    // Insertar un action 'add' que el cron tratará como 'update' si ya existe el archivo de zona, regenerándolo.
    $stmt = $pdo->prepare("INSERT INTO sys_dns_requests (action, domain, status) VALUES ('add', ?, 'pending')");
    $stmt->execute([$domain]);
}

function response($code, $success, $message, $data = null) {
    http_response_code($code);
    $output = [
        'success' => $success,
        'message' => $message
    ];
    if ($data !== null) {
        $output = array_merge($output, $data);
    }
    echo json_encode($output, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE);
    exit();
}
?>
