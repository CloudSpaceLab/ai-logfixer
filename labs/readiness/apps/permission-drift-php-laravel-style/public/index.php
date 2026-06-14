<?php

if ($_SERVER['REQUEST_URI'] !== '/orders/readiness') {
    http_response_code(404);
    header('Content-Type: application/json');
    echo json_encode(['error' => 'not found']);
    return;
}

$auditPath = __DIR__ . '/../storage/logs/audit.log';
if (@file_put_contents($auditPath, "readiness audit\n") === false) {
    http_response_code(500);
    header('Content-Type: application/json');
    echo json_encode(['detail' => 'permission drift: storage/logs is not writable']);
    return;
}

header('Content-Type: application/json');
echo json_encode(['status' => 'FIXED', 'lane' => 'permission-drift']);
