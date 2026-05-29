<?php

require __DIR__ . '/../app/Http/Controllers/OrderController.php';

use App\Http\Controllers\OrderController;

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);

if (preg_match('#^/orders/([^/]+)$#', $path)) {
    try {
        http_response_code(200);
        echo (new OrderController())->show();
    } catch (Throwable $error) {
        error_log($error);
        http_response_code(503);
        echo $error->getMessage();
    }
    return;
}

http_response_code(404);
echo 'not found';
