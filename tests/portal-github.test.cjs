const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');
const start=source.indexOf('function githubRepositoryName');
const end=source.indexOf('function nodeUploadRows',start);
assert.ok(start>=0&&end>start,'GitHub UI source block missing');

class Element{
 constructor(tag='div'){this.tagName=tag.toUpperCase();this.children=[];this.dataset={};this.className='';this.textContent='';this.value='';this.isConnected=true;this.parentNode=null;this.listeners={};this._selectedOptions=[];}
 get selectedOptions(){return this._selectedOptions.length?this._selectedOptions:this.tagName==='SELECT'?this.children.slice(0,1):[];} set selectedOptions(value){this._selectedOptions=value;}
 append(...items){for(const item of items){if(item==null)continue;if(typeof item==='string'){this.textContent+=item;continue;}item.parentNode=this;this.children.push(item);}}
 appendChild(item){this.append(item);return item;}
 replaceChildren(...items){for(const old of this.children)old.isConnected=false;this.children=[];this.textContent='';this.append(...items);}
 addEventListener(name,fn){this.listeners[name]=fn;}
 removeEventListener(name,fn){if(this.listeners[name]===fn)delete this.listeners[name];}
 dispatch(name,event={target:this,currentTarget:this,preventDefault(){}}){return this.listeners[name]?.(event);}
 querySelector(selector){for(const child of this.children){if(selector.startsWith('.')&&child.className===selector.slice(1))return child;if(selector==='form'&&child.tagName==='FORM')return child;if(selector==='button'&&child.tagName==='BUTTON')return child;if(selector==='input'&&child.tagName==='INPUT')return child;const nested=child.querySelector?.(selector);if(nested)return nested;}return null;}
}
function allText(element){return element.textContent+' '+element.children.map(allText).join(' ');}
function ctxFor(apiImpl,options={}){
 const context={Map,Promise,Number,encodeURIComponent,console,confirm:()=>true,crypto:{randomUUID:()=>"request-key"},document:{hidden:false,createElement:tag=>new Element(tag),addEventListener(){}},api:apiImpl,githubConnections:true,githubInstallation:false,githubImports:options.githubImports===true,githubAutoDeploy:options.githubAutoDeploy===true,generation:1,currentView:'projects',githubCallbackHandled:false,githubCallbackCode:'',githubCallbackState:'',githubCallbackError:'',location:{assign(url){context.assigned=url;}},showWebsite(id){context.shown=id;},projectCard(){return null;},refreshProject:async()=>{context.refreshed=(context.refreshed||0)+1;},setTimeout(fn){fn();return 1;},clearTimeout(){},$(){return {value:'',};},workspaces:[],selectedWebsite:'',loadProjects:async()=>{context.loaded=(context.loaded||0)+1;}};
 vm.createContext(context);vm.runInContext(source.slice(start,end),context);return context;
}
function detailsAndContent(){const details=new Element('details');details.isConnected=true;const status=new Element('p');status.className='github-status';const content=new Element('div');content.className='github-content';details.append(status,content);return {details,status,content};}
function enable(c,name){vm.runInContext(name+'=true',c);}

test('pending OAuth response opens repository chooser and sends numeric IDs',async()=>{
 let requested='';let connected;
 const c=ctxFor(async(path,body)=>{requested=path;if(path.startsWith('/api/github/connection'))return {connection:null,pending:true};if(path.startsWith('/api/github/repositories'))return {repositories:[{InstallationID:11,RepositoryID:22,RepositoryFullName:'acme/site',DefaultBranch:'main'}]};if(path==='/api/github/connect'){connected=body;return {connection:{connected:true,repository:'acme/site'}};}throw new Error('unexpected '+path);});
 const {details,status,content}=detailsAndContent();await c.renderGithubContent(content,{id:'p'},details,status);await Promise.resolve();assert.match(allText(content),/Choose a repository/);const form=content.querySelector('form');assert.ok(form);await form.dispatch('submit');await Promise.resolve();assert.equal(connected.installation_id,11);assert.equal(connected.repository_id,22);assert.equal(typeof connected.installation_id,'number');assert.equal(typeof connected.repository_id,'number');assert.match(requested,/github\/repositories/);
});

