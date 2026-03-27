<?php
// admin/dns_tokens.php
// Panel de Gestión de Tokens para Lightweight-Hosting-DNS

$configFile = __DIR__ . '/config.php';
if (!file_exists($configFile)) {
    die("<div style='color:red; font-family:sans-serif;'>Error: config.php no encontrado en " . __DIR__ . "</div>");
}
require_once $configFile;

require_once __DIR__ . '/auth.php';
checkAuth();

$pdo = getPDO();
$message = '';
$messageType = 'success';

// Manejo de acciones (Añadir, Eliminar, Alternar Estado)
if ($_SERVER['REQUEST_METHOD'] === 'POST') {
    $action = $_POST['action'] ?? '';
    
    if ($action === 'add') {
        $clientName = trim($_POST['client_name'] ?? '');
        if (!empty($clientName)) {
            try {
                $newToken = bin2hex(random_bytes(16)); // Generar token seguro de 32 caracteres
                $stmt = $pdo->prepare("INSERT INTO sys_dns_tokens (token, client_name, is_active) VALUES (?, ?, 1)");
                $stmt->execute([$newToken, $clientName]);
                $message = "Token para '$clientName' generado con éxito.";
            } catch (Exception $e) {
                $message = "Error al crear el token: " . $e->getMessage();
                $messageType = 'danger';
            }
        } else {
            $message = "El nombre del cliente no puede estar vacío.";
            $messageType = 'danger';
        }
    } elseif ($action === 'edit') {
        $tokenId = intval($_POST['token_id'] ?? 0);
        $clientName = trim($_POST['client_name'] ?? '');
        $tokenValue = trim($_POST['token_value'] ?? '');
        
        if ($tokenId > 0 && !empty($clientName) && !empty($tokenValue)) {
            try {
                $stmt = $pdo->prepare("UPDATE sys_dns_tokens SET client_name = ?, token = ? WHERE id = ?");
                $stmt->execute([$clientName, $tokenValue, $tokenId]);
                $message = "Token actualizado con éxito.";
            } catch (Exception $e) {
                $message = "Error al actualizar el token: " . $e->getMessage();
                $messageType = 'danger';
            }
        } else {
            $message = "Todos los campos son obligatorios para la edición.";
            $messageType = 'danger';
        }
    } elseif ($action === 'delete') {
        $tokenId = intval($_POST['token_id'] ?? 0);
        if ($tokenId > 0) {
            $pdo->prepare("DELETE FROM sys_dns_tokens WHERE id = ?")->execute([$tokenId]);
            $message = "Token eliminado definitivamente.";
        }
    } elseif ($action === 'toggle') {
        $tokenId = intval($_POST['token_id'] ?? 0);
        $currentStatus = intval($_POST['current_status'] ?? 0);
        $newStatus = $currentStatus === 1 ? 0 : 1;
        if ($tokenId > 0) {
            $pdo->prepare("UPDATE sys_dns_tokens SET is_active = ? WHERE id = ?")->execute([$newStatus, $tokenId]);
            $message = $newStatus === 1 ? "Token activado." : "Token revocado.";
        }
    }
}

