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
 dispatch(name,event={target:this,currentTarget:this,preventDefault(){}}){return this.listeners[name]?.(event);}
 querySelector(selector){for(const child of this.children){if(selector==='.github-content'&&child.className==='github-content')return child;if(selector==='.github-status'&&child.className==='github-status')return child;if(selector==='form'&&child.tagName==='FORM')return child;const nested=child.querySelector?.(selector);if(nested)return nested;}return null;}
}
function allText(element){return element.textContent+' '+element.children.map(allText).join(' ');}
function ctxFor(apiImpl){
 const context={Map,Promise,Number,encodeURIComponent,console,confirm:()=>true,document:{createElement:tag=>new Element(tag)},api:apiImpl,githubConnections:true,generation:1,githubCallbackHandled:false,githubCallbackCode:'',githubCallbackState:'',githubCallbackError:'',location:{assign(url){context.assigned=url;}},showWebsite(id){context.shown=id;},projectCard(){return null;},setTimeout(fn){fn();return 1;},clearTimeout(){},$(){return {value:'',};},workspaces:[],selectedWebsite:'',loadProjects:async()=>{context.loaded=(context.loaded||0)+1;}};
 vm.createContext(context);vm.runInContext(source.slice(start,end),context);return context;
}
function detailsAndContent(){const details=new Element('details');details.isConnected=true;const status=new Element('p');status.className='github-status';const content=new Element('div');content.className='github-content';details.append(status,content);return {details,status,content};}

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
