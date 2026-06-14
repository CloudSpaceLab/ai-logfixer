const express = require('express');
const fs = require('fs');
const path = require('path');

const app = express();
const auditPath = path.join(process.cwd(), 'uploads', 'audit.log');

app.get('/orders/readiness', (_req, res) => {
  try {
    fs.readFileSync(path.join(process.cwd(), 'config', 'readiness.json'), 'utf8');
    fs.writeFileSync(auditPath, 'readiness audit\n');
  } catch (error) {
    res.status(500).json({ detail: `permission drift: ${error.message}` });
    return;
  }
  res.json({ status: 'FIXED', lane: 'permission-drift' });
});

app.listen(8080, () => {
  console.log('permission drift express fixture listening on :8080');
});
