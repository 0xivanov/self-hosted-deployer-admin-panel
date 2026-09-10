'use strict';
const $=id=>document.getElementById(id);
let csrf='',workspaces=[],generation=0,flow='',testBilling=false,billingGeneration=0;
const fragment=new URLSearchParams(location.hash.slice(1));
let actionToken=fragment.get('verify')||fragment.get('reset')||fragment.get('invite')||'';
const initialFlow=fragment.has('verify')?'verify':fragment.has('reset')?'reset':fragment.has('invite')?'invite':'';
let pendingInvite=initialFlow==='invite'?actionToken:'';
if(location.hash)history.replaceState(null,'',location.pathname+location.search);
async function api(path,body){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined});const data=await response.json();if(!response.ok){const error=new Error(data.error||'Request failed');error.status=response.status;throw error;}return data;}
function error(e){$('error').textContent=e.message;$('error').hidden=false;}
function signedOut(){generation++;billingGeneration++;$('billing-content').replaceChildren();$('billing-panel').hidden=true;csrf='';workspaces=[];$('workspace-view').hidden=true;$('logout').hidden=true;$('login').hidden=false;$('account-flow').hidden=true;$('projects').replaceChildren();}
async function loadProjects(){const version=++generation;const workspace=$('workspace').value;const selected=workspaces.find(w=>w.id===workspace);billingGeneration++;$('billing-panel').hidden=!testBilling||!selected||selected.role!=='owner';$('billing-content').replaceChildren();$('project-form').hidden=!selected||selected.role==='viewer'; $('member-panel').hidden=!selected||selected.role!=='owner';$('members').replaceChildren();$('projects').replaceChildren();if(!workspace)return;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;for(const project of data.projects){const card=document.createElement('div');card.className='project';const name=document.createElement('strong');name.textContent=project.name;const kind=document.createElement('span');kind.textContent=(project.kind==='node'?'Node.js':'Static website');card.append(name,kind);$('projects').append(card);await projectUploads(card,project,selected.role,version);if(version!==generation)return;}if(!data.projects.length)$('projects').textContent='No projects yet.';if(selected.role==='owner'){await loadMembers(workspace,version);if(version===generation&&testBilling)await loadBilling(workspace,version);}}
async function loadSession(){const data=await api('/api/session');csrf=data.csrf;workspaces=data.workspaces;$('account').textContent=data.account.email;$('workspace').replaceChildren();for(const workspace of workspaces){const option=document.createElement('option');option.value=workspace.id;option.textContent=workspace.name+' · '+workspace.role;$('workspace').append(option);}$('login').hidden=true;$('workspace-view').hidden=false;$('logout').hidden=false;await loadProjects();if(pendingInvite)showFlow('invite');}
async function submit(form,fn){$('error').hidden=true;const button=form.querySelector('button');button.disabled=true;try{await fn();}catch(e){error(e);}finally{button.disabled=false;}}
$('login-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{try{await api('/api/login',{email:$('email').value,password:$('password').value});}finally{$('password').value='';}await loadSession();});});
$('project-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{await api('/api/projects',{workspace:$('workspace').value,name:$('project-name').value,kind:$('project-kind').value});$('project-name').value='';await loadProjects();});});
$('workspace').addEventListener('change',()=>loadProjects().catch(error));
$('logout').addEventListener('click',async()=>{try{await api('/api/logout',{});signedOut();}catch(e){error(e);}});
function showFlow(kind){
 flow=kind;$('error').hidden=true;$('login').hidden=true;$('workspace-view').hidden=true;$('account-flow').hidden=false;$('account-form').hidden=false;
 const titles={register:'Create your account',forgot:'Reset your password',reset:'Choose a new password',verify:'Verify your email',invite:'Join a workspace'};
 $('flow-title').textContent=titles[kind];$('flow-copy').textContent=kind==='invite'?'Accept this invitation using the verified account for the invited email address.':kind==='verify'?'Confirm that you want to verify this email address.':kind==='register'?'Create a workspace for your websites. Use a password of at least 12 characters.':kind==='reset'?'Use a password of at least 12 characters. Existing sessions will be signed out.':'Enter your account email to request a reset link.';
 for(const [name,visible] of [['email',kind==='register'||kind==='forgot'],['password',kind==='register'||kind==='reset'],['workspace',kind==='register']]){ $('flow-'+name+'-label').hidden=!visible;$('flow-'+name).required=visible;$('flow-'+name).value=''; }
 $('flow-submit').textContent=kind==='invite'?'Join workspace':kind==='verify'?'Verify email':kind==='reset'?'Change password':kind==='register'?'Create account':'Send reset link';
}
$('open-signup').addEventListener('click',()=>showFlow('register'));
$('open-forgot').addEventListener('click',()=>showFlow('forgot'));
$('back-login').addEventListener('click',()=>{actionToken='';$('error').hidden=true;$('account-flow').hidden=true;$('login').hidden=false;});
$('account-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{
 let path,body;
 if(flow==='invite'){path='/api/invitations/accept';body={token:pendingInvite||actionToken};}
 if(flow==='register'){path='/api/register';body={email:$('flow-email').value,password:$('flow-password').value,workspace:$('flow-workspace').value};}
 if(flow==='forgot'){path='/api/password/forgot';body={email:$('flow-email').value};}
 if(flow==='verify'){path='/api/verify';body={token:actionToken};}
 if(flow==='reset'){path='/api/password/reset';body={token:actionToken,password:$('flow-password').value};}
 const data=await api(path,body);$('flow-password').value='';$('account-form').hidden=true;$('flow-copy').textContent=data.message;if(flow==='reset'||flow==='verify')actionToken='';if(flow==='invite'){pendingInvite='';actionToken='';$('account-flow').hidden=true;await loadSession();}
 });});
