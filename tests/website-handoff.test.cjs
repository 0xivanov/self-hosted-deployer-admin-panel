const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const source=fs.readFileSync('internal/portal/static/portal.js','utf8');
const start=source.indexOf('function websiteHandoffText('),end=source.indexOf('function showWebsiteHandoff(',start);
const context={URL,Date};vm.createContext(context);vm.runInContext(source.slice(start,end),context);
test('handoff contains a dated status and strips query, fragment and path from public address',()=>{
 const result=context.websiteHandoffText('Client website','Publishing update','https://example.com/private?token=secret#proof',new Date('2026-09-25T12:00:00Z'));
 assert.match(result,/Client website\nPublishing status: Publishing update/);
 assert.match(result,/Website: https:\/\/example.com\n/);
 assert.match(result,/Snapshot: 2026-09-25T12:00:00.000Z/);
 assert.match(result,/does not grant portal access/);
 assert.doesNotMatch(result,/private|secret|proof/);
});
test('handoff excludes credential-bearing, unsafe and unavailable addresses',()=>{
 for(const href of ['https://user:secret@example.com/','javascript:alert(1)','http://example.com/',undefined]){
  const result=context.websiteHandoffText('Website','Setting up hosting',href);
  assert.match(result,/No published address yet/);
  assert.doesNotMatch(result,/secret|alert|Website: http/);
 }
});
