const express = require('express');

const app = express();

app.get('/orders/:id', (request, response) => {
  if (process.env.FAULT_MODE === 'runtime_error') {
    throw new Error('database unavailable');
  }

  response.json({ status: 'BROKEN', orderId: request.params.id });
});

app.listen(process.env.PORT || 8080, () => {
  console.log('readiness express app listening');
});
