// Bounded S3 failure fixture from the AWS REST error contract. This is not
// a functional S3 emulator; successful storage is tested separately with MinIO.
const http = require('http');
http.createServer((req, res) => {
  req.resume();
  if (req.method === 'HEAD') { res.writeHead(200); res.end(); return; }
  if (req.method === 'PUT' || req.method === 'POST') {
    res.writeHead(500, {'Content-Type': 'application/xml'});
    res.end('<Error><Code>InternalError</Code><Message>Injected S3 failure</Message><RequestId>local-test</RequestId></Error>');
    return;
  }
  res.writeHead(501); res.end('Endpoint not modeled');
}).listen(process.env.PORT || 4500);