async function initialize(){
 const config=await api('/api/config');testBilling=config.test_billing===true;$('open-signup').hidden=!config.signup;$('open-forgot').hidden=!config.account_mail;$('registration-note').textContent=config.signup?'Verify your email before signing in.':'Registration is closed.';
 if(initialFlow==='invite'){try{await loadSession();}catch(e){signedOut();if(e.status===401)error(new Error('Sign in with the invited email to accept. New users must register and verify their email first.'));else throw e;}return;}
 if(initialFlow){if(!config.account_mail)throw new Error('Account recovery is unavailable. Contact the operator.');showFlow(initialFlow);return;}
 try{await loadSession();}catch(e){signedOut();if(e.status!==401)throw e;}
}
initialize().catch(error);

window.addEventListener('hashchange',()=>{
 const params=new URLSearchParams(location.hash.slice(1));
 const kind=params.has('verify')?'verify':params.has('reset')?'reset':params.has('invite')?'invite':'';
 if(!kind)return;actionToken=params.get(kind)||'';history.replaceState(null,'',location.pathname+location.search);if(kind==='invite')pendingInvite=actionToken;showFlow(kind);
});

async function loadMembers(workspace,version){
 const data=await api('/api/members?workspace='+encodeURIComponent(workspace));if(version!==generation)return;
 for(const member of data.members){
  const row=document.createElement('form');row.className='project';const title=document.createElement('strong');title.textContent=member.email;
  const label=document.createElement('label');label.textContent='Role for '+member.email;
  const select=document.createElement('select');for(const [value,text] of [['owner','Owner'],['developer','Developer'],['viewer','Viewer'],['','Remove from workspace']]){const option=document.createElement('option');option.value=value;option.textContent=text;select.append(option);}select.value=member.role;label.append(select);
  const button=document.createElement('button');button.textContent='Apply change';row.append(title,label,button);
  row.addEventListener('submit',event=>{event.preventDefault();if(select.value===member.role)return;if(!confirm('Change access for '+member.email+' to '+(select.value||'removed')+'? Their sessions will be signed out.'))return;
   submit(row,async()=>{await api('/api/members',{workspace,user:member.id,role:select.value});try{await loadSession();}catch(e){if(e.status===401)signedOut();else throw e;}});
  });$('members').append(row);
 }
 await loadInvitations(workspace,version);
}

