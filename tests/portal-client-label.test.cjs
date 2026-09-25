const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');
const start=source.indexOf('function renderProjectRename');
const end=source.indexOf('function renderDeletingProject',start);
assert.ok(start>=0&&end>start);
class Element{
 constructor(tag='div'){this.tagName=tag.toUpperCase();this.children=[];this.className='';this.textContent='';this.value='';this.hidden=false;this.isConnected=true;this.listeners={};this.dataset={};}
 append(...children){for(const child of children){if(child==null)continue;child.parentNode=this;this.children.push(child);}}
 addEventListener(name,fn){this.listeners[name]=fn;}
 dispatch(name){return this.listeners[name]?.({preventDefault(){},currentTarget:this});}
 querySelector(selector){for(const child of this.children){if(selector.startsWith('.')&&child.className===selector.slice(1))return child;if(selector==='form'&&child.tagName==='FORM')return child;if(selector==='input'&&child.tagName==='INPUT')return child;const nested=child.querySelector?.(selector);if(nested)return nested;}return null;}
}
function harness(apiImpl){const c={document:{createElement:tag=>new Element(tag)},generation:1,api:apiImpl,submit:async(form,fn)=>fn(),renderProjectDeletion(){},disclosure(title,className){const details=new Element('details');details.className=className;const summary=new Element('summary');summary.textContent=title;details.append(summary);return details;}};vm.createContext(c);vm.runInContext(source.slice(start,end),c);return c;}
function projectCard(){const card=new Element('article');const heading=new Element('div');heading.className='project-heading-info';const name=new Element('strong');name.className='project-name';name.textContent='Website';heading.append(name);card.append(heading);return card;}
const flush=()=>new Promise(resolve=>setImmediate(resolve));
test('client label saves trimmed value and updates heading',async()=>{let request;const c=harness(async(pathname,body)=>{request={pathname,body};return {id:'p',client_label:'Acme'};});const card=projectCard();const project={id:'p',name:'Website',client_label:''};c.renderProjectRename(card,project,'owner',1);const form=card.querySelector('.project-client-label-form');form.querySelector('input').value='  Acme  ';form.dispatch('submit');await flush();assert.equal(request.pathname,'/api/projects/client-label');assert.equal(request.body.project,'p');assert.equal(request.body.label,'Acme');assert.equal(card.querySelector('.project-client-label').textContent,'Acme');});
test('blank client label clears heading label',async()=>{const c=harness(async()=>({id:'p',client_label:''}));const card=projectCard();const existing=new Element('span');existing.className='project-client-label';existing.textContent='Old';card.querySelector('.project-heading-info').append(existing);const project={id:'p',name:'Website',client_label:'Old'};c.renderProjectRename(card,project,'developer',1);const form=card.querySelector('.project-client-label-form');form.querySelector('input').value='   ';form.dispatch('submit');await flush();assert.equal(existing.textContent,'');assert.equal(project.client_label,'');});
