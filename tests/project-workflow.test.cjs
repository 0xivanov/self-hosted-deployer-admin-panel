const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../internal/portal/static/portal.js'),'utf8');const c=vm.createContext({nodeJobActive:j=>j&&(j.state==='queued'||j.state==='running')});vm.runInContext(source.slice(source.indexOf('// Project workflow state.'),source.indexOf('// End project workflow state.')),c);
const upload={id:'new'};const build={id:'build',upload_id:'new',state:'succeeded'};const release={build_id:'build'};
for(const [name,kind,uploads,data,action,title] of [
 ['empty node','node',[],{available:true},'upload','Add your website files'],
 ['uploaded unbuilt','node',[upload],{available:true},'build','Files uploaded · Build next'],
 ['built node','node',[upload],{available:true,builds:[build],releases:[release]},'publish','Ready to publish'],
 ['old release must not publish newer upload','node',[upload],{available:true,builds:[{...build,upload_id:'old'}],releases:[release]},'build','Files uploaded · Build next'],
 ['live node','node',[upload],{available:true,builds:[build],releases:[release],active:{release_id:'build'}},'upload','Your website is live'],
 ['failed build','node',[upload],{available:true,builds:[{...build,state:'failed'}]},'build','Your build needs attention'],
 ['building','node',[upload],{available:true,builds:[{...build,state:'running'}]},undefined,'Building your website'],
 ['publishing','node',[upload],{available:true,deployments:[{state:'queued'}]},undefined,'Publishing your website'],
 ['static empty','static',[],{available:true},'upload','Add your website files'],
 ['static ready','static',[upload],{available:true},'publish','Ready to publish'],
 ['static live','static',[upload],{available:true,active:'job',jobs:[{id:'job',upload_id:'new',state:'succeeded'}]},'upload','Your website is live'],
 ['setup pending','node',[upload],{available:false},undefined,'Waiting for a hosting assignment']
])test(name,()=>{const result=c.projectWorkflow(kind,uploads,data);assert.equal(result.action,action);assert.equal(result.title,title);});

test("unassigned hosting never displays active progress",()=>{assert.equal(c.projectWorkflow("node",[upload],{available:false}).busy,undefined);});
