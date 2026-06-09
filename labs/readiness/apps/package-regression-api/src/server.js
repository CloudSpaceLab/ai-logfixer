const express = require('express');
const taxClient = require('@acme/tax-client');

const app = express();

app.get('/orders/readiness', (_req, res) => {
  try {
    const quote = taxClient.quoteOrder({ total: 125 });
    res.json({ status: quote.status, lane: 'package-regression' });
  } catch (error) {
    res.status(500).json({
      error: 'package regression in @acme/tax-client',
      detail: error.message,
    });
  }
});

app.listen(8080, () => {
  console.log('package regression fixture listening on :8080');
});
