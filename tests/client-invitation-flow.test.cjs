const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');
const handler=source.slice(source.indexOf("window.addEventListener('hashchange'"),source.indexOf('async function loadMembers'));
function openLink(session,hash){
 let changed;const seen=[];const c=vm.createContext({URLSearchParams,Error,csrf:session,actionToken:'',pendingInvite:'',pendingClientInvite:'',location:{hash,pathname:'/',search:''},history:{replaceState:(_s,_t,url)=>seen.push(['url',url])},window:{addEventListener:(_e,fn)=>changed=fn},signedOut:()=>{c.csrf='';seen.push(['login']);},error:e=>seen.push(['error',e.message]),showFlow:kind=>seen.push(['flow',kind])});vm.runInContext(handler,c);changed();return {c,seen};
}
test('client invitation opened in a signed-out tab prompts sign-in and retains its token',()=>{
 const {c,seen}=openLink('','#client-invite=synthetic-token');assert.equal(c.pendingClientInvite,'synthetic-token');assert.ok(seen.some(x=>x[0]==='login'));assert.ok(!seen.some(x=>x[0]==='flow'));assert.deepEqual(seen[0],['url','/']);
});
test('signed-in client uses the separate client acceptance flow',()=>{
 const {c,seen}=openLink('session','#client-invite=synthetic-token');assert.equal(c.pendingInvite,'');assert.equal(c.pendingClientInvite,'synthetic-token');assert.ok(seen.some(x=>x[0]==='flow'&&x[1]==='client-invite'));
});
test('workspace invitation retains its separate acceptance flow',()=>{
 const {c,seen}=openLink('session','#invite=workspace-token');assert.equal(c.pendingClientInvite,'');assert.equal(c.pendingInvite,'workspace-token');assert.ok(seen.some(x=>x[0]==='flow'&&x[1]==='invite'));
});
