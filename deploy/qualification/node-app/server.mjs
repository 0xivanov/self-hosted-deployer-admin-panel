import http from 'node:http';
const server=http.createServer((req,res)=>{
 res.setHeader('Content-Type','application/json');
 res.end(JSON.stringify({status:'node-build-lab-ok',environment:process.env.NODE_ENV}));
});
server.listen(Number(process.env.PORT),process.env.HOST);
process.on('SIGTERM',()=>server.close(()=>process.exit(0)));