test('disconnected tombstone renders start flow and no duplicate buttons',async()=>{
 const c=ctxFor(async(path)=>{if(path.startsWith('/api/github/connection'))return {connection:{connected:false,repository:'old/site'},pending:false};throw new Error('unexpected');});
 const {details,status,content}=detailsAndContent();await c.renderGithubContent(content,{id:'p'},details,status);assert.equal(content.children.filter(child=>child.tagName==='BUTTON').length,1);assert.doesNotMatch(allText(content),/Repository connected/);await c.renderGithubContent(content,{id:'p'},details,status);assert.equal(content.children.filter(child=>child.tagName==='BUTTON').length,1);
});

test('start button returns a button and rejects non-GitHub authorization URLs',async()=>{
 const c=ctxFor(async(path)=>({url:'http://evil.example/authorize'}));const {details,status,content}=detailsAndContent();const button=c.githubStartButton({id:'p'},details,status,content);assert.equal(button.tagName,'BUTTON');assert.equal(content.children[0],button);await button.dispatch('click');assert.equal(c.assigned,undefined);assert.match(status.textContent,/invalid/);
});

test('callback after login selects returned workspace and reloads projects once',async()=>{
 let callbackCalls=0;const c=ctxFor(async(path,body)=>{callbackCalls++;assert.equal(path,'/api/github/callback');assert.equal(body.code,'code');assert.equal(body.state,'state');return {project:'p2',workspace:'w2'};});c.githubCallbackCode='code';c.githubCallbackState='state';c.workspaces=[{id:'w1'},{id:'w2'}];c.$=()=>({value:'w1'});await c.completeGithubCallback();await c.completeGithubCallback();assert.equal(callbackCalls,1);assert.equal(c.loaded,1);assert.equal(c.shown,'p2');assert.equal(c.githubCallbackCode,'');assert.equal(c.githubCallbackState,'');
});

test('empty repository chooser provides restart guidance',async()=>{
 const c=ctxFor(async(path)=>{if(path.startsWith('/api/github/connection'))return {connection:null,pending:true};if(path.startsWith('/api/github/repositories'))return {repositories:[]};throw new Error('unexpected');});const {details,status,content}=detailsAndContent();await c.renderGithubContent(content,{id:'p'},details,status);await Promise.resolve();assert.match(allText(content),/Install or manage the Launchstead GitHub App/);assert.match(allText(content),/Restart GitHub connection/);
});

test('connected repository exposes successful import and refreshes upload workflow',async()=>{
 let importBody;const c=ctxFor(async(path,body)=>{if(path.startsWith('/api/github/connection'))return {connection:{connected:true,repository:'acme/site',branch:'main'},pending:false};if(path.startsWith('/api/github/imports?'))return {imports:[{id:'job',state:'succeeded',commit:'abc123',created_at:1700000000}]};if(path==='/api/github/imports'){importBody=body;return {import:{id:'job2',state:'succeeded',commit:'def456'}};}throw new Error('unexpected '+path);});c.githubImports=true;const {details,status,content}=detailsAndContent();details.open=true;await c.renderGithubContent(content,{id:'p',role:'owner'},details,status);await Promise.resolve();const imports=content.querySelector('.github-imports');assert.ok(imports);const button=imports.querySelector('button');assert.equal(button.textContent,'Import latest commit');await button.dispatch('click');assert.equal(importBody.request_key,'request-key');assert.equal(c.refreshed,2);assert.match(allText(content),/do not auto-deploy/);
});

