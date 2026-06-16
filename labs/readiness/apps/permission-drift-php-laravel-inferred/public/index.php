<?php

if ($_SERVER['REQUEST_URI'] !== '/orders/readiness') {
    http_response_code(404);
    header('Content-Type: application/json');
    echo json_encode(['error' => 'not found']);
    return;
}

$keyPath = __DIR__ . '/../config/app.key';
if (@file_get_contents($keyPath) === false) {
    http_response_code(500);
    header('Content-Type: application/json');
    echo json_encode(['detail' => 'permission drift: config/app.key is not readable']);
    return;
}

$auditPath = __DIR__ . '/../storage/logs/readiness-' . getmypid() . '.log';
if (@file_put_contents($auditPath, "readiness audit\n") === false) {
    http_response_code(500);
    header('Content-Type: application/json');
    echo json_encode(['detail' => 'permission drift: storage/logs is not writable']);
    return;
}

header('Content-Type: application/json');
echo json_encode(['status' => 'FIXED', 'lane' => 'permission-drift']);
