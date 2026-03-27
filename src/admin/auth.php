<?php
/**
 * Lightweight-Hosting Authentication Helper
 * Shared between DNS and Hosting panels.
 */

if (session_status() === PHP_SESSION_NONE) {
    session_start();
}

// Determinar la ruta al config.php (puede variar según si es DNS standalone o Hosting)
$possibleConfigs = [
    __DIR__ . '/config.php',               // Nivel actual
    '/var/www/admin_panel/config.php'      // Ruta estándar en Linux (Hosting)
];

$configLoaded = false;
foreach ($possibleConfigs as $cfg) {
    if (file_exists($cfg)) {
        require_once $cfg;
        $configLoaded = true;
        break;
    }
}

if (!$configLoaded) {
    die("Error: config.php no encontrado. Ejecuta el instalador primero.");
}

// Función para verificar si el usuario está logueado
function checkAuth() {
    if (!isset($_SESSION['lwh_logged_in']) || $_SESSION['lwh_logged_in'] !== true) {
        // Redirigir al login.php en el mismo directorio con parámetro de retorno
        $protocol = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') ? 'https' : 'http';
        $host = $_SERVER['HTTP_HOST'];
        $dir = dirname($_SERVER['PHP_SELF']);
        $currentFile = basename($_SERVER['PHP_SELF']);
        
        // Evitar bucles con login.php
        if ($currentFile === 'login.php') return;

        header("Location: $protocol://$host" . $dir . "/login.php?redirect=" . urlencode($currentFile));
        exit;
    }
}

// Manejo de Logout
if (isset($_GET['logout'])) {
    $_SESSION = [];
    if (ini_get("session.use_cookies")) {
        $params = session_get_cookie_params();
        setcookie(session_name(), '', time() - 42000,
            $params["path"], $params["domain"],
            $params["secure"], $params["httponly"]
        );
    }
    session_destroy();
    
    $protocol = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') ? 'https' : 'http';
    $host = $_SERVER['HTTP_HOST'];
    $dir = dirname($_SERVER['PHP_SELF']);
    header("Location: $protocol://$host" . $dir . "/login.php?logged_out=1");
    exit;
}
?>