// Obtener todos los tokens
$stmt = $pdo->query("SELECT * FROM sys_dns_tokens ORDER BY created_at DESC");
$tokens = $stmt->fetchAll();
?>
<!DOCTYPE html>
<html lang="es" data-bs-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Gestión de Tokens DNS - Lightweight Hosting</title>
    <!-- Mismo estilo base que Lightweight Hosting (Bootstrap 5) -->
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/css/bootstrap.min.css" rel="stylesheet">
    <link href="https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.1/font/bootstrap-icons.css" rel="stylesheet">
    <style>
        body { background-color: #121212; color: #e0e0e0; font-family: 'Segoe UI', system-ui, sans-serif; }
        .card { background-color: #1e1e1e; border: 1px solid #333; border-radius: 12px; box-shadow: 0 4px 6px rgba(0,0,0,0.3); }
        .card-header { background-color: #252525; border-bottom: 1px solid #333; font-weight: 600; }
        .table { color: #e0e0e0; }
        .table-hover tbody tr:hover { background-color: #2a2a2a; color: #fff; }
        .token-text { font-family: monospace; background: #000; padding: 4px 8px; border-radius: 4px; border: 1px solid #333; color: #0d6efd; user-select: all; }
        .btn-glow { transition: all 0.2s; }
        .btn-glow:hover { transform: translateY(-1px); box-shadow: 0 4px 12px rgba(13, 110, 253, 0.4); }
    </style>
</head>
<body>

<nav class="navbar navbar-expand-lg navbar-dark bg-dark border-bottom border-secondary mb-4">
  <div class="container-fluid">
    <a class="navbar-brand" href="#"><i class="bi bi-hdd-network"></i> Lightweight Hosting</a>
    <span class="navbar-text ms-auto text-primary fw-bold">DNS API Tokens</span>
  </div>
</nav>

<div class="container py-3">
    
    <div class="d-flex justify-content-between align-items-center mb-4">
        <h2 class="h3 mb-0"><i class="bi bi-key"></i> Gestión de Claves API DNS</h2>
    </div>

    <?php if ($message): ?>
        <div class="alert alert-<?= $messageType ?> alert-dismissible fade show shadow-sm" role="alert">
            <i class="bi bi-info-circle-fill me-2"></i><?= htmlspecialchars($message) ?>
            <button type="button" class="btn-close" data-bs-dismiss="alert" aria-label="Close"></button>
        </div>
    <?php endif; ?>

    <div class="row">
        <!-- Formulario para Nuevo Token -->
        <div class="col-lg-4 mb-4">
            <div class="card h-100">
                <div class="card-header"><i class="bi bi-plus-circle"></i> Generar Nuevo Token</div>
                <div class="card-body">
                    <p class="text-muted small">Crea credenciales para que paneles externos o servidores remotos puedan modificar las zonas DNS vía API.</p>
                    <form method="POST">
                        <input type="hidden" name="action" value="add">
                        <div class="mb-3">
                            <label class="form-label text-light">Identificador del Cliente/Servidor</label>
                            <input type="text" name="client_name" class="form-control bg-dark text-light border-secondary" placeholder="Ej. WebServer 1, cPanel..." required>
                        </div>
                        <button type="submit" class="btn btn-primary w-100 btn-glow"><i class="bi bi-magic"></i> Generar API Key</button>
                    </form>
                </div>
            </div>
        </div>

        <!-- Tabla de Tokens -->
        <div class="col-lg-8 mb-4">
            <div class="card h-100">
                <div class="card-header"><i class="bi bi-list-check"></i> Tokens Activos e Histórico</div>
                <div class="card-body p-0">
                    <div class="table-responsive">
                        <table class="table table-hover table-borderless align-middle mb-0">
                            <thead class="table-dark">
                                <tr>
                                    <th class="ps-3">Cliente</th>
                                    <th>Token (Bearer)</th>
                                    <th>Creado</th>
                                    <th>Estado</th>
                                    <th class="text-end pe-3">Acciones</th>
                                </tr>
                            </thead>
                            <tbody>
                                <?php if (empty($tokens)): ?>
                                    <tr>
                                        <td colspan="5" class="text-center py-4 text-muted">No hay tokens registrados en la base de datos.</td>
                                    </tr>
                                <?php else: ?>
                                    <?php foreach ($tokens as $t): ?>
                                    <tr class="<?= $t['is_active'] ? '' : 'opacity-50' ?>">
                                        <td class="ps-3 fw-medium"><?= htmlspecialchars($t['client_name']) ?></td>
                                        <td><span class="token-text"><?= htmlspecialchars($t['token']) ?></span></td>
                                        <td class="text-muted small"><?= date('Y-m-d H:i', strtotime($t['created_at'])) ?></td>
                                        <td>
                                            <?php if ($t['is_active']): ?>
                                                <span class="badge bg-success bg-opacity-25 text-success border border-success"><i class="bi bi-check-circle"></i> Activo</span>
                                            <?php else: ?>
                                                <span class="badge bg-danger bg-opacity-25 text-danger border border-danger"><i class="bi bi-x-circle"></i> Revocado</span>
                                            <?php endif; ?>
                                        </td>
                                        <td class="text-end pe-3">
                                            <!-- Alternar Estado -->
                                            <form method="POST" class="d-inline">
                                                <input type="hidden" name="action" value="toggle">
                                                <input type="hidden" name="token_id" value="<?= $t['id'] ?>">
                                                <input type="hidden" name="current_status" value="<?= $t['is_active'] ?>">
                                                <?php if ($t['is_active']): ?>
                                                    <button type="submit" class="btn btn-sm btn-outline-warning" title="Revocar Acceso"><i class="bi bi-pause-fill"></i></button>
                                                <?php else: ?>
                                                    <button type="submit" class="btn btn-sm btn-outline-success" title="Reactivar Acceso"><i class="bi bi-play-fill"></i></button>
                                                <?php endif; ?>
                                            </form>
                                            
                                            <!-- Editar -->
                                            <button type="button" class="btn btn-sm btn-outline-info" title="Editar" 
                                                    onclick="editToken(<?= $t['id'] ?>, '<?= htmlspecialchars(addslashes($t['client_name'])) ?>', '<?= htmlspecialchars(addslashes($t['token'])) ?>')">
                                                <i class="bi bi-pencil"></i>
                                            </button>
                                            
                                            <!-- Borrar -->
                                            <form method="POST" class="d-inline" onsubmit="return confirm('¿Estás seguro de que quieres eliminar este token definitivamente? Las aplicaciones que lo usen fallarán.');">
                                                <input type="hidden" name="action" value="delete">
                                                <input type="hidden" name="token_id" value="<?= $t['id'] ?>">
                                                <button type="submit" class="btn btn-sm btn-outline-danger" title="Eliminar"><i class="bi bi-trash"></i></button>
                                            </form>
                                        </td>
                                    </tr>
                                    <?php endforeach; ?>
                                <?php endif; ?>
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>
        </div>
    </div>
</div>

<!-- Modal de Edición -->
<div class="modal fade" id="editTokenModal" tabindex="-1" aria-labelledby="editTokenModalLabel" aria-hidden="true">
    <div class="modal-dialog">
        <div class="modal-content bg-dark text-light border-secondary">
            <div class="modal-header border-secondary">
                <h5 class="modal-title" id="editTokenModalLabel"><i class="bi bi-pencil-square"></i> Editar Token DNS</h5>
                <button type="button" class="btn-close btn-close-white" data-bs-dismiss="modal" aria-label="Close"></button>
            </div>
            <form method="POST">
                <div class="modal-body">
                    <input type="hidden" name="action" value="edit">
                    <input type="hidden" name="token_id" id="edit_token_id">
                    
                    <div class="mb-3">
                        <label class="form-label">Nombre del Cliente / Servidor</label>
                        <input type="text" name="client_name" id="edit_client_name" class="form-control bg-dark text-light border-secondary" required>
                    </div>
                    
                    <div class="mb-3">
                        <label class="form-label">Valor del Token (API Key)</label>
                        <div class="input-group">
                            <input type="text" name="token_value" id="edit_token_value" class="form-control bg-dark text-light border-secondary font-monospace" required>
                            <button class="btn btn-outline-secondary" type="button" onclick="generateToken('edit_token_value')"><i class="bi bi-arrow-clockwise"></i></button>
                        </div>
                        <small class="text-muted">Puedes pegar aquí un token de otro servidor para sincronizarlos.</small>
                    </div>
                </div>
                <div class="modal-footer border-secondary">
                    <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancelar</button>
                    <button type="submit" class="btn btn-primary">Guardar Cambios</button>
                </div>
            </form>
        </div>
    </div>
</div>

<script src="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/js/bootstrap.bundle.min.js"></script>
<script>
function editToken(id, client, token) {
    document.getElementById('edit_token_id').value = id;
    document.getElementById('edit_client_name').value = client;
    document.getElementById('edit_token_value').value = token;
    
    var editModal = new bootstrap.Modal(document.getElementById('editTokenModal'));
    editModal.show();
}

function generateToken(inputId) {
    const chars = 'abcdef0123456789';
    let result = '';
    for (let i = 0; i < 32; i++) {
        result += chars.charAt(Math.floor(Math.random() * chars.length));
    }
    document.getElementById(inputId).value = result;
}
</script>
</body>
</html>
