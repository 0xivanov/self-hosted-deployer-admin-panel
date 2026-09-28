const {test}=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');
const js=fs.readFileSync('internal/portal/static/portal.js','utf8');
function section(name){return {name,hidden:false,matches(selector){return selector.split(',').some(s=>s==='.'+name);}};}
test('website navigation isolates dangerous actions and keeps upload controls reachable',()=>{
 const children=['project-heading','website-detail-links','project-overview','project-statistics','project-workflow','refresh-status','upload-details','project-history','project-danger'].map(section);const select={value:''};
 const card={children,dataset:{},querySelector(selector){return selector==='.project-section-select'?select:children.find(c=>c.matches(selector));},querySelectorAll(){return [];}};
 const context=vm.createContext({});vm.runInContext(js.slice(js.indexOf('function setProjectSection('),js.indexOf('function showWebsite(')),context);
 for(const [key,visible] of [['overview',['project-overview']],['publishing',['project-workflow','refresh-status']],['versions',['upload-details','project-history']],['danger',['project-danger']]]){
 context.setProjectSection(card,key);assert.equal(select.value,key);assert.deepEqual(children.filter(c=>!c.hidden&&!['project-heading','website-detail-links'].includes(c.name)).map(c=>c.name),visible);
 }
});
test('overview and statistics survive upload organization while data refreshes',()=>{
 const a=section('project-overview'),b=section('project-statistics');for(const child of [a,b])child.classList={contains:n=>n===child.name};
 const card={children:[a,b],append(){throw new Error('Overview must not become version history');}};
 const ctx=vm.createContext({disclosure(){return {children:[{}],append(){throw new Error('Overview moved into disclosure');}};}});
 vm.runInContext(js.slice(js.indexOf('function organizeProject('),js.indexOf('function projectDomainStateLabel(')),ctx);ctx.organizeProject(card);
});

test('section switching does not hide upload forms while async rendering is unfinished',()=>{
 const form=section('upload-form');form.tagName='FORM';
 const workflow=section('project-workflow');const overview=section('project-overview');
 const card={children:[overview,workflow,form],dataset:{},querySelector(selector){return this.children.find(c=>c.matches(selector));},querySelectorAll(){return [];}};
 const context=vm.createContext({});vm.runInContext(js.slice(js.indexOf('function setProjectSection('),js.indexOf('function showWebsite(')),context);
 context.setProjectSection(card,'overview');assert.equal(form.hidden,false);
 const disclosures=[];context.disclosure=(title,name)=>{const d={name,children:[{}],append(child){d.children.push(child);}};disclosures.push(d);return d;};
 for(const child of card.children)child.classList={contains:n=>n===child.name};card.append=()=>{};
 vm.runInContext(js.slice(js.indexOf('function organizeProject('),js.indexOf('function projectDomainStateLabel(')),context);context.organizeProject(card);
 assert.ok(disclosures.find(d=>d.name==='upload-details').children.includes(form));assert.equal(form.hidden,false);
});
