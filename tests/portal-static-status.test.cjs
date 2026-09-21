const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');
function harness(){
 const start=source.indexOf('// Static publication polling.');
 const end=source.indexOf('// End static publication polling.');
 assert.ok(start>=0&&end>start,'polling source block missing');
 const timers=new Map();let seq=0;
 const card={isConnected:true,dataset:{},querySelector:()=>null};
 const c=vm.createContext({Map,JSON,Promise,AbortController,console,
  document:{hidden:false,visibilityState:'visible'},generation:1,currentView:'projects',
  staticStatusCards:new Map(),staticStatusState:null,nodeStatusInterval:5000,
  $:()=>({value:'workspace'}),projectCard:()=>card,
  setTimeout:fn=>{timers.set(++seq,fn);return seq;},clearTimeout:id=>timers.delete(id),
  signedOut:()=>{c.signedOutCount++;},signedOutCount:0,
  api:async()=>c.result,refreshProject:async()=>{c.refreshes++;},refreshes:0,
 });
 vm.runInContext(source.slice(start,end),c);
 const project={id:'p',kind:'static'};
 const pending={available:true,jobs:[{id:'job',state:'running'}],active:'',site:'https://example.test'};
 c.trackStaticStatus(project,'owner',1,pending);
 return {c,card,timers,project,pending};
}
test('unchanged pending status does not rerender; completion refreshes once',async()=>{
 const {c,pending}=harness();c.result=pending;
 await c.pollStaticStatus(c.staticStatusState);assert.equal(c.refreshes,0);
 c.result={...pending,jobs:[{id:'job',state:'succeeded'}],active:'job'};
 await c.pollStaticStatus(c.staticStatusState);assert.equal(c.refreshes,1);
});
test('hidden tab and non-project view do not request status',async()=>{
 const {c}=harness();let calls=0;c.api=async()=>{calls++;};
 c.document.hidden=true;await c.pollStaticStatus(c.staticStatusState);
 c.document.hidden=false;c.currentView='billing';await c.pollStaticStatus(c.staticStatusState);
 assert.equal(calls,0);
});
test('late responses after workspace generation change cannot refresh cards',async()=>{
 const {c,pending}=harness();let resolve;c.api=()=>new Promise(r=>{resolve=r;});
 const polling=c.pollStaticStatus(c.staticStatusState);c.generation++;
 resolve({...pending,active:'new'});await polling;assert.equal(c.refreshes,0);
});
test('deleted cards and stopped polling cannot update',async()=>{
 const {c,card}=harness();card.dataset.deleting='true';let calls=0;c.api=async()=>{calls++;};
 await c.pollStaticStatus(c.staticStatusState);assert.equal(calls,0);
 c.stopStaticStatusRefresh();assert.equal(c.staticStatusCards.size,0);
});
test('completion retires polling; a new publication reactivates it',async()=>{
 const {c,project,pending}=harness();
 c.result={...pending,jobs:[{id:'job',state:'succeeded'}],active:'job'};
 c.refreshProject=async()=>{c.trackStaticStatus(project,'owner',1,c.result);};
 await c.pollStaticStatus(c.staticStatusState);
 assert.equal(c.staticStatusCards.size,0);assert.equal(c.staticStatusState,null);
 c.trackStaticStatus(project,'owner',1,pending);assert.ok(c.staticStatusState);
});
test('manual refresh supersedes an older in-flight response',async()=>{
 const {c,project,pending}=harness();let resolve;c.api=()=>new Promise(r=>{resolve=r;});
 const polling=c.pollStaticStatus(c.staticStatusState);
 c.trackStaticStatus(project,'owner',1,{...pending,active:'newer'});
 resolve({...pending,active:'old'});await polling;assert.equal(c.refreshes,0);
});
test('failed refresh retries instead of saving an unrendered snapshot',async()=>{
 const {c,pending}=harness();c.result={...pending,active:'new'};let attempts=0;
 c.refreshProject=async()=>{attempts++;throw new Error('temporary');};
 await c.pollStaticStatus(c.staticStatusState);await c.pollStaticStatus(c.staticStatusState);
 assert.equal(attempts,2);
});
