const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');
const start=source.indexOf('function renderContainerSavedList');
const end=source.indexOf('function renderContainerLive',start);
assert.ok(start>=0&&end>start);
class Element{
 constructor(tag='div'){this.tagName=tag.toUpperCase();this.children=[];this.className='';this.textContent='';this.disabled=false;this.isConnected=true;this.listeners={};}
 append(...children){for(const child of children){if(child==null)continue;this.children.push(child);}}
 replaceChildren(...children){this.children=[];this.append(...children);}
 addEventListener(name,fn){this.listeners[name]=fn;}
 setAttribute(){}
 dispatch(name){return this.listeners[name]?.({preventDefault(){}});}
 querySelector(selector){for(const child of this.children){if(selector.startsWith('.')&&child.className===selector.slice(1))return child;if(selector==='button'&&child.tagName==='BUTTON')return child;const nested=child.querySelector?.(selector);if(nested)return nested;}return null;}
}
function text(node){return node.textContent+' '+node.children.map(text).join(' ');}
function buttons(node){return node.children.flatMap(child=>[...(child.tagName==='BUTTON'?[child]:[]),...buttons(child)]);}
function harness(apiImpl){const c={document:{createElement:tag=>new Element(tag)},generation:1,api:apiImpl,refreshProject:async()=>{c.refreshed=(c.refreshed||0)+1;}};vm.createContext(c);vm.runInContext(source.slice(start,end),c);return c;}
test('credential deletion routes to the credential endpoint and refreshes',async()=>{let request;const c=harness(async(path,body)=>{request={path,body};return {};});const panel=new Element('details');c.renderContainerSavedList(panel,[{id:'cred-1',label:'Registry',registry:'ghcr.io',deletable:true}],'credential',{id:'project-1'},'owner',1);const button=panel.querySelector('button');await button.dispatch('click');assert.equal(request.path,'/api/container/credentials/delete');assert.equal(request.body.project,'project-1');assert.equal(request.body.id,'cred-1');assert.equal(c.refreshed,1);});
test('retained versions are visibly blocked and delete errors restore the button',async()=>{let calls=0;const c=harness(async()=>{calls++;throw new Error('retained-release');});const panel=new Element('details');c.renderContainerSavedList(panel,[{id:'env-1',label:'Production',names:['TOKEN'],deletable:false},{id:'env-2',label:'Unused',names:[],deletable:true}],'environment',{id:'p'},'developer',1);const savedButtons=buttons(panel);assert.equal(savedButtons[0].disabled,true);assert.match(text(panel),/Retained by a release and cannot be deleted/);await savedButtons[1].dispatch('click');await new Promise(setImmediate);assert.equal(calls,1);assert.equal(savedButtons[1].disabled,false);assert.match(text(panel),/retained-release/);});
