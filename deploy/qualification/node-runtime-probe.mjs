// Trusted synthetic fixture only, never generated from customer code.
import fs from 'node:fs';
import net from 'node:net';
import http from 'node:http';
import assert from 'node:assert/strict';
const checks = {};
assert.equal(process.getuid(), 60000);
assert.equal(process.getgid(), 60000);
assert.ok(process.getgroups().every(group=>group===60000));
const status = fs.readFileSync('/proc/self/status', 'utf8');
assert.match(status, /NoNewPrivs:\s+1/);
assert.match(status, /CapEff:\s+0+\n/);
checks.unprivileged = true;
for (const target of ['/etc/node-runtime-write-probe', './write-probe']) {
 let denied = false;
 try { fs.writeFileSync(target, 'forbidden'); } catch (e) { denied = ['EACCES','EPERM','EROFS'].includes(e.code); }
 assert.ok(denied, 'write unexpectedly allowed: ' + target);
}
assert.throws(() => fs.readFileSync('/opt/deployer-node/management-canary'), /EACCES|EPERM/);
checks.read_only_release_and_private_control = true;
for (const [target,limit] of [['/work',64<<20],['/tmp',16<<20],['/var/tmp',16<<20]]) {
 const info = fs.statfsSync(target);
 assert.ok(info.blocks * info.bsize <= limit, 'unbounded temporary mount: '+target);
 fs.writeFileSync(target+'/write-probe', 'allowed');
 fs.unlinkSync(target+'/write-probe');
}
checks.bounded_temporary_mounts = true;
const group = fs.readFileSync('/proc/self/cgroup','utf8').trim().split('\n').find(l=>l.startsWith('0::')).slice(3);
const cgroup = '/sys/fs/cgroup'+group;
assert.equal(fs.readFileSync(cgroup+'/memory.max','utf8').trim(), String(256<<20));
assert.equal(fs.readFileSync(cgroup+'/memory.swap.max','utf8').trim(), '0');
assert.equal(fs.readFileSync(cgroup+'/pids.max','utf8').trim(), '64');
const [quota,period]=fs.readFileSync(cgroup+'/cpu.max','utf8').trim().split(' ').map(Number);
assert.equal(quota,period);
checks.cgroup_limits = true;
const probe=JSON.parse(fs.readFileSync('./probe.json','utf8'));
const connected=await new Promise(resolve=>{
 const socket=net.createConnection({host:probe.host,port:probe.port});
 socket.setTimeout(700);
 socket.once('connect',()=>{socket.destroy();resolve(true)});
 socket.once('timeout',()=>{socket.destroy();resolve(false)});
 socket.once('error',()=>{socket.destroy();resolve(false)});
});
assert.equal(connected,false,'non-loopback network access unexpectedly allowed');
checks.non_loopback_blocked = true;
const server=http.createServer((req,res)=>{
 res.setHeader('Content-Type','application/json');
 res.end(JSON.stringify({status:'node-runtime-unit-ok',uid:process.getuid(),pid:process.pid,checks}));
});
server.listen(Number(process.env.PORT),process.env.HOST);
process.on('SIGTERM',()=>server.close(()=>process.exit(0)));