async function loadInvitations(workspace,version){
 const data=await api('/api/invitations?workspace='+encodeURIComponent(workspace));if(version!==generation)return;$('invitations').replaceChildren();
 for(const invite of data.invitations){const row=document.createElement('div');row.className='project';const label=document.createElement('span');label.textContent=invite.email+' · '+invite.role+' · expires '+new Date(invite.expires_at*1000).toLocaleDateString();const button=document.createElement('button');button.textContent='Revoke invitation';button.addEventListener('click',async()=>{button.disabled=true;try{await api('/api/invitations/revoke',{workspace,id:invite.id});await loadProjects();}catch(e){error(e);button.disabled=false;}});row.append(label,button);$('invitations').append(row);}
}
$('invite-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{await api('/api/invitations',{workspace:$('workspace').value,email:$('invite-email').value,role:$('invite-role').value});$('invite-email').value='';await loadProjects();});});

async function projectUploads(card,project,role,version){
 const [result,publication]=await Promise.all([api('/api/uploads?project='+encodeURIComponent(project.id)),api('/api/publications?project='+encodeURIComponent(project.id))]);if(version!==generation)return;
 const pending=publication.jobs.some(j=>j.state==='queued'||j.state==='running');const active=publication.jobs.find(j=>j.id===publication.active);
 const status=document.createElement('p');status.textContent=pending?(active?'Publication pending · Current revision '+active.revision:'Publication pending'):active?'Published revision '+active.revision:publication.available?'Ready to publish':'Publishing awaits runtime assignment';card.append(status);
 if(active&&publication.site){const link=document.createElement('a');link.href=publication.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent='Open website';card.append(link);}
 const refresh=document.createElement('button');refresh.textContent='Refresh release status';refresh.addEventListener('click',()=>loadProjects().catch(error));card.append(refresh);
 for(const job of publication.jobs){const line=document.createElement('p');line.textContent='Revision '+job.revision+' · '+job.state+(job.id===publication.active?' · Current':'')+(job.state==='running'?' · Applying or awaiting reconciliation':'');card.append(line);
  if(publication.available&&role==='owner'&&job.state==='running'){const resume=document.createElement('button');resume.textContent='Resume revision '+job.revision;resume.addEventListener('click',async()=>{if(!confirm('Resume revision '+job.revision+'? Its saved website files will be published using your permission. A worker that is still active cannot be interrupted.'))return;resume.disabled=true;try{await api('/api/publications/resume',{project:project.id,job:job.id,upload:job.upload_id});if(version===generation)await loadProjects();}catch(e){error(e);resume.disabled=false;}});card.append(resume);}
 }
 const copy=document.createElement('p');copy.textContent=project.kind==='node'?'ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Omit node_modules and secrets.':'ZIP up to 10 MiB. Include index.html at the root. Omit secrets.';card.append(copy);
 if(role!=='viewer'){
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload and validate';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');const response=await fetch('/api/uploads?project='+encodeURIComponent(project.id),{method:'POST',headers:{'Content-Type':'application/zip','X-CSRF-Token':csrf},body:file});const data=await response.json();if(!response.ok)throw new Error(data.error||'Upload failed');if(version===generation)await loadProjects();});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
 if(publication.available&&role!=='viewer'){const publish=document.createElement('button');const previous=publication.jobs.some(j=>j.upload_id===upload.id&&j.state==='succeeded');const isCurrent=active&&active.upload_id===upload.id;publish.textContent=isCurrent?'Current upload':previous?'Restore this upload':'Publish this upload';publish.disabled=pending||isCurrent;const requestKey=crypto.randomUUID();publish.addEventListener('click',async()=>{publish.disabled=true;try{await api('/api/publications',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await loadProjects();}catch(e){error(e);publish.disabled=false;}});row.append(publish);}
  if(role!=='viewer'){const button=document.createElement('button');button.textContent='Delete upload';button.addEventListener('click',async()=>{if(!confirm('Delete this saved upload?'))return;button.disabled=true;try{await api('/api/uploads/delete',{project:project.id,id:upload.id});if(version===generation)await loadProjects();}catch(e){error(e);button.disabled=false;}});row.append(button);}card.append(row);
 }
}

