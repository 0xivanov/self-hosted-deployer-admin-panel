'use strict';
const $=id=>document.getElementById(id);
let csrf='',workspaces=[],generation=0,flow='',testBilling=false,billingManagement=false,billingGeneration=0,domainQuotes=false,domainExpiryTimer;
const fragment=new URLSearchParams(location.hash.slice(1));
let actionToken=fragment.get('verify')||fragment.get('reset')||fragment.get('invite')||'';
const initialFlow=fragment.has('verify')?'verify':fragment.has('reset')?'reset':fragment.has('invite')?'invite':'';
let pendingInvite=initialFlow==='invite'?actionToken:'';
if(location.hash)history.replaceState(null,'',location.pathname+location.search);
async function api(path,body){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined});const data=await response.json();if(!response.ok){const error=new Error(data.error||'Request failed');error.status=response.status;throw error;}return data;}
function error(e){$('error').textContent=e.message;$('error').hidden=false;}
function signedOut(){generation++;$('error').hidden=true;resetDomainPanel();$('domain-panel').hidden=true;billingGeneration++;$('billing-content').replaceChildren();$('billing-panel').hidden=true;csrf='';workspaces=[];$('workspace-view').hidden=true;$('logout').hidden=true;$('login').hidden=false;$('account-flow').hidden=true;$('projects').replaceChildren();}
async function loadProjects(){const version=++generation;const workspace=$('workspace').value;const selected=workspaces.find(w=>w.id===workspace);resetDomainPanel();$('domain-panel').hidden=!domainQuotes||!selected||selected.role!=='owner';billingGeneration++;$('billing-panel').hidden=!testBilling||!selected||selected.role!=='owner';$('billing-content').replaceChildren();$('project-form').hidden=!selected||selected.role==='viewer'; $('member-panel').hidden=!selected||selected.role!=='owner';$('members').replaceChildren();$('projects').replaceChildren();if(!workspace)return;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;for(const project of data.projects){const card=document.createElement('div');card.className='project';const name=document.createElement('strong');name.textContent=project.name;const kind=document.createElement('span');kind.textContent=(project.kind==='node'?'Node.js':'Static website');card.append(name,kind);$('projects').append(card);await projectUploads(card,project,selected.role,version);if(version!==generation)return;}if(!data.projects.length)$('projects').textContent='No projects yet.';if(selected.role==='owner'){await loadMembers(workspace,version);if(version===generation&&testBilling)await loadBilling(workspace,version);}}
async function loadSession(){const data=await api('/api/session');csrf=data.csrf;workspaces=data.workspaces;$('account').textContent=data.account.email;$('workspace').replaceChildren();for(const workspace of workspaces){const option=document.createElement('option');option.value=workspace.id;option.textContent=workspace.name+' · '+workspace.role;$('workspace').append(option);}$('login').hidden=true;$('workspace-view').hidden=false;$('logout').hidden=false;await loadProjects();if(pendingInvite)showFlow('invite');}
async function submit(form,fn){$('error').hidden=true;const button=form.querySelector('button');button.disabled=true;try{await fn();}catch(e){error(e);}finally{button.disabled=false;}}
$('login-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{try{await api('/api/login',{email:$('email').value,password:$('password').value});}finally{$('password').value='';}await loadSession();});});
$('project-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{await api('/api/projects',{workspace:$('workspace').value,name:$('project-name').value,kind:$('project-kind').value});$('project-name').value='';await loadProjects();});});
$('workspace').addEventListener('change',()=>loadProjects().catch(error));
$('logout').addEventListener('click',async()=>{try{await api('/api/logout',{});signedOut();}catch(e){error(e);}});
function showFlow(kind){
 flow=kind;$('error').hidden=true;$('login').hidden=true;$('workspace-view').hidden=true;$('account-flow').hidden=false;$('account-form').hidden=false;
 const titles={register:'Create your account',resend:'Resend verification email',forgot:'Reset your password',reset:'Choose a new password',verify:'Verify your email',invite:'Join a workspace'};
 $('flow-title').textContent=titles[kind];$('flow-copy').textContent=kind==='resend'?'Enter your account email. Check your spam folder and wait at least a minute between requests.':kind==='invite'?'Accept this invitation using the verified account for the invited email address.':kind==='verify'?'Confirm that you want to verify this email address.':kind==='register'?'Create a workspace for your websites. Use a password of at least 12 characters.':kind==='reset'?'Use a password of at least 12 characters. Existing sessions will be signed out.':'Enter your account email to request a reset link.';
 for(const [name,visible] of [['email',kind==='register'||kind==='forgot'||kind==='resend'],['password',kind==='register'||kind==='reset'],['workspace',kind==='register']]){ $('flow-'+name+'-label').hidden=!visible;$('flow-'+name).required=visible;$('flow-'+name).value=''; }
 $('flow-submit').textContent=kind==='resend'?'Send verification link':kind==='invite'?'Join workspace':kind==='verify'?'Verify email':kind==='reset'?'Change password':kind==='register'?'Create account':'Send reset link';
}
$('open-signup').addEventListener('click',()=>showFlow('register'));
$('open-forgot').addEventListener('click',()=>showFlow('forgot'));
$('open-resend').addEventListener('click',()=>showFlow('resend'));
$('back-login').addEventListener('click',()=>{actionToken='';$('error').hidden=true;$('account-flow').hidden=true;$('login').hidden=false;});
$('account-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{
 let path,body;
 if(flow==='invite'){path='/api/invitations/accept';body={token:pendingInvite||actionToken};}
 if(flow==='register'){path='/api/register';body={email:$('flow-email').value,password:$('flow-password').value,workspace:$('flow-workspace').value};}
 if(flow==='resend'){path='/api/verification/resend';body={email:$('flow-email').value};}
 if(flow==='forgot'){path='/api/password/forgot';body={email:$('flow-email').value};}
 if(flow==='verify'){path='/api/verify';body={token:actionToken};}
 if(flow==='reset'){path='/api/password/reset';body={token:actionToken,password:$('flow-password').value};}
 const data=await api(path,body);$('flow-password').value='';$('account-form').hidden=true;$('flow-copy').textContent=data.message;if(flow==='reset'||flow==='verify')actionToken='';if(flow==='invite'){pendingInvite='';actionToken='';$('account-flow').hidden=true;await loadSession();}
 });});
