const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync('internal/portal/static/shop.js','utf8');
async function shop(mode,sessionMode=mode){
 const nodes=new Map(),saved=new Map();
 const make=()=>({hidden:true,disabled:false,textContent:'',value:'',addEventListener(){},append(){},replaceChildren(){},removeAttribute(){}});
 const node=id=>{if(!nodes.has(id))nodes.set(id,make());return nodes.get(id);};
 const context=vm.createContext({document:{getElementById:node,createElement:make},URL,URLSearchParams,Intl,location:{search:'',pathname:'/shop'},history:{replaceState(){}},sessionStorage:{setItem:(k,v)=>saved.set(k,v),getItem:k=>saved.get(k),removeItem:k=>saved.delete(k)},fetch:async path=>({ok:true,status:200,json:async()=>path==='/api/config'?{merchant_mode:mode}:{csrf:'csrf',merchant_mode:sessionMode}})});
 vm.runInContext(source,context);
 await new Promise(resolve=>setImmediate(resolve));
 return {context,node,saved};
}
for(const mode of ['test','live'])test(`${mode} checkout labels, refunds and saved orders match the mode`,async()=>{
 const {context,node,saved}=await shop(mode);
 assert.match(node('shop-mode-notice').textContent,mode==='live'?/Live checkout/:/Test checkout/);
 const order={id:'a'.repeat(64),mode,name:'Product',currency:'eur',amount_minor:100,state:'complete',payment_status:'paid'};
 vm.runInContext(`showOrder(${JSON.stringify(order)})`,context);
 assert.equal(saved.get('merchant-last-order:'+mode),order.id);
 assert.match(node('message').textContent,mode==='test'?/^Test payment confirmed/:/^Payment confirmed/);
 order.refund={state:'succeeded',currency:'eur',amount_minor:100};
 vm.runInContext(`showOrder(${JSON.stringify(order)})`,context);
 assert.match(node('message').textContent,mode==='test'?/test refund succeeded/:/the refund succeeded/);
 order.mode=mode==='live'?'test':'live';
 assert.throws(()=>vm.runInContext(`showOrder(${JSON.stringify(order)})`,context),/mode changed/);
});
test('unavailable mode and a changed session leave checkout disabled',async()=>{
 for(const [mode,session] of [['', ''],['live','test']]){
 const {node}=await shop(mode,session);
 assert.equal(node('buy').disabled,true);
 assert.equal(node('title').textContent,'Checkout unavailable');
 }
});
