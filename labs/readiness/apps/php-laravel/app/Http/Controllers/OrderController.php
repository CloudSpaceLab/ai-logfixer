<?php

namespace App\Http\Controllers;

class OrderController
{
    public function show(): string
    {
        if (getenv('FAULT_MODE') === 'runtime_error') {
            throw new \RuntimeException('database unavailable');
        }

        return "BROKEN";
    }
}
