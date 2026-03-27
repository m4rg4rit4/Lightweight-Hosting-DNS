<?php
/**
 * Lightweight-Hosting Login Page
 */

if (session_status() === PHP_SESSION_NONE) {
    session_start();
}

$configFile = __DIR__ . '/config.php';
if (!file_exists($configFile)) {
    die("Error: config.php no encontrado.");
}
require_once $configFile;

$redirect = $_GET['redirect'] ?? $_POST['redirect'] ?? 'index.php';

$error = '';
if ($_SERVER['REQUEST_METHOD'] === 'POST') {
    $user = $_POST['user'] ?? '';
    $pass = $_POST['pass'] ?? '';

    // Validar constantes (asegurar que existen)
    if (!defined('ADMIN_USER') || !defined('ADMIN_PASS')) {
        $error = "Configuración de seguridad incompleta en config.php.";
    } elseif ($user === ADMIN_USER && $pass === ADMIN_PASS) {
        $_SESSION['lwh_logged_in'] = true;
        $_SESSION['lwh_user'] = $user;
        
        // Redirigir a la página principal (o a la que intentaba entrar)
        header("Location: " . $redirect);
        exit;
    } else {
        $error = "Usuario o contraseña incorrectos.";
    }
}
?>
<!DOCTYPE html>
<html lang="es" data-bs-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Login | Lightweight Hosting</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/css/bootstrap.min.css" rel="stylesheet">
    <link href="https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;600&display=swap" rel="stylesheet">
    <style>
        :root {
            --primary: #0d6efd;
            --bg-dark: #0f172a;
            --card-bg: #1e293b;
        }
        body {
            background-color: var(--bg-dark);
            color: #f8fafc;
            font-family: 'Outfit', sans-serif;
            height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            margin: 0;
            overflow: hidden;
        }
        .login-card {
            background: var(--card-bg);
            border: 1px solid rgba(255,255,255,0.1);
            border-radius: 1.5rem;
            padding: 2.5rem;
            width: 100%;
            max-width: 400px;
            box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
            animation: fadeIn 0.6s ease-out;
        }
        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(20px); }
            to { opacity: 1; transform: translateY(0); }
        }
        .brand {
            font-weight: 600;
            font-size: 1.5rem;
            text-align: center;
            margin-bottom: 2rem;
            color: var(--primary);
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 10px;
        }
        .form-control {
            background-color: #0f172a;
            border: 1px solid #334155;
            color: #fff;
            padding: 0.75rem 1rem;
            border-radius: 0.75rem;
        }
        .form-control:focus {
            background-color: #0f172a;
            box-shadow: 0 0 0 3px rgba(13, 110, 253, 0.25);
            border-color: var(--primary);
            color: #fff;
        }
        .btn-primary {
            padding: 0.75rem;
            border-radius: 0.75rem;
            font-weight: 600;
            background: linear-gradient(135deg, #0d6efd 0%, #0a58ca 100%);
            border: none;
            margin-top: 1rem;
        }
        .btn-primary:hover {
            transform: translateY(-1px);
            box-shadow: 0 4px 12px rgba(13, 110, 253, 0.4);
        }
        .error-msg {
            background: rgba(239, 68, 68, 0.1);
            border: 1px solid #ef4444;
            color: #fca5a5;
            padding: 0.75rem;
            border-radius: 0.75rem;
            margin-bottom: 1.5rem;
            font-size: 0.9rem;
            text-align: center;
        }
    </style>
</head>
<body>

<div class="login-card">
    <div class="brand">
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" fill="currentColor" class="bi bi-hdd-network" viewBox="0 0 16 16">
            <path d="M4.5 5a.5.5 0 1 0 0-1 .5.5 0 0 0 0 1M3 4.5a.5.5 0 1 1-1 0 .5.5 0 0 1 1 0m2 7a.5.5 0 1 1-1 0 .5.5 0 0 1 1 0m-2.5.5a.5.5 0 1 0 0-1 .5.5 0 0 0 0 1"/>
            <path d="M2 2a2 2 0 0 0-2 2v1a2 2 0 0 0 2 2h1v3H1a1 1 0 0 0-1 1v2a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1h-2V7h1a2 2 0 0 0 2-2V4a2 2 0 0 0-2-2zM1 4a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v1a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1zm1.5 8a.5.5 0 1 0 0-1 .5.5 0 0 0 0 1m9 0a.5.5 0 1 0 0-1 .5.5 0 0 0 0 1M10 7v3h-1V7zm-4 3V7h1v3z"/>
        </svg>
        Lightweight Hosting
    </div>

    <?php if ($error): ?>
        <div class="error-msg">
            <?= htmlspecialchars($error) ?>
        </div>
    <?php endif; ?>

    <?php if (isset($_GET['logged_out'])): ?>
        <div class="alert alert-info py-2 small text-center" style="border-radius: 0.75rem;">
            Sesión cerrada correctamente.
        </div>
    <?php endif; ?>

    <form method="POST">
        <input type="hidden" name="redirect" value="<?= htmlspecialchars($redirect) ?>">
        <div class="mb-3">
            <label class="form-label small text-secondary">Usuario</label>
            <input type="text" id="user" name="user" class="form-control" placeholder="admin" autocomplete="username" required autofocus>
        </div>
        <div class="mb-4">
            <label class="form-label small text-secondary">Contraseña</label>
            <input type="password" id="pass" name="pass" class="form-control" placeholder="••••••••" autocomplete="current-password" required>
        </div>
        <button type="submit" class="btn btn-primary w-100">Iniciar Sesión</button>
    </form>
    
    <div class="mt-4 text-center">
        <p class="text-secondary small">Panel de Administración</p>
    </div>
</div>

</body>
</html>
