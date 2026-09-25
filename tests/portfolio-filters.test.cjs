const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync('internal/portal/static/portal.js','utf8');
class Element{
 constructor(value=''){this.value=value;this.children=[];this.dataset={};this.hidden=false;this.textContent='';this.moves=0;this.classList={toggle(){}};}
 append(child){const old=this.children.indexOf(child);if(old>=0)this.children.splice(old,1);this.children.push(child);this.moves++;}
 replaceChildren(...items){this.children=items;}
 get options(){return this.children;}
 querySelectorAll(){return this.children;}
}
function card(id,name,label,status,kind='static'){
 const e=new Element();e.dataset={projectId:id,projectKind:kind};
 e.fields={'.project-name':{textContent:name},'.project-client-label':{textContent:label},'.project-workflow':{dataset:{portfolioStatus:status}}};
 e.querySelector=s=>e.fields[s];return e;
}
function harness(){
 const elements={};for(const id of ['website-search','website-type-filter','website-status-filter','website-label-filter','website-sort','projects','projects-panel','website-detail-toolbar','website-count','website-no-results'])elements[id]=new Element();
 for(const id of ['website-type-filter','website-status-filter','website-label-filter'])elements[id].value='all';
 elements['website-sort'].value='az';
 const cards=[card('b','Zulu','Acme','attention'),card('a','Alpha','','live'),card('c','Other','all','inprogress','node')];
 elements.projects.children=[...cards];
 const context=vm.createContext({$:id=>elements[id],document:{createElement:()=>new Element()},selectedWebsite:'',updateWebsiteSummary(){},setWebsiteText:(e,t)=>{e.textContent=t;}});
 const start=source.indexOf('function filterWebsiteCards()'),end=source.indexOf("$('back-to-websites').addEventListener",start);
 vm.runInContext(source.slice(start,end),context);
 return {elements,cards,context,run:()=>context.filterWebsiteCards()};
}
test('client, status, type and search filters combine without replacing cards',()=>{
 const {elements,cards,run}=harness();elements['website-label-filter'].value='client:Acme';elements['website-status-filter'].value='attention';elements['website-search'].value='zulu';run();
 assert.deepEqual(cards.map(c=>c.hidden),[false,true,true]);
 assert.equal(elements['website-count'].textContent,'1 of 3 websites');
 assert.equal(elements.projects.children[2],cards[0]);
 elements['website-label-filter'].value='client:all';elements['website-status-filter'].value='all';elements['website-search'].value='';run();
 assert.deepEqual(cards.map(c=>c.hidden),[true,true,false]);
});
test('sort is stable on repeated refresh and preserves selected details',()=>{
 const {elements,cards,context,run}=harness();run();const moves=elements.projects.moves;run();assert.equal(elements.projects.moves,moves);
 context.selectedWebsite='b';elements['website-sort'].value='za';elements['website-status-filter'].value='live';run();
 assert.equal(cards[0].hidden,false);assert.equal(elements.projects.moves,moves);
 context.selectedWebsite='';run();assert.equal(elements.projects.children[0],cards[0]);
});
test('removed client selection falls back immediately and unknown status is not live',()=>{
 const {elements,cards,run}=harness();elements['website-label-filter'].value='client:missing';run();
 assert.equal(elements['website-label-filter'].value,'all');assert.ok(cards.every(c=>!c.hidden));
 cards[1].fields['.project-workflow']=null;elements['website-status-filter'].value='live';run();assert.ok(cards.every(c=>c.hidden));
 assert.equal(elements['website-no-results'].hidden,false);
});
