<?php

use App\Http\Controllers\OrderController;

Route::get('/orders/{id}', [OrderController::class, 'show']);
