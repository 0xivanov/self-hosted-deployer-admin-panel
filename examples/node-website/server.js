const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const page = fs.readFileSync(path.join(__dirname, 'dist/index.html'));
const port = Number(process.env.PORT || 3000);
http.createServer((request, response) => {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    response.writeHead(405, { Allow: 'GET, HEAD' });
    return response.end();
  }
  const health = request.url.split('?')[0] === '/health';
  response.writeHead(200, {
    'Content-Type': health ? 'text/plain; charset=utf-8' : 'text/html; charset=utf-8',
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff'
  });
  response.end(request.method === 'HEAD' ? undefined : health ? 'ok' : page);
}).listen(port, '127.0.0.1');
