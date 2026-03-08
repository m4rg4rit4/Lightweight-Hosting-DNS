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

$headers = getallheaders();
$authHeader = isset($headers['Authorization']) ? $headers['Authorization'] : '';
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
        } elseif ($routeString === 'record/del') {
            handlePostRecordDel($pdo, $input);
        } else {
            response(404, false, "Ruta POST no encontrada: $routeString");
        }
    } elseif ($method === 'GET') {
        if (strpos($routeString, 'status/') === 0) {
            $id = intval(substr($routeString, 7));
            handleGetStatus($pdo, $id);
        } elseif (strpos($routeString, 'records/') === 0) {
            $domain = substr($routeString, 8);
            handleGetRecords($pdo, $domain);
        } elseif (strpos($routeString, 'query/') === 0) {
            $fqdn = substr($routeString, 6);
            handleGetQuery($pdo, $fqdn);
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
        response(400, false, "El dominio ya existe en las zonas DNS.");
    }

    // Insertar petición pendiente
    $stmt = $pdo->prepare("INSERT INTO sys_dns_requests (action, domain, target_ip, status) VALUES ('add', ?, ?, 'pending')");
    $stmt->execute([$domain, $ip]);
    $reqId = $pdo->lastInsertId();

    response(200, true, "La solicitud de alta ha sido recibida.", ['request_id' => $reqId, 'status' => 'pending']);
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
    // Argumentos: domain, name, type, content, ttl (optional), priority (optional, for MX/SRV)
    $domain = trim($input['domain'] ?? '');
    $name = trim($input['name'] ?? ''); // Ej: '@', 'www', 'mail1._domainkey'
    $type = strtoupper(trim($input['type'] ?? '')); // A, TXT, MX...
    $content = trim($input['content'] ?? '');
    $ttl = intval($input['ttl'] ?? 3600);
    $priority = isset($input['priority']) ? intval($input['priority']) : null;

    if (empty($domain) || empty($name) || empty($type) || empty($content)) {
        response(400, false, "Faltan parámetros: domain, name, type, content son obligatorios.");
    }

    // Obtener zone_id
    $stmt = $pdo->prepare("SELECT id FROM sys_dns_zones WHERE domain = ?");
    $stmt->execute([$domain]);
    $zone = $stmt->fetch();
    if (!$zone) {
        response(400, false, "El dominio principal '$domain' no existe.");
    }
    $zoneId = $zone['id'];

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
        response(200, true, "Registro $type añadido a $domain.");
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
    
    response(200, true, "Consulta resuelta.", ['fqdn' => $fqdn, 'results' => $records]);
}

// Helpers

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