async function initialize(){
 const config=await api('/api/config');domainQuotes=config.domain_quotes===true;testBilling=config.test_billing===true;billingManagement=config.billing_management===true;$('open-signup').hidden=!config.signup;$('open-forgot').hidden=!config.account_mail;$('open-resend').hidden=!config.account_mail;$('registration-note').textContent=config.signup?'Verify your email before signing in.':'Registration is closed.';
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
 const result=await api('/api/uploads?project='+encodeURIComponent(project.id));if(version!==generation)return;
 if(project.kind==='node')return nodeProjectUploads(card,project,role,version,result);
 const publication=await api('/api/publications?project='+encodeURIComponent(project.id));if(version!==generation)return;
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

async function nodeProjectUploads(card,project,role,version,result){
 if(role==='viewer'){
  const copy=document.createElement('p');copy.textContent='Node.js project status is available to owners and developers. Your saved uploads are shown below.';card.append(copy);
  return nodeUploadRows(card,project,role,version,result, null);
 }
 const node=await api('/api/node?project='+encodeURIComponent(project.id));if(version!==generation)return;
 const builds=Array.isArray(node.builds)?node.builds:[];
 const releases=Array.isArray(node.releases)?node.releases:[];
 const deployments=Array.isArray(node.deployments)?node.deployments:[];
 const active=node.active||null;
 const pendingDeployment=deployments.some(item=>item.state==='queued'||item.state==='running');
 const activeRelease=active&&active.release_id;
 const status=document.createElement('p');
 status.textContent=pendingDeployment?'Deployment pending':active?'Live · Revision '+active.revision:node.available?'Ready to deploy':'Hosting setup is pending';
 card.append(status);
 if(active&&node.site){
  const link=document.createElement('a');link.href=node.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent='Open website';card.append(link);
 }
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh release status';refresh.addEventListener('click',()=>loadProjects().catch(error));card.append(refresh);
 for(const build of builds){
  const line=document.createElement('p');line.textContent='Build · '+build.state+' · '+new Date(build.created_at*1000).toLocaleString();card.append(line);
  if(role!=='viewer'&&build.state==='queued'){
   const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel build';cancel.addEventListener('click',async()=>{cancel.disabled=true;try{await api('/api/node/builds/cancel',{project:project.id,id:build.id});if(version===generation)await loadProjects();}catch(e){error(e);cancel.disabled=false;}});card.append(cancel);
  }
 }
 const releaseHeading=document.createElement('p');releaseHeading.textContent='Saved releases';card.append(releaseHeading);
 for(const release of releases){
  const row=document.createElement('div');const label=document.createElement('p');
  const deployment=deployments.find(item=>item.release_id===release.build_id&&item.state==='succeeded');
  label.textContent='Release from '+new Date(release.created_at*1000).toLocaleString()+(deployment?' · '+deployment.state:'');row.append(label);
  if(node.available&&role!=='viewer'){
   const deploy=document.createElement('button');deploy.type='button';deploy.textContent=release.build_id===activeRelease?'Current':deployment?'Restore':'Deploy';deploy.disabled=pendingDeployment||release.build_id===activeRelease;
   const requestKey=crypto.randomUUID();deploy.addEventListener('click',async()=>{deploy.disabled=true;try{await api('/api/node/deployments',{project:project.id,release:release.build_id,key:requestKey});if(version===generation)await loadProjects();}catch(e){error(e);deploy.disabled=false;}});row.append(deploy);
  }
  const releasePending=deployments.some(item=>item.release_id===release.build_id&&(item.state==='queued'||item.state==='running'));
  if(role!=='viewer'&&release.build_id!==activeRelease&&!releasePending){
   const remove=document.createElement('button');remove.type='button';remove.textContent='Delete release';remove.addEventListener('click',async()=>{if(!confirm('Delete this saved release?'))return;remove.disabled=true;try{await api('/api/node/releases/delete',{project:project.id,id:release.build_id});if(version===generation)await loadProjects();}catch(e){error(e);remove.disabled=false;}});row.append(remove);
  }
  card.append(row);
 }
 const historyHeading=document.createElement('p');historyHeading.textContent='Deployment history';card.append(historyHeading);
 for(const deployment of deployments){
  const row=document.createElement('div');const label=document.createElement('p');label.textContent='Revision '+deployment.revision+' · '+deployment.state+(deployment.id===active?.id?' · Current':'');row.append(label);
  if(role!=='viewer'&&deployment.state==='queued'){const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel deployment';cancel.addEventListener('click',async()=>{cancel.disabled=true;try{await api('/api/node/deployments/cancel',{project:project.id,id:deployment.id});if(version===generation)await loadProjects();}catch(e){error(e);cancel.disabled=false;}});row.append(cancel);}
  card.append(row);
 }
 return nodeUploadRows(card,project,role,version,result,{node,builds,releases});
}

function nodeUploadRows(card,project,role,version,result,state){
 const copy=document.createElement('p');copy.textContent='ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Omit node_modules and secrets.';card.append(copy);
 if(role!=='viewer'){
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload and validate';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');const response=await fetch('/api/uploads?project='+encodeURIComponent(project.id),{method:'POST',headers:{'Content-Type':'application/zip','X-CSRF-Token':csrf},body:file});const data=await response.json();if(!response.ok)throw new Error(data.error||'Upload failed');if(version===generation)await loadProjects();});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
  if(state&&state.node.available&&role!=='viewer'){
   const build=state.builds.find(item=>item.upload_id===upload.id);const saved=build&&state.releases.some(release=>release.build_id===build.id);const busy=state.builds.some(item=>item.state==='queued'||item.state==='running');const button=document.createElement('button');button.type='button';button.textContent=saved?'Built':build&&build.state==='queued'?'Build queued':build&&build.state==='running'?'Building':build?'Build again':'Build';button.disabled=busy||!!saved;const requestKey=crypto.randomUUID();button.addEventListener('click',async()=>{button.disabled=true;try{await api('/api/node/builds',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await loadProjects();}catch(e){error(e);button.disabled=false;}});row.append(button);
  }
  if(role!=='viewer'){const button=document.createElement('button');button.type='button';button.textContent='Delete upload';button.addEventListener('click',async()=>{if(!confirm('Delete this saved upload?'))return;button.disabled=true;try{await api('/api/uploads/delete',{project:project.id,id:upload.id});if(version===generation)await loadProjects();}catch(e){error(e);button.disabled=false;}});row.append(button);}card.append(row);
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
 if(status.customer_state==='ready'&&billingManagement){action('Manage subscription and payment details',async()=>{
  const result=await api('/api/billing/manage',{workspace});
  if(version!==generation)return;
  const url=new URL(result.url);if(url.protocol!=='https:'||url.host!=='billing.stripe.com'||url.username||url.password)throw new Error('Invalid billing management link');
  location.assign(url.href);
 });}
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


function resetDomainPanel(){
 clearTimeout(domainExpiryTimer);
 $('domain-result').replaceChildren();
 $('domain-name').value='';
}
function showDomainQuote(quote,version){
 if(version!==generation)return;
 clearTimeout(domainExpiryTimer);
 const content=$('domain-result');content.replaceChildren();
 const offer=quote.offer;
 const note=text=>{const p=document.createElement('p');p.textContent=text;content.append(p);};
 const money=amount=>new Intl.NumberFormat(undefined,{style:'currency',currency:offer.currency.toUpperCase(),minimumFractionDigits:2,maximumFractionDigits:2}).format(amount/100);
 note(offer.domain);
 note('First year: '+money(offer.registration_minor));
 note('Estimated renewal: '+money(offer.renewal_minor)+' per year. Future prices may change.');
 note('Tax will be confirmed before purchase.');
 const expiry=document.createElement('p');expiry.className='notice';content.append(expiry);
 const remaining=Date.parse(offer.expires_at)-Date.now();
 const expired=()=>{if(version===generation)expiry.textContent='This quote has expired. Search again for a current price.';};
 if(quote.expired||remaining<=0){expired();}else{
  expiry.textContent='Quote valid until '+new Date(offer.expires_at).toLocaleTimeString()+'. Availability will be checked again before purchase.';
  domainExpiryTimer=setTimeout(expired,Math.min(remaining,2147483647));
 }
}
$('domain-form').addEventListener('submit',async event=>{
 event.preventDefault();
 const version=generation,workspace=$('workspace').value;
 if(!domainQuotes||workspaces.find(w=>w.id===workspace)?.role!=='owner')return;
 const button=event.currentTarget.querySelector('button');button.disabled=true;
 $('error').hidden=true;clearTimeout(domainExpiryTimer);$('domain-result').textContent='Checking availability and price…';
 try{
  const quote=await api('/api/domains/quote',{workspace,domain:$('domain-name').value});
  if(version===generation)showDomainQuote(quote,version);
 }catch(e){if(version===generation){$('domain-result').replaceChildren();error(e);}}
 finally{button.disabled=false;}
});
