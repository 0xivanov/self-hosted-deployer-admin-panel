const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');

const html=fs.readFileSync(path.join(__dirname,'../internal/portal/static/index.html'),'utf8');
const js=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');

test('domain checkout is feature gated and offers project selection',()=>{
 assert.match(html,/id="project-domain-checkout"/);
 assert.match(html,/Find a domain after creating this website/);
 assert.match(js,/domainCheckout=checkoutConfig\.domain_checkout===true/);
 assert.match(js,/\/api\/domains\/checkout/);
 assert.match(js,/Website project for domain purchase/);
 assert.match(js,/Pay with Stripe test checkout/);
});

test('sandbox purchase status uses the explicit non-public connected label',()=>{
 assert.match(js,/\/api\/domains\/purchases\?workspace=/);
 assert.match(js,/\/api\/domains\/sync/);
 assert.match(js,/connected:'Sandbox domain connected'/);
 assert.match(html,/not publicly resolvable/);
 assert.match(js,/domainCheckoutReturn=githubCallbackParams\.get\('domain_checkout'\)==='return'/);
 assert.match(js,/selectWorkspaceView\('domains'\)/);
});

test('checkout links are restricted to Stripe checkout host',()=>{
 assert.match(js,/url\.protocol==='https:'&&url\.host==='checkout\.stripe\.com'/);
 assert.match(js,/data\.orders\.filter\(order=>!purchasesByOrder\.has\(order\.id\)\)/);
 assert.match(js,/for\(const order of unpaidOrders\)/);
});

test('website domain handoff uses the created project id after refresh',()=>{
 assert.match(js,/const created=await api\('\/api\/projects'/);
 assert.match(js,/await loadProjects\(\);if\(findDomain&&workspace===\$\('workspace'\)\.value&&created\?\.id\)/);
 assert.doesNotMatch(js,/setTimeout\(async\(\)=>\{try\{const data=await api\('\/api\/projects\?workspace=/);
 assert.match(js,/domainPurchaseGeneration/);
});