test('import network retry reuses key and status retry does not create a job',async()=>{
 let postCalls=0;let getCalls=0;const bodies=[];const c=ctxFor(async(path,body)=>{if(path.startsWith('/api/github/connection'))return {connection:{connected:true,repository:'acme/site'},pending:false};if(path.startsWith('/api/github/imports?')){getCalls++;if(getCalls===1)throw new Error('temporary status failure');return {imports:[]};}if(path==='/api/github/imports'){postCalls++;bodies.push(body);if(postCalls===1)throw new Error('network down');return {import:{id:'job',state:'queued'}};}throw new Error('unexpected '+path);});c.githubImports=true;const {details,status,content}=detailsAndContent();details.open=true;await c.renderGithubContent(content,{id:'p',role:'owner'},details,status);await Promise.resolve();const imports=content.querySelector('.github-imports');const button=imports.querySelector('button');assert.equal(button.textContent,'Retry import status');await button.dispatch('click');await Promise.resolve();assert.equal(postCalls,0);assert.equal(button.textContent,'Import latest commit');await button.dispatch('click');await Promise.resolve();assert.equal(postCalls,1);await button.dispatch('click');await Promise.resolve();assert.equal(postCalls,2);assert.equal(bodies[0].request_key,'request-key');assert.equal(bodies[1].request_key,'request-key');
});

test('repository access guide links to verified app in a separate tab and can refresh choices',async()=>{
 let paths=[];const c=ctxFor(async path=>{paths.push(path);if(path.startsWith('/api/github/installation?'))return {url:'https://github.com/apps/launchstead/installations/new'};if(path.startsWith('/api/github/connection?'))return {connection:null,pending:true};if(path.startsWith('/api/github/repositories?'))return {repositories:[]};throw new Error('unexpected path');});c.githubInstallation=true;
 const {details,status,content}=detailsAndContent();c.githubAccessGuide(content,{id:'p'},details,status,true);await new Promise(setImmediate);
 const action=content.children[0].children[2];const link=action.children[0];assert.equal(link.tagName,'A');assert.equal(link.target,'_blank');assert.equal(link.rel,'noopener noreferrer');assert.equal(link.href,'https://github.com/apps/launchstead/installations/new');
 await action.children[1].dispatch('click');assert.ok(paths.some(path=>path.startsWith('/api/github/connection?')));
});
test('repository access guide refuses an untrusted setup URL and offers retry',async()=>{
 const c=ctxFor(async()=>({url:'https://attacker.example/installations/new'}));c.githubInstallation=true;const {details,status,content}=detailsAndContent();c.githubAccessGuide(content,{id:'p'},details,status);await new Promise(setImmediate);
 const action=content.children[0].children[2];assert.equal(action.children.some(item=>item.tagName==='A'),false);assert.match(allText(action),/could not be verified/);assert.equal(action.children[1].textContent,'Retry GitHub App setup');
});

test('automatic deployment toggle is feature-gated and sends enabled state',async()=>{
 let body;const c=ctxFor(async(path,requestBody)=>{if(path==='/api/github/auto-deploy'){body=requestBody;return {enabled:true};}if(path.startsWith('/api/github/connection'))return {connection:{connected:true,repository:'acme/site'},pending:false};return {deployments:[]};});const {details,status,content}=detailsAndContent();c.githubAutoDeploy=true;c.renderGithubAutoDeploy(content,{id:'p'}, {auto_deploy:false},details,status);const toggle=content.querySelector('input');assert.ok(toggle);toggle.checked=true;await toggle.dispatch('change');await Promise.resolve();assert.deepEqual({project:body.project,enabled:body.enabled},{project:'p',enabled:true});
 const disabled=detailsAndContent();c.githubAutoDeploy=false;c.renderGithubAutoDeploy(disabled.content,{id:'p'},{auto_deploy:false},disabled.details,disabled.status);assert.equal(disabled.content.children.length,0);
});

test('automatic deployment progress renders safe failure guidance',async()=>{
 const c=ctxFor(async(path)=>{if(path.startsWith('/api/github/deployments'))return {deployments:[{id:'d',state:'failed',error:'source_unavailable',commit:'abc',updated_at:1700000000}]};throw new Error('unexpected');});c.githubAutoDeploy=true;const {details,status,content}=detailsAndContent();details.open=true;c.renderGithubDeployments(content,{id:'p'},'owner',1,details);await new Promise(setImmediate);assert.match(allText(content),/repository source is unavailable/i);assert.doesNotMatch(allText(content),/source_unavailable/);
});