function billingPrice(price){
 const zero=new Set(['bif','clp','djf','gnf','jpy','kmf','krw','mga','pyg','rwf','vnd','vuv','xaf','xof','xpf']);
 const three=new Set(['bhd','jod','kwd','omr','tnd']);
 const digits=zero.has(price.currency)?0:three.has(price.currency)?3:2;
 const amount=new Intl.NumberFormat(undefined,{style:'currency',currency:price.currency.toUpperCase(),minimumFractionDigits:digits,maximumFractionDigits:digits}).format(price.amount_minor/10**digits);
 return amount+' every '+price.interval_count+' '+price.interval+(price.interval_count===1?'':'s');
}
async function loadBilling(workspace,version){
 const request=++billingGeneration;
 const [catalog,status]=await Promise.all([api('/api/billing/offers?workspace='+encodeURIComponent(workspace)),api('/api/billing/status?workspace='+encodeURIComponent(workspace))]);
 if(version!==generation||request!==billingGeneration)return;
 const content=$('billing-content');content.replaceChildren();
 const note=text=>{const p=document.createElement('p');p.textContent=text;content.append(p);};
 const action=(text,fn)=>{const button=document.createElement('button');button.textContent=text;button.addEventListener('click',async()=>{button.disabled=true;try{await fn();if(version===generation)await loadBilling(workspace,version);}catch(e){if(version===generation)error(e);}finally{button.disabled=false;}});content.append(button);};
 if(status.checkout){
  const checkout=status.checkout;
  note(checkout.state==='completed'?'Checkout completed. Hosting activation is awaiting billing reconciliation.':checkout.state==='open'?'Your test checkout is ready. Review the final amount on Stripe.':'Preparing your test checkout. Refresh shortly.');
  note('Selected plan: '+checkout.plan);
  if(checkout.state==='open'&&checkout.url){const url=new URL(checkout.url);if(url.protocol==='https:'&&url.host==='checkout.stripe.com'&&!url.username&&!url.password){const link=document.createElement('a');link.href=url.href;link.target='_blank';link.rel='noopener noreferrer';link.textContent='Open Stripe test checkout';content.append(link);}}
  return;
 }
 if(status.customer_state==='not_started'){note('Set up billing for this workspace to choose a hosting plan.');action('Set up test billing',()=>api('/api/billing/customer',{workspace}));return;}
 if(status.customer_state==='pending'){note('Preparing your billing account. Refresh shortly.');return;}
 note('Base prices are shown below. Confirm taxes and the final total on Stripe before completing checkout.');
 if(!catalog.offers.length)note('No hosting plans are currently available.');
 for(const offer of catalog.offers){
  note(offer.plan+' · '+(offer.price?billingPrice(offer.price):'Price temporarily unavailable'));
  if(offer.price){note(offer.price.tax_behavior==='inclusive'?'Tax included in the base price.':offer.price.tax_behavior==='exclusive'?'Tax may be added at checkout.':'Tax treatment will be confirmed at checkout.');action('Choose '+offer.plan,()=>api('/api/billing/checkout',{workspace,plan:offer.plan}));}
 }
}
$('billing-refresh').addEventListener('click',()=>{const workspace=$('workspace').value;if(testBilling&&workspaces.find(w=>w.id===workspace)?.role==='owner')loadBilling(workspace,generation).catch(error);});
