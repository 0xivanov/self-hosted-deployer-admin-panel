'use strict';
const $=id=>document.getElementById(id);
let csrf='',workspaces=[],generation=0,flow='',testBilling=false,billingManagement=false,inviteOnly=false,billingGeneration=0,merchantEnabled=false,merchantCountries=[],merchantGeneration=0,productGeneration=0,merchantOrdersGeneration=0,domainQuotes=false,domainOrderGeneration=0,domainExpiryTimer;
let nodeStatusCards=new Map(),nodeStatusState=null;
let projectDeletionTimer=null,projectDeletionPollVersion=0,projectDeletionPollWorkspace='';
const projectDomainStates=new Map();
const projectRefreshes=new Map();
const domainRefreshes=new WeakMap();
const nodeStatusInterval=5000;
const workspacePanels={projects:'projects-panel',billing:'billing-panel',team:'member-panel',domains:'domain-panel',store:'merchant-panel'};
let currentView=location.pathname.startsWith('/billing/')?'billing':'projects';
let billingTimer=null,billingPollCount=0;
function stopBillingRefresh(){clearTimeout(billingTimer);billingTimer=null;}
function selectWorkspaceView(view){
 if(!workspacePanels[view]||$('nav-'+view).hidden)view='projects';
 currentView=view;
 for(const [name,id] of Object.entries(workspacePanels)){
  $(id).dataset.inactive=String(name!==view);
  $('nav-'+name).setAttribute('aria-current',name===view?'page':'false');
 }
 if(view!=='billing')stopBillingRefresh();
}
function configureWorkspaceNavigation(selected){
 const owner=selected?.role==='owner';
 for(const [view,available] of Object.entries({projects:true,billing:owner&&testBilling,team:owner,domains:owner&&domainQuotes,store:owner&&merchantEnabled}))$('nav-'+view).hidden=!available;
 $('new-project-details').hidden=!selected||selected.role==='viewer';
 selectWorkspaceView(currentView);
}
function refreshCurrentBilling(){
 const workspace=$('workspace').value;
 if(document.hidden||$('workspace-view').hidden||currentView!=='billing'||!testBilling||workspaces.find(w=>w.id===workspace)?.role!=='owner')return;
 loadBilling(workspace,generation).catch(e=>{if(e.status===401)signedOut();else error(e);});
}
for(const view of Object.keys(workspacePanels))$('nav-'+view).addEventListener('click',()=>{selectWorkspaceView(view);if(view==='billing'){billingPollCount=0;refreshCurrentBilling();}});
window.addEventListener('focus',()=>{billingPollCount=0;refreshCurrentBilling();});
document.addEventListener('visibilitychange',()=>{if(document.hidden)stopBillingRefresh();else{billingPollCount=0;refreshCurrentBilling();}});

const fragment=new URLSearchParams(location.hash.slice(1));
let actionToken=fragment.get('verify')||fragment.get('reset')||fragment.get('invite')||'';
const initialFlow=fragment.has('verify')?'verify':fragment.has('reset')?'reset':fragment.has('invite')?'invite':'';
let pendingInvite=initialFlow==='invite'?actionToken:'';
if(location.hash)history.replaceState(null,'',location.pathname+location.search);
async function api(path,body,signal){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined,signal});const data=await response.json();if(!response.ok){const error=new Error(data.error||'Request failed');error.status=response.status;throw error;}return data;}
function error(e){$('error').textContent=e.message;$('error').hidden=false;}
function stopNodeStatusRefresh(){const state=nodeStatusState;nodeStatusCards.clear();if(!state)return;state.stopped=true;clearTimeout(state.timer);state.timer=null;if(state.controller)state.controller.abort();if(nodeStatusState===state)nodeStatusState=null;nodeStatusCards.clear();}
function signedOut(){stopBillingRefresh();currentView='projects';stopNodeStatusRefresh();generation++;$('error').hidden=true;resetDomainPanel();$('domain-panel').hidden=true;billingGeneration++;merchantGeneration++;productGeneration++;merchantOrdersGeneration++;$('billing-content').replaceChildren();$('billing-panel').hidden=true;$('merchant-content').replaceChildren();$('product-content').replaceChildren();$('merchant-orders-content').replaceChildren();$('merchant-panel').hidden=true;csrf='';workspaces=[];$('workspace-view').hidden=true;$('logout').hidden=true;$('login').hidden=false;$('account-flow').hidden=true;$('projects').replaceChildren();}
async function loadProjects(){stopBillingRefresh();billingPollCount=0;stopNodeStatusRefresh();const version=++generation;const workspace=$('workspace').value;const selected=workspaces.find(w=>w.id===workspace);resetDomainPanel();$('domain-panel').hidden=!domainQuotes||!selected||selected.role!=='owner';billingGeneration++;merchantGeneration++;productGeneration++;merchantOrdersGeneration++;$('billing-panel').hidden=!testBilling||!selected||selected.role!=='owner';$('billing-content').replaceChildren();$('merchant-panel').hidden=!merchantEnabled||!selected||selected.role!=='owner';$('merchant-content').replaceChildren();$('product-content').replaceChildren();$('merchant-orders-content').replaceChildren();$('project-form').hidden=!selected||selected.role==='viewer'; $('member-panel').hidden=!selected||selected.role!=='owner';$('members').replaceChildren();$('projects').replaceChildren();configureWorkspaceNavigation(selected);if(!workspace)return;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;for(const project of data.projects){await appendProject(project,selected.role,version);if(version!==generation)return;}if(!data.projects.length){$('projects').textContent='Your first website starts here. Create a project, then upload your files.';$('new-project-details').open=true;}if(selected.role==='owner'){if(domainQuotes)loadDomainOrders(workspace,version);await loadMembers(workspace,version);if(version===generation&&testBilling)await loadBilling(workspace,version);if(version===generation&&merchantEnabled){await loadMerchant(workspace,version);loadMerchantProducts(workspace,version);loadMerchantOrders(workspace,version);}}if(version===generation)startNodeStatusRefresh(version,workspace);}
async function appendProject(project,role,version){if(version!==generation)return;const card=document.createElement('div');card.className='project';card.dataset.projectId=project.id;const name=document.createElement('strong');name.textContent=project.name;const kind=document.createElement('span');kind.textContent=(project.kind==='node'?'Node.js':'Static website');const heading=document.createElement('div');heading.className='project-heading';const icon=document.createElement('span');icon.className='project-icon';icon.textContent=project.kind==='node'?'JS':'</>';heading.append(icon,name,kind);card.append(heading);$('projects').append(card);if(project.deleting){renderDeletingProject(card,project,version);startProjectDeletionPolling(version,$('workspace').value);return;}await projectUploads(card,project,role,version);if(version!==generation||!card.isConnected)return;organizeProject(card);await renderProjectDomains(card,project,role,version);if(version!==generation||!card.isConnected)return;renderProjectDeletion(card,project,role,version);}
async function loadSession(){const data=await api('/api/session');csrf=data.csrf;workspaces=data.workspaces;$('account').textContent=data.account.email;$('workspace').replaceChildren();for(const workspace of workspaces){const option=document.createElement('option');option.value=workspace.id;option.textContent=workspace.name+' · '+workspace.role;$('workspace').append(option);}$('login').hidden=true;$('workspace-view').hidden=false;$('logout').hidden=false;await loadProjects();if(pendingInvite)showFlow('invite');}
async function submit(form,fn){$('error').hidden=true;const button=form.querySelector('button');button.disabled=true;try{await fn();}catch(e){error(e);}finally{button.disabled=false;}}
$('login-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{try{await api('/api/login',{email:$('email').value,password:$('password').value});}finally{$('password').value='';}await loadSession();});});
$('project-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{const version=generation;const workspace=$('workspace').value;await api('/api/projects',{workspace,name:$('project-name').value,kind:$('project-kind').value});if(version!==generation)return;$('project-name').value='';$('new-project-details').open=false;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;const role=workspaces.find(w=>w.id===workspace)?.role;for(const project of data.projects){if(!projectCard(project)){if(!$('projects').querySelector('.project'))$('projects').replaceChildren();await appendProject(project,role,version);}}startNodeStatusRefresh(version,workspace);});});
$('workspace').addEventListener('change',()=>loadProjects().catch(error));
$('logout').addEventListener('click',async()=>{try{await api('/api/logout',{});signedOut();}catch(e){error(e);}});
function showFlow(kind){
 flow=kind;$('error').hidden=true;$('login').hidden=true;$('workspace-view').hidden=true;$('account-flow').hidden=false;$('account-form').hidden=false;
 const titles={register:'Create your account',resend:'Resend verification email',forgot:'Reset your password',reset:'Choose a new password',verify:'Verify your email',invite:'Join a workspace'};
 $('flow-title').textContent=titles[kind];$('flow-copy').textContent=kind==='resend'?'Enter your account email. Check your spam folder and wait at least a minute between requests.':kind==='invite'?'Accept this invitation using the verified account for the invited email address.':kind==='verify'?'Confirm that you want to verify this email address.':kind==='register'?(inviteOnly?'Create your invited account.':'Create a workspace for your websites.'):kind==='reset'?'Use a password of at least 12 characters. Existing sessions will be signed out.':'Enter your account email to request a reset link.';
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
 const config=await api('/api/config');domainQuotes=config.domain_quotes===true;testBilling=config.test_billing===true;billingManagement=config.billing_management===true;inviteOnly=config.invite_only===true;merchantEnabled=config.merchant===true;merchantCountries=Array.isArray(config.merchant_countries)?config.merchant_countries.filter(country=>typeof country==='string'):[];$('open-signup').hidden=!config.signup;$('open-forgot').hidden=!config.account_mail;$('open-resend').hidden=!config.account_mail;$('registration-note').textContent=config.signup?(inviteOnly?'Registration is by invitation.':'Verify your email before signing in.'):'Registration is closed.';
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
 const data=await api('/api/members?workspace='+encodeURIComponent(workspace));if(version!==generation)return;$('members').replaceChildren();
 for(const member of data.members){
  const row=document.createElement('form');row.className='project';const title=document.createElement('strong');title.textContent=member.email;
  const label=document.createElement('label');label.textContent='Role for '+member.email;
  const select=document.createElement('select');for(const [value,text] of [['owner','Owner'],['developer','Developer'],['viewer','Viewer'],['','Remove from workspace']]){const option=document.createElement('option');option.value=value;option.textContent=text;select.append(option);}select.value=member.role;label.append(select);
  const button=document.createElement('button');button.textContent='Apply change';row.append(title,label,button);
  row.addEventListener('submit',event=>{event.preventDefault();if(select.value===member.role)return;if(!confirm('Change access for '+member.email+' to '+(select.value||'removed')+'? Their sessions will be signed out.'))return;
   submit(row,async()=>{await api('/api/members',{workspace,user:member.id,role:select.value});try{await api('/api/session');await loadMembers(workspace,version);}catch(e){if(e.status===401)signedOut();else throw e;}});
  });$('members').append(row);
 }
 await loadInvitations(workspace,version);
}

async function loadInvitations(workspace,version){
 const data=await api('/api/invitations?workspace='+encodeURIComponent(workspace));if(version!==generation)return;$('invitations').replaceChildren();
 for(const invite of data.invitations){const row=document.createElement('div');row.className='project';const label=document.createElement('span');label.textContent=invite.email+' · '+invite.role+' · expires '+new Date(invite.expires_at*1000).toLocaleDateString();const button=document.createElement('button');button.textContent='Revoke invitation';button.addEventListener('click',async()=>{button.disabled=true;try{await api('/api/invitations/revoke',{workspace,id:invite.id});await loadInvitations(workspace,version);}catch(e){error(e);button.disabled=false;}});row.append(label,button);$('invitations').append(row);}
}
$('invite-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{const workspace=$('workspace').value;const version=generation;await api('/api/invitations',{workspace,email:$('invite-email').value,role:$('invite-role').value});if(version!==generation)return;$('invite-email').value='';await loadInvitations(workspace,version);});});

function projectCard(project){return [...$('projects').children].find(card=>card.dataset.projectId===project.id);}
function renderDeletingProject(card,project,version){
 card.dataset.deleting='true';card.classList.add('project-deleting');nodeStatusCards.delete(project.id);for(const child of [...card.children])if(!child.classList.contains('project-heading'))child.remove();
 const status=document.createElement('p');status.className='project-status';status.setAttribute('role','status');status.textContent=project.deletion_error?'Project deletion needs attention: '+project.deletion_error:'Deleting project… Disconnecting the website and removing its uploads and releases.';card.append(status);
 if(project.deletion_error){const note=document.createElement('p');note.className='deletion-error';note.textContent='Cleanup retries automatically. Contact support if this continues.';card.append(note);}
}
function renderProjectDeletion(card,project,role,version){
 if(role!=='owner'||!card.isConnected||version!==generation||project.deleting)return;
 const details=disclosure('Danger zone','project-danger');const intro=document.createElement('p');intro.textContent='Deleting this project is irreversible. The website will go offline, uploads and releases will be deleted, and any custom domain will be disconnected. Domain registrations are unaffected.';details.append(intro);
 const form=document.createElement('form');const label=document.createElement('label');label.textContent='Type '+project.name+' to confirm';const input=document.createElement('input');input.type='text';input.required=true;input.autocomplete='off';input.spellcheck=false;input.setAttribute('aria-label','Type '+project.name+' to confirm');const button=document.createElement('button');button.type='submit';button.className='button-danger';button.textContent='Delete project';button.disabled=true;input.addEventListener('input',()=>{button.disabled=input.value!==project.name;});form.append(label,button);label.append(input);details.append(form);form.addEventListener('submit',event=>{event.preventDefault();if(input.value!==project.name)return;submit(form,async()=>{const accepted=await api('/api/projects/delete',{project:project.id,name:input.value});if(version!==generation)return;project.deleting=true;project.deletion_error=accepted.deletion_error||'';renderDeletingProject(card,project,version);startProjectDeletionPolling(version,$('workspace').value);});});card.append(details);
}
function removeProjectCard(card){const id=card.dataset.projectId;nodeStatusCards.delete(id);projectRefreshes.delete(id);projectDomainStates.delete(id);card.remove();}
function updateProjectsEmptyState(){if($('projects').querySelector('.project'))return;$('projects').textContent='Your first website starts here. Create a project, then upload your files.';$('new-project-details').open=true;}
async function pollProjectDeletion(version,workspace){
 if(version!==generation||workspace!==$('workspace').value){projectDeletionTimer=null;return;}
 try{const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation||workspace!==$('workspace').value)return;const projects=Array.isArray(data.projects)?data.projects:[];const byID=new Map(projects.map(project=>[project.id,project]));
  for(const card of [...$('projects').querySelectorAll('.project[data-deleting="true"]')]){const project=byID.get(card.dataset.projectId);if(!project){removeProjectCard(card);}else if(!project.deleting){removeProjectCard(card);}else{const status=card.querySelector('.project-status');if(status)status.textContent=project.deletion_error?'Project deletion needs attention: '+project.deletion_error:'Deleting project… Disconnecting the website and removing its uploads and releases.';}}
  updateProjectsEmptyState();
  if([...$('projects').querySelectorAll('.project[data-deleting="true"]')].length)projectDeletionTimer=setTimeout(()=>pollProjectDeletion(version,workspace),5000);else projectDeletionTimer=null;
 }catch(e){if(e.status===401)signedOut();else if(version===generation&&workspace===$('workspace').value)projectDeletionTimer=setTimeout(()=>pollProjectDeletion(version,workspace),5000);}
}
function startProjectDeletionPolling(version,workspace){if(version!==generation||workspace!==$('workspace').value)return;if(projectDeletionTimer!==null&&(projectDeletionPollVersion!==version||projectDeletionPollWorkspace!==workspace)){clearTimeout(projectDeletionTimer);projectDeletionTimer=null;}projectDeletionPollVersion=version;projectDeletionPollWorkspace=workspace;if(projectDeletionTimer===null)projectDeletionTimer=setTimeout(()=>pollProjectDeletion(version,workspace),5000);}
function stopProjectDeletionPolling(){clearTimeout(projectDeletionTimer);projectDeletionTimer=null;projectDeletionPollVersion=0;projectDeletionPollWorkspace='';}
function refreshProject(project,role,version){
 const previous=projectRefreshes.get(project.id)||Promise.resolve();
 const pending=previous.catch(()=>{}).then(async()=>{
  const card=projectCard(project);if(version!==generation||!card||!card.isConnected||project.deleting||card.dataset.deleting==='true')return;
  card.setAttribute('aria-busy','true');
  try{
   const staging=document.createElement('div');
   await projectUploads(staging,project,role,version);
   if(version!==generation||!card.isConnected||card.dataset.deleting==='true'||project.deleting)return;
   organizeProject(staging);
   const oldDetails=[...card.querySelectorAll('details')];
   const freshDetails=[...staging.querySelectorAll('details')];
   const used=new Set();
   for(const fresh of freshDetails){const old=oldDetails.find(item=>!used.has(item)&&item.className===fresh.className);if(old){fresh.open=old.open;used.add(old);}}
   // Keep the actual file input, not a copied value: browsers protect FileList.
   const active=document.activeElement;const focused=card.contains(active)?active:null;
   const upload=card.querySelector('.upload-details form');const freshUpload=staging.querySelector('.upload-details form');
   if(upload&&freshUpload)freshUpload.replaceWith(upload);
   const focusText=focused?.textContent;const focusTag=focused?.tagName;
   const heading=card.querySelector('.project-heading');const domains=card.querySelector('.project-domains');const danger=card.querySelector('.project-danger');
   const children=[heading,...staging.childNodes,domains,danger].filter(Boolean);
   // Move retained nodes in place rather than detaching their inputs.
   for(const child of [...card.childNodes])if(!children.includes(child))child.remove();
   for(const child of children)card.append(child);
   if(focused?.isConnected)focused.focus({preventScroll:true});
   else if(focusText){const replacement=[...card.querySelectorAll('button,summary,a')].find(item=>item.tagName===focusTag&&item.textContent===focusText);replacement?.focus({preventScroll:true});}
   startNodeStatusRefresh(version,$('workspace').value);
  }finally{card.removeAttribute('aria-busy');}
 });
 projectRefreshes.set(project.id,pending);
 pending.finally(()=>{if(projectRefreshes.get(project.id)===pending)projectRefreshes.delete(project.id);}).catch(()=>{});
 return pending;
}

function disclosure(title,className){
 const details=document.createElement('details');details.className=className;
 const summary=document.createElement('summary');summary.textContent=title;details.append(summary);return details;
}
function organizeProject(card){
 const children=[...card.children];const uploads=disclosure('Upload a new version','upload-details');const history=disclosure('Uploads & release history','project-history');
 let pastStatus=false;
 for(const child of children){
  if(child.classList.contains('project-heading')||child.classList.contains('node-live'))continue;
  if(child.tagName==='P'&&!pastStatus&&!child.textContent.startsWith('ZIP')){child.classList.add('project-status');pastStatus=true;continue;}
  if(child.tagName==='A'&&child.target==='_blank'){child.className='site-link';continue;}
  if(child.tagName==='BUTTON'&&child.textContent==='Refresh release status'){child.className='refresh-status';child.textContent='Refresh status';continue;}
  if(child.tagName==='FORM'||(child.tagName==='P'&&child.textContent.startsWith('ZIP'))||(child.tagName==='A'&&child.hasAttribute('download'))){uploads.append(child);continue;}
  history.append(child);
 }
 if(uploads.children.length>1)card.append(uploads);
 if(history.children.length>1)card.append(history);
}

function projectDomainStateLabel(domain){
 const labels={pending:'Waiting for DNS',verified:'Setting up HTTPS',active:'Connected · HTTPS',removing:'Disconnecting'};
 return labels[domain.state]||'Status unavailable';
}
function domainText(value){return typeof value==='string'?value:'';}
function domainRecord(label,name,value){
 const row=document.createElement('div');row.className='domain-record';
 const kind=document.createElement('strong');kind.textContent=label;
 const host=document.createElement('span');host.textContent=name;
 const entry=document.createElement('input');entry.type='text';entry.readOnly=true;entry.value=value;entry.setAttribute('aria-label',label+' '+name);entry.title='Select and copy';
 row.append(kind,host,entry);return row;
}
async function renderProjectDomains(card,project,role,version){
 const request=(domainRefreshes.get(card)||0)+1;domainRefreshes.set(card,request);
 const workspace=$('workspace').value;
 const state=projectDomainStates.get(project.id)||{};
 const existing=card.querySelector('.project-domains');
 const details=disclosure('Connect a domain','project-domains');details.open=existing?existing.open:state.open===true;
 const summary=details.querySelector('summary');summary.addEventListener('click',()=>{state.open=!details.open;projectDomainStates.set(project.id,state);});
 const content=document.createElement('div');content.className='domain-content';details.append(content);
 try{
  const data=await api('/api/project-domains?project='+encodeURIComponent(project.id));
  if(version!==generation||workspace!==$('workspace').value||card.dataset.deleting==='true')return;
  const domains=Array.isArray(data.domains)?data.domains:[];const target=domainText(data.target)||'159.195.146.26';
  const intro=document.createElement('p');intro.className='muted';intro.textContent='Connect your root domain or www as a separate connection. Keep DNS records DNS-only, turn off Cloudflare proxying, and do not add an AAAA record. Your domain stays with your current provider. Connect the root and www separately if you want both.';content.append(intro);
  if(role!=='viewer'){
   const form=document.createElement('form');form.className='domain-connect-form';const label=document.createElement('label');label.textContent='Hostname';const input=document.createElement('input');input.type='text';input.maxLength=253;input.required=true;input.autocomplete='off';input.spellcheck=false;input.placeholder='www.example.com';input.value=domainText(state.input);input.addEventListener('input',()=>{state.input=input.value;projectDomainStates.set(project.id,state);});label.append(input);const button=document.createElement('button');button.type='submit';button.textContent='Connect domain';form.append(label,button);form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const hostname=input.value.trim();if(!hostname)throw new Error('Enter a hostname.');await api('/api/project-domains',{project:project.id,hostname});state.input='';input.value='';projectDomainStates.set(project.id,state);if(version===generation&&workspace===$('workspace').value)await renderProjectDomains(card,project,role,version);});});content.append(form);
  }
  if(!domains.length){const empty=document.createElement('p');empty.className='muted';empty.textContent='No custom domains connected yet.';content.append(empty);}
  for(const domain of domains){
   const row=document.createElement('div');row.className='project-domain';
   const heading=document.createElement('div');heading.className='project-domain-heading';const hostname=document.createElement('strong');hostname.textContent=domainText(domain.hostname);const status=document.createElement('span');status.className='domain-state domain-state-'+domainText(domain.state);status.textContent=projectDomainStateLabel(domain);heading.append(hostname,status);row.append(heading);
   if(domain.message){const message=document.createElement('p');message.className='muted';message.textContent=domainText(domain.message);row.append(message);}
   if(domain.state!=='active'&&domain.state!=='removing'){
    const records=document.createElement('div');records.className='domain-records';records.append(domainRecord('A',domainText(domain.hostname),target),domainRecord('TXT',domainText(domain.dns?.name),domainText(domain.dns?.value)));row.append(records);
    const note=document.createElement('p');note.className='muted';note.textContent='Add these records at your DNS provider, then verify. Some providers expect @ for the root domain and only the subdomain part for other record names. HTTPS is set up automatically after verification.';row.append(note);
   }
   const actions=document.createElement('div');actions.className='domain-actions';if(domain.state==='active'){const visit=document.createElement('a');visit.href='https://'+domain.hostname;visit.target='_blank';visit.rel='noopener noreferrer';visit.className='site-link';visit.textContent='Visit website ↗';actions.append(visit);}
   if(role!=='viewer'&&domain.state!=='active'&&domain.state!=='removing'){const verify=document.createElement('button');verify.type='button';verify.textContent='Verify DNS';verify.addEventListener('click',async()=>{verify.disabled=true;try{await api('/api/project-domains/verify',{project:project.id,id:domain.id});if(version===generation&&workspace===$('workspace').value)await renderProjectDomains(card,project,role,version);}catch(e){if(version===generation&&workspace===$('workspace').value){error(e);verify.disabled=false;}}});actions.append(verify);}
   const refresh=document.createElement('button');refresh.type='button';refresh.className='button-quiet';refresh.textContent='Refresh status';refresh.addEventListener('click',()=>renderProjectDomains(card,project,role,version).catch(e=>{if(version===generation&&workspace===$('workspace').value)error(e);}));actions.append(refresh);
   if(role!=='viewer'&&domain.state!=='removing'){const remove=document.createElement('button');remove.type='button';remove.className='button-quiet';remove.textContent='Remove';remove.addEventListener('click',async()=>{if(!confirm('Remove '+domainText(domain.hostname)+' from this project?'))return;remove.disabled=true;try{await api('/api/project-domains/remove',{project:project.id,id:domain.id});if(version===generation&&workspace===$('workspace').value)await renderProjectDomains(card,project,role,version);}catch(e){if(version===generation&&workspace===$('workspace').value){error(e);remove.disabled=false;}}});actions.append(remove);}
   row.append(actions);content.append(row);
  }
 }catch(e){
  if(version!==generation||workspace!==$('workspace').value||card.dataset.deleting==='true')return;
  if(existing){error(e);return;}
  content.replaceChildren();const note=document.createElement('p');note.className='domain-error';note.textContent='Custom domain status is unavailable. Use Refresh status to try again.';content.append(note);
 }
 if(version!==generation||workspace!==$('workspace').value||!card.isConnected||card.dataset.deleting==='true'||domainRefreshes.get(card)!==request)return;
 const previous=card.querySelector('.project-domains');
 if(previous){const active=document.activeElement;const focused=previous.contains(active)?active:null;const text=focused?.textContent;details.open=previous.open;const form=previous.querySelector('form');const fresh=details.querySelector('form');if(form&&fresh)fresh.replaceWith(form);previous.replaceWith(details);if(focused?.isConnected)focused.focus({preventScroll:true});else if(text)[...details.querySelectorAll('button,summary,a')].find(item=>item.tagName===focused.tagName&&item.textContent===text)?.focus({preventScroll:true});}else card.append(details);
}

function uploadDeleteControl(row,upload,project,role,version){
 if(role==='viewer')return;
 const button=document.createElement('button');button.type='button';button.textContent=upload.retention_reason?'Protected upload':'Delete upload';button.disabled=!!upload.retention_reason;
 const note=document.createElement('p');note.className='muted';note.setAttribute('role','status');note.textContent=upload.retention_reason||'';note.hidden=!upload.retention_reason;
 button.addEventListener('click',async()=>{
  if(!confirm('Delete this unused saved upload? This cannot be undone.'))return;
  $('error').hidden=true;button.disabled=true;button.textContent='Deleting…';note.hidden=true;
  try{await api('/api/uploads/delete',{project:project.id,id:upload.id});if(version!==generation)return;await refreshProject(project,role,version);}
  catch(e){if(version!==generation)return;note.textContent=e.message;note.hidden=false;note.setAttribute('role','alert');button.disabled=false;button.textContent='Delete upload';}
 });
 row.append(button,note);
}

async function projectUploads(card,project,role,version){
 const result=await api('/api/uploads?project='+encodeURIComponent(project.id));if(version!==generation)return;
 if(project.kind==='node')return nodeProjectUploads(card,project,role,version,result);
 const publication=await api('/api/publications?project='+encodeURIComponent(project.id));if(version!==generation)return;
 const pending=publication.jobs.some(j=>j.state==='queued'||j.state==='running');const active=publication.jobs.find(j=>j.id===publication.active);
 const status=document.createElement('p');status.textContent=pending?(active?'Publication pending · Current revision '+active.revision:'Publication pending'):active?'Published revision '+active.revision:publication.available?'Ready to publish':'Hosting setup pending. You can upload files now; publishing will be available once setup is complete.';card.append(status);
 if(active&&publication.site){const link=document.createElement('a');link.href=publication.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent='Visit website ↗';card.append(link);}
 const refresh=document.createElement('button');refresh.textContent='Refresh release status';refresh.addEventListener('click',()=>refreshProject(project,role,version).catch(error));card.append(refresh);
 for(const job of publication.jobs){const line=document.createElement('p');line.textContent='Revision '+job.revision+' · '+job.state+(job.id===publication.active?' · Current':'')+(job.state==='running'?' · Applying or awaiting reconciliation':'');card.append(line);
  if(publication.available&&role==='owner'&&job.state==='running'){const resume=document.createElement('button');resume.textContent='Resume revision '+job.revision;resume.addEventListener('click',async()=>{if(!confirm('Resume revision '+job.revision+'? Its saved website files will be published using your permission. A worker that is still active cannot be interrupted.'))return;resume.disabled=true;try{await api('/api/publications/resume',{project:project.id,job:job.id,upload:job.upload_id});if(version===generation)await refreshProject(project,role,version);}catch(e){error(e);resume.disabled=false;}});card.append(resume);}
 }
 const copy=document.createElement('p');copy.textContent=project.kind==='node'?'ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Omit node_modules and secrets.':'ZIP up to 10 MiB. Include index.html at the root. Omit secrets.';card.append(copy);
 if(role!=='viewer'){
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload ZIP';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');const response=await fetch('/api/uploads?project='+encodeURIComponent(project.id),{method:'POST',headers:{'Content-Type':'application/zip','X-CSRF-Token':csrf},body:file});const data=await response.json();if(!response.ok)throw new Error(data.error||'Upload failed');input.value='';if(version===generation)await refreshProject(project,role,version);});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
 if(publication.available&&role!=='viewer'){const publish=document.createElement('button');const previous=publication.jobs.some(j=>j.upload_id===upload.id&&j.state==='succeeded');const isCurrent=active&&active.upload_id===upload.id;publish.textContent=isCurrent?'Current upload':previous?'Restore this upload':'Publish this upload';publish.disabled=pending||isCurrent;const requestKey=crypto.randomUUID();publish.addEventListener('click',async()=>{publish.disabled=true;try{await api('/api/publications',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await refreshProject(project,role,version);}catch(e){error(e);publish.disabled=false;}});row.append(publish);}
  uploadDeleteControl(row,upload,project,role,version);card.append(row);
 }
}

async function nodeProjectUploads(card,project,role,version,result){
 if(role==='viewer'){
  const copy=document.createElement('p');copy.textContent='Node.js project status is available to owners and developers. Your saved uploads are shown below.';card.append(copy);
  return nodeUploadRows(card,project,role,version,result, null);
 }
 const node=await api('/api/node?project='+encodeURIComponent(project.id));if(version!==generation)return;
 const entry={card,project,role,version,node,deployKeys:new Map(),uploadButtons:[],live:document.createElement('div')};entry.live.className='node-live';card.append(entry.live);nodeStatusCards.set(project.id,entry);renderNodeLive(entry,node);
 return nodeUploadRows(card,project,role,version,result,{node,builds:Array.isArray(node.builds)?node.builds:[],releases:Array.isArray(node.releases)?node.releases:[]});
}

function nodeJobActive(job){return job&&(job.state==='queued'||job.state==='running');}
function renderNodeLive(entry,node){
 const {live,project,role,version}=entry;
 if(entry.mutating)return;
 const historyOpen=live.querySelector('details')?.open||false;const snapshot=JSON.stringify(node);if(entry.snapshot===snapshot)return;entry.snapshot=snapshot;
 live.replaceChildren();
 const builds=Array.isArray(node.builds)?node.builds:[];
 const releases=Array.isArray(node.releases)?node.releases:[];
 const deployments=Array.isArray(node.deployments)?node.deployments:[];
 const active=node.active||null;
 const pendingDeployment=deployments.some(nodeJobActive);
 const activeRelease=active&&active.release_id;
 const status=document.createElement('p');
 status.textContent=pendingDeployment?'Deployment pending':builds.some(nodeJobActive)?'Build pending':active?'Live · Revision '+active.revision:node.available?'Ready to deploy':'Hosting setup is pending';
 status.className='project-status';live.append(status);
 if(active&&node.site){
  const link=document.createElement('a');link.href=node.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent='Visit website ↗';link.className='site-link';live.append(link);
 }
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh status';refresh.className='refresh-status';refresh.addEventListener('click',()=>refreshProject(project,role,version).catch(error));live.append(refresh);const history=disclosure('Builds & releases','project-history');history.open=historyOpen;live.append(history);
 for(const build of builds){
  const line=document.createElement('p');line.textContent='Build · '+build.state+' · '+new Date(build.created_at*1000).toLocaleString();history.append(line);
  if(build.state==='failed'){const hint=document.createElement('p');hint.textContent=role==='viewer'?'Build failed.':'Build failed. Review your project or contact the operator, then choose Build again.';history.append(hint);}
  if(role!=='viewer'&&build.state==='queued'){
   const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel build';cancel.addEventListener('click',async()=>{cancel.disabled=true;entry.mutating=true;try{await api('/api/node/builds/cancel',{project:project.id,id:build.id});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);cancel.disabled=false;}finally{entry.mutating=false;}});history.append(cancel);
  }
 }
 const releaseHeading=document.createElement('p');releaseHeading.textContent='Saved releases';history.append(releaseHeading);
 for(const release of releases){
  const row=document.createElement('div');const label=document.createElement('p');
  const deployment=deployments.find(item=>item.release_id===release.build_id&&item.state==='succeeded');
  label.textContent='Release from '+new Date(release.created_at*1000).toLocaleString()+(deployment?' · '+deployment.state:'');row.append(label);
  if(node.available&&role!=='viewer'){
   const deploy=document.createElement('button');deploy.type='button';deploy.textContent=release.build_id===activeRelease?'Current':deployment?'Restore':'Deploy';deploy.disabled=pendingDeployment||release.build_id===activeRelease;
   const requestKey=entry.deployKeys.get(release.build_id)||crypto.randomUUID();entry.deployKeys.set(release.build_id,requestKey);deploy.addEventListener('click',async()=>{deploy.disabled=true;entry.mutating=true;try{await api('/api/node/deployments',{project:project.id,release:release.build_id,key:requestKey});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);deploy.disabled=false;}finally{entry.mutating=false;}});row.append(deploy);
  }
  const releasePending=deployments.some(item=>item.release_id===release.build_id&&(item.state==='queued'||item.state==='running'));
  if(role!=='viewer'&&release.build_id!==activeRelease&&!releasePending){
   const remove=document.createElement('button');remove.type='button';remove.textContent='Delete release';remove.addEventListener('click',async()=>{if(!confirm('Delete this saved release?'))return;remove.disabled=true;entry.mutating=true;try{await api('/api/node/releases/delete',{project:project.id,id:release.build_id});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);remove.disabled=false;}finally{entry.mutating=false;}});row.append(remove);
  }
  history.append(row);
 }
 const historyHeading=document.createElement('p');historyHeading.textContent='Deployment history';history.append(historyHeading);
 for(const deployment of deployments){
  const row=document.createElement('div');const label=document.createElement('p');label.textContent='Revision '+deployment.revision+' · '+deployment.state+(deployment.id===active?.id?' · Current':'');row.append(label);
  if(role!=='viewer'&&deployment.state==='queued'){const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel deployment';cancel.addEventListener('click',async()=>{cancel.disabled=true;entry.mutating=true;try{await api('/api/node/deployments/cancel',{project:project.id,id:deployment.id});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);cancel.disabled=false;}finally{entry.mutating=false;}});row.append(cancel);}
  history.append(row);
 }
 entry.node=node;entry.active=builds.some(nodeJobActive)||deployments.some(nodeJobActive);
 for(const {button,upload} of entry.uploadButtons||[]){
  const build=builds.find(item=>item.upload_id===upload.id);
  const saved=build&&releases.some(release=>release.build_id===build.id);
  button.textContent=saved?'Built':build?.state==='queued'?'Build queued':build?.state==='running'?'Building':build?'Build again':'Build';
  button.disabled=builds.some(nodeJobActive)||!!saved||!node.available;
 }
}
async function pollNodeStatus(state){
 if(state.stopped||nodeStatusState!==state||state.version!==generation||state.workspace!==$('workspace').value||state.inFlight)return;
 const entries=[...nodeStatusCards.values()].filter(entry=>entry.version===state.version&&entry.active&&!entry.mutating);if(!entries.length){state.timer=setTimeout(()=>pollNodeStatus(state),nodeStatusInterval);return;}
 state.inFlight=true;state.controller=new AbortController();
 const results=await Promise.allSettled(entries.map(entry=>api('/api/node?project='+encodeURIComponent(entry.project.id),undefined,state.controller.signal)));
 if(nodeStatusState!==state||state.stopped||state.version!==generation||state.workspace!==$('workspace').value){state.inFlight=false;return;}
 let unauthorized=false;
   results.forEach((result,index)=>{if(!entries[index].card.isConnected||entries[index].card.dataset.deleting==='true'){nodeStatusCards.delete(entries[index].project.id);return;}if(result.status==='fulfilled')renderNodeLive(entries[index],result.value);else if(result.reason?.status===401)unauthorized=true;else{const note=entries[index].live.firstElementChild;if(note)note.textContent='Status refresh unavailable. Retry with Refresh release status.';entries[index].snapshot=null;if(result.reason?.status===403||result.reason?.status===404)entries[index].active=false;}});
 state.inFlight=false;state.controller=null;
 if(!unauthorized&&[...nodeStatusCards.values()].some(entry=>entry.version===state.version&&entry.active))state.timer=setTimeout(()=>pollNodeStatus(state),nodeStatusInterval);else if(unauthorized)signedOut();
}
function startNodeStatusRefresh(version,workspace){
 if(version!==generation||workspace!==$('workspace').value)return;
 if([...nodeStatusCards.values()].some(entry=>entry.version===version&&entry.active)){if(nodeStatusState){nodeStatusState.stopped=true;clearTimeout(nodeStatusState.timer);nodeStatusState.controller?.abort();}const state={version,workspace,timer:null,inFlight:false,stopped:false,controller:null};nodeStatusState=state;state.timer=setTimeout(()=>pollNodeStatus(state),nodeStatusInterval);}
}

function nodeUploadRows(card,project,role,version,result,state){
 const copy=document.createElement('p');copy.textContent='ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Customize index.html in the starter, then ZIP the source files and upload, build, and deploy. Omit node_modules and secrets.';card.append(copy);
 if(role!=='viewer'){
  const starter=document.createElement('a');starter.href='/examples/node-website.zip';starter.download='node-website.zip';starter.textContent='Download a starter website';card.append(starter);
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload ZIP';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');const response=await fetch('/api/uploads?project='+encodeURIComponent(project.id),{method:'POST',headers:{'Content-Type':'application/zip','X-CSRF-Token':csrf},body:file});const data=await response.json();if(!response.ok)throw new Error(data.error||'Upload failed');input.value='';if(version===generation)await refreshProject(project,role,version);});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
  if(state&&role!=='viewer'){
   const build=state.builds.find(item=>item.upload_id===upload.id);const saved=build&&state.releases.some(release=>release.build_id===build.id);const busy=state.builds.some(item=>item.state==='queued'||item.state==='running');const button=document.createElement('button');button.type='button';button.textContent=saved?'Built':build&&build.state==='queued'?'Build queued':build&&build.state==='running'?'Building':build?'Build again':'Build';button.disabled=busy||!!saved||!state.node.available;nodeStatusCards.get(project.id).uploadButtons.push({button,upload});const requestKey=crypto.randomUUID();button.addEventListener('click',async()=>{button.disabled=true;const entry=nodeStatusCards.get(project.id);entry.mutating=true;try{await api('/api/node/builds',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);button.disabled=false;}finally{entry.mutating=false;}});row.append(button);
  }
  uploadDeleteControl(row,upload,project,role,version);card.append(row);
 }
}

function billingCurrencyDigits(currency){
 const code=String(currency||'').toLowerCase();
 const zero=new Set(['bif','clp','djf','gnf','jpy','kmf','krw','mga','pyg','rwf','vnd','vuv','xaf','xof','xpf']);
 const three=new Set(['bhd','jod','kwd','omr','tnd']);
 return zero.has(code)?0:three.has(code)?3:2;
}
function billingAmount(amountMinor,currency){
 const digits=billingCurrencyDigits(currency);const code=String(currency||'').toUpperCase();
 if(!code||typeof amountMinor!=='number'||!Number.isFinite(amountMinor))return '';
 try{return new Intl.NumberFormat(undefined,{style:'currency',currency:code,minimumFractionDigits:digits,maximumFractionDigits:digits}).format(amountMinor/10**digits);}catch(e){return String(amountMinor/10**digits)+' '+code;}
}
function billingPrice(price){
 const amount=billingAmount(price.amount_minor,price.currency);
 return amount+' every '+price.interval_count+' '+price.interval+(price.interval_count===1?'':'s');
}
function billingStatusLabel(state){
 const labels={pending:'Awaiting billing update',active:'Active',trialing:'Trial',past_due:'Past due',unpaid:'Unpaid',canceled:'Canceled',incomplete:'Incomplete',incomplete_expired:'Incomplete and expired',paused:'Paused'};
 return labels[state]||state||'Unknown';
}
function billingDate(unix){
 if(typeof unix!=='number'||!Number.isFinite(unix)||unix<=0)return '';
 const date=new Date(unix*1000);return Number.isNaN(date.getTime())?'':date.toLocaleDateString();
}
function billingDateTime(unix){
 if(typeof unix!=='number'||!Number.isFinite(unix)||unix<=0)return '';
 const date=new Date(unix*1000);return Number.isNaN(date.getTime())?'':date.toLocaleString();
}
function billingInvoiceLabel(status){
 if(!status)return '';
 return status.replaceAll('_',' ').replace(/\b\w/g,letter=>letter.toUpperCase());
}
function renderPaymentHistory(payments){
 const list=document.createElement('div');
 for(const payment of payments){
  const row=document.createElement('div');row.className='project';
  const title=document.createElement('strong');title.textContent=payment.plan||'Hosting plan';row.append(title);
  const note=text=>{const p=document.createElement('p');p.textContent=text;row.append(p);};
  if(payment.pending){note('Payment details are awaiting an update.');list.append(row);continue;}
  note('Paid '+(billingAmount(payment.amount_captured,payment.currency)||'amount unavailable'));
  note('Refunded '+(billingAmount(payment.amount_refunded,payment.currency)||'amount unavailable'));
  const disputes=Array.isArray(payment.dispute_statuses)?payment.dispute_statuses.filter(Boolean):[];
  if(disputes.length)note('Dispute history: '+disputes.map(billingInvoiceLabel).join(', '));
  else note(payment.disputed?'Disputed':payment.disputes_checked?'No disputes reported':'Dispute status awaiting an update');
  const observed=billingDateTime(payment.observed_at);if(observed)note('Last checked '+observed);
  if(payment.stale)note('Payment details may be out of date.');
  list.append(row);
 }
 return list;
}
async function loadPaymentHistory(workspace,version,request,target){
 const section=document.createElement('section');section.className='billing-history';
 const heading=document.createElement('h3');heading.textContent='Payment history';section.append(heading);
 const body=document.createElement('div');section.append(body);
 const message=document.createElement('p');message.setAttribute('aria-live','polite');section.append(message);
 const button=document.createElement('button');button.type='button';button.textContent='Load more';button.hidden=true;section.append(button);target.append(section);
 const state={cursor:'',loading:false,done:false,ids:new Set()};
 const current=()=>version===generation&&request===billingGeneration&&workspace===$('workspace').value;
 const loadPage=async()=>{
  if(state.loading||state.done||!current())return;
  state.loading=true;button.disabled=true;message.textContent='Loading payment history…';
  try{
   const query='/api/billing/payments?workspace='+encodeURIComponent(workspace)+(state.cursor?'&before='+encodeURIComponent(state.cursor):'');
   const data=await api(query);if(!current())return;
   const fresh=[];
   for(const payment of Array.isArray(data.payments)?data.payments:[]){if(payment&&payment.id&&!state.ids.has(payment.id)){state.ids.add(payment.id);fresh.push(payment);}}
   if(fresh.length)body.append(renderPaymentHistory(fresh));
   const next=typeof data.next_cursor==='string'?data.next_cursor:'';
   state.done=!next||next===state.cursor;state.cursor=next;
   button.hidden=state.done;button.textContent='Load more';
   message.textContent=state.ids.size?'':'No reconciled payments yet.';
  }catch(e){
   if(current()){message.textContent='Payment history is unavailable. Please retry.';button.textContent='Retry payment history';button.hidden=false;}
  }finally{state.loading=false;button.disabled=false;}
 };
 button.addEventListener('click',loadPage);
 await loadPage();
}
function merchantDate(unix){return billingDateTime(unix);}
function parseMerchantPrice(raw){const value=String(raw||'').trim();if(!/^(?:0|[1-9]\d{0,5})(?:\.\d{1,2})?$/.test(value))return null;const parts=value.split('.');const minor=Number(parts[0])*100+Number((parts[1]||'').padEnd(2,'0'));return Number.isSafeInteger(minor)&&minor>=50&&minor<=99999999?minor:null;}
function merchantProductError(message){const p=document.createElement('p');p.textContent=message;p.className='muted';return p;}
function renderMerchantProducts(workspace,version,request,products){
 const content=$('product-content');content.replaceChildren();
 const current=()=>version===generation&&request===productGeneration&&workspace===$('workspace').value;
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh catalog';refresh.addEventListener('click',()=>{if(current()&&confirm('Reload the catalog? Unsaved edits will be discarded.'))loadMerchantProducts(workspace,version);});content.append(refresh);
 const label=(text,control)=>{const el=document.createElement('label');el.textContent=text;el.append(control);return el;};
 function productForm(product){
  const creating=!product;let intent=null,busy=false;
  const form=document.createElement('form');form.className='project';
  const heading=document.createElement('strong');heading.textContent=creating?'New product':'Edit product';form.append(heading);
  const name=document.createElement('input');name.required=true;name.maxLength=120;name.value=product?.name||'';
  const currency=document.createElement('select');for(const code of ['eur','usd','gbp']){const option=document.createElement('option');option.value=code;option.textContent=code.toUpperCase();currency.append(option);}currency.value=product?.currency||'eur';
  const price=document.createElement('input');price.required=true;price.inputMode='decimal';price.value=product?(product.amount_minor/100).toFixed(2):'';price.placeholder='12.50';
  const active=document.createElement('input');active.type='checkbox';active.checked=product?product.active===true:true;
  const button=document.createElement('button');button.type='submit';button.textContent=creating?'Create product':'Save changes';
  const message=document.createElement('p');message.setAttribute('role','status');
  form.append(label('Product name',name),label('Currency',currency),label('Price',price),label('Active in catalog',active),button,message);
  const purchase=document.createElement('a');purchase.textContent='Open test purchase page';purchase.target='_blank';purchase.rel='noopener noreferrer';
  const updatePurchase=()=>{purchase.hidden=!product?.active;if(product)purchase.href='/shop?product='+encodeURIComponent(product.id);};updatePurchase();if(!creating)form.append(purchase);
  form.addEventListener('submit',async event=>{
   event.preventDefault();if(busy||!current())return;message.textContent='';
   const clean=name.value.trim(),amount=parseMerchantPrice(price.value);
   if(!clean||new TextEncoder().encode(clean).length>120||[...clean].some(ch=>/\p{Cc}/u.test(ch))||amount===null){message.textContent='Enter a name and a price from 0.50 to 999999.99.';return;}
   const payload={workspace,name:clean,currency:currency.value,amount_minor:amount,active:active.checked};
   if(creating){const fingerprint=JSON.stringify(payload);if(intent&&intent.fingerprint!==fingerprint){message.textContent='Refresh the catalog to check the earlier request before changing it.';return;}if(!intent)intent={fingerprint,key:crypto.randomUUID()};payload.key=intent.key;}
   else{payload.id=product.id;payload.revision=product.revision;}
   busy=true;button.disabled=true;for(const control of [name,currency,price,active])control.disabled=true;
   try{
    const saved=await api('/api/merchant/products',payload);if(!current())return;
    if(creating){content.append(productForm(saved));name.value='';price.value='';intent=null;}
    else{Object.assign(product,saved);updatePurchase();}
    message.textContent='Product saved.';
   }catch(e){if(current()){message.textContent=e.status===409?'Product changed, request conflicts, or catalog is full. Refresh before retrying.':'Unable to save. Retry unchanged values or refresh the catalog to check the result.';if(creating&&e.status===400)intent=null;}}
   finally{busy=false;button.disabled=false;for(const control of [name,currency,price,active])control.disabled=false;}
  });
  return form;
 }
 content.append(productForm(null));
 for(const product of Array.isArray(products)?products:[])content.append(productForm(product));
}
async function loadMerchantProducts(workspace,version){
 if(version!==generation||workspace!==$('workspace').value)return;
 const request=++productGeneration;
 try{const data=await api('/api/merchant/products?workspace='+encodeURIComponent(workspace));if(version===generation&&request===productGeneration&&workspace===$('workspace').value)renderMerchantProducts(workspace,version,request,data.products);}
 catch(e){if(version===generation&&request===productGeneration&&workspace===$('workspace').value)$('product-content').replaceChildren(merchantProductError('Product catalog is unavailable. Refresh the workspace to retry.'));}
}
function renderMerchantOrders(orders,refunds=[],enabled=false){
 const content=$('merchant-orders-content');content.replaceChildren();const list=Array.isArray(orders)?orders:[];
 const workspace=$('workspace').value,version=generation,request=merchantOrdersGeneration;
 const current=()=>version===generation&&request===merchantOrdersGeneration&&workspace===$('workspace').value;
 if(!list.length){const empty=document.createElement('p');empty.textContent='No merchant orders yet.';content.append(empty);return;}
 for(const order of list){
  const row=document.createElement('div');row.className='project';
  const note=text=>{const p=document.createElement('p');p.textContent=text;row.append(p);};
  const title=document.createElement('strong');title.textContent=(order.name||'Product')+' · '+(billingAmount(order.amount_minor,order.currency)||'Amount unavailable');row.append(title);
  note('Order: '+(order.state||'Unknown')+' · Payment: '+(order.payment_status||'Unknown'));
  const created=billingDateTime(order.created_at);if(created)note('Created '+created);
  const observed=billingDateTime(order.observed_at);note(observed?'Last checked '+observed:'Payment not checked yet');
  const refund=(Array.isArray(refunds)?refunds:[]).find(r=>r.order_id===order.id);
  if(refund){note('Full refund: '+refund.state);if(refund.observed_at)note('Refund last checked '+billingDateTime(refund.observed_at));if(refund.state==='submitted')note('Refund outcome is unknown. Contact the operator for reconciliation.');}
  const fulfilledAt=Number(order.fulfilled_at)||0;
  note(fulfilledAt?'Marked fulfilled '+billingDateTime(fulfilledAt):'Not marked fulfilled');
  if(enabled && (refund||order.payment_status==='paid')){
   const button=document.createElement('button');button.type='button';button.textContent=refund?'Refresh refund':'Refund full test payment';let busy=false;
   button.addEventListener('click',async()=>{
    if(busy||!current())return;
    if(!refund&&!confirm('Refund the full '+billingAmount(order.amount_minor,order.currency)+' test payment for '+order.name+'?'))return;
    busy=true;button.disabled=true;
    try{await api(refund?'/api/merchant/refunds/refresh':'/api/merchant/refunds',refund?{workspace,id:refund.id}:{workspace,order:order.id});}
    catch(e){if(current())error(e);}
    finally{if(current())await loadMerchantOrders(workspace,version);busy=false;}
   });row.append(button);
  }
  const activeRefund=refund&&['requested','submitted','pending','requires_action','succeeded'].includes(refund.state);
  if(!fulfilledAt&&order.state==='complete'&&order.payment_status==='paid'&&!activeRefund){
   const button=document.createElement('button');button.type='button';button.textContent='Mark fulfilled';let busy=false;
   button.addEventListener('click',async()=>{
    if(busy||!current())return;
    if(!confirm('Confirm that you have delivered or completed this service for the customer?'))return;
    busy=true;button.disabled=true;
    try{await api('/api/merchant/orders/fulfill',{workspace,order:order.id});}
    catch(e){if(current())error(e);}
    finally{if(current())await loadMerchantOrders(workspace,version);busy=false;}
   });row.append(button);
  }
  content.append(row);
 }
}
async function loadMerchantOrders(workspace,version){if(version!==generation||workspace!==$('workspace').value)return;const request=++merchantOrdersGeneration;const button=$('merchant-orders-refresh');if(button)button.disabled=true;try{const [data,refundData]=await Promise.all([api('/api/merchant/orders?workspace='+encodeURIComponent(workspace)),api('/api/merchant/refunds?workspace='+encodeURIComponent(workspace))]);if(version===generation&&request===merchantOrdersGeneration&&workspace===$('workspace').value)renderMerchantOrders(data.orders,refundData.refunds,refundData.enabled===true);}catch(e){if(version===generation&&request===merchantOrdersGeneration&&workspace===$('workspace').value){const content=$('merchant-orders-content');content.replaceChildren();const note=document.createElement('p');note.textContent='Order history is unavailable.';content.append(note);}}finally{if(button&&version===generation&&request===merchantOrdersGeneration&&workspace===$('workspace').value)button.disabled=false;}}
$('merchant-orders-refresh').addEventListener('click',()=>{const workspace=$('workspace').value;if(merchantEnabled&&workspaces.find(w=>w.id===workspace)?.role==='owner')loadMerchantOrders(workspace,generation);});
function merchantAccountLabel(state){return {not_started:'Not started',requested:'Setup in progress',submitted:'Needs reconciliation',bound:'Account created'}[state]||'Unknown';}
function merchantRequestValid(version,request,workspace){return version===generation&&request===merchantGeneration&&workspace===$('workspace').value;}
function renderMerchantAccount(workspace,version,request,account){
 const content=$('merchant-content');content.replaceChildren();let busy=false;
 const note=text=>{const p=document.createElement('p');p.textContent=text;content.append(p);};
 const current=()=>merchantRequestValid(version,request,workspace);
 if(!account||!account.state){note('Merchant account status is unavailable.');return;}
 note('Status: '+merchantAccountLabel(account.state));
 if(account.country)note('Business country: '+account.country);
 if(account.state==='bound'){
  for(const [label,value] of [['Details submitted',account.details_submitted],['Charges enabled',account.charges_enabled],['Payouts enabled',account.payouts_enabled],['Card payments',account.card_payments]])note(label+': '+(typeof value==='boolean'?(value?'Yes':'No'):String(value||'Not available')));
  const observed=merchantDate(account.observed_at);if(observed)note('Last checked '+observed);
  if(account.stale)note('Merchant account status may be out of date. Refresh it to check.');
 }
 const action=(button,fn)=>async()=>{
  if(busy||!current())return;busy=true;button.disabled=true;
  try{await fn();}catch(e){if(current()){error(e);await loadMerchant(workspace,version);}}
  finally{busy=false;button.disabled=false;}
 };
 const post=async(path,body)=>{const result=await api(path,body);return current()?result:null;};
 const create=async country=>{const result=await post('/api/merchant/account',{workspace,country});if(result)renderMerchantAccount(workspace,version,request,result);};
 if(account.state==='not_started'){
  if(!merchantCountries.length){note('No merchant countries are configured.');return;}
  const form=document.createElement('form'),label=document.createElement('label'),select=document.createElement('select');label.textContent='Business country';
  for(const country of merchantCountries){const option=document.createElement('option');option.value=country;option.textContent=country;select.append(option);}label.append(select);
  const button=document.createElement('button');button.textContent='Create test merchant account';form.append(label,button);content.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();action(button,()=>create(select.value))();});
 }else if(account.state==='requested'){
  const button=document.createElement('button');button.type='button';button.textContent='Continue account creation';button.addEventListener('click',action(button,()=>create(account.country)));content.append(button);
 }else if(account.state==='submitted')note('Account setup needs reconciliation. Contact the operator.');
 else if(account.state==='bound'){
  const button=document.createElement('button');button.type='button';button.textContent='Continue Stripe setup';button.addEventListener('click',action(button,async()=>{
   const result=await post('/api/merchant/onboarding',{workspace});if(!result)return;
   const url=new URL(result.url);if(url.protocol!=='https:'||url.host!=='connect.stripe.com'||url.username||url.password||url.hash)throw new Error('Invalid merchant onboarding link');
   if(typeof result.expires_at!=='number'||result.expires_at<=Math.floor(Date.now()/1000))throw new Error('Expired merchant onboarding link');
   if(current())location.assign(url.href);
  }));content.append(button);
 }
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh account status';refresh.addEventListener('click',action(refresh,async()=>{
  if(account.state==='bound'){const result=await post('/api/merchant/refresh',{workspace});if(result)renderMerchantAccount(workspace,version,request,result);}
  else await loadMerchant(workspace,version);
 }));content.append(refresh);
}
async function loadMerchant(workspace,version){const request=++merchantGeneration;try{const account=await api('/api/merchant/account?workspace='+encodeURIComponent(workspace));if(merchantRequestValid(version,request,workspace))renderMerchantAccount(workspace,version,request,account);}catch(e){if(merchantRequestValid(version,request,workspace)){const content=$('merchant-content');content.replaceChildren();const p=document.createElement('p');p.textContent='Merchant account status is unavailable.';content.append(p);error(e);}}}
function showBillingSubscriptions(content,subscriptions){
 for(const subscription of Array.isArray(subscriptions)?subscriptions:[]){
  const row=document.createElement('div');row.className='subscription-card';
  const title=document.createElement('strong');title.textContent=(subscription.plan==='starter'?'Starter':subscription.plan||'Hosting plan')+' · '+billingStatusLabel(subscription.state);row.append(title);
  const periodEnd=billingDate(subscription.period_end);if(periodEnd){const period=document.createElement('p');period.textContent='Billing period ends '+periodEnd;row.append(period);}
  if(subscription.cancel_at_period_end){const cancellation=document.createElement('p');cancellation.textContent='Cancellation scheduled for end of billing period'+(periodEnd?' ('+periodEnd+')':'');row.append(cancellation);}
  if(subscription.collection_paused){const paused=document.createElement('p');paused.textContent='Collection paused';row.append(paused);}
  const invoice=billingInvoiceLabel(subscription.invoice_status);if(invoice){const invoiceLine=document.createElement('p');invoiceLine.textContent='Invoice status: '+invoice;row.append(invoiceLine);}
  const observed=billingDateTime(subscription.observed_at);if(observed){const checked=document.createElement('p');checked.textContent='Last checked '+observed;row.append(checked);}
  if(subscription.stale===true&&observed){const stale=document.createElement('p');stale.textContent='Billing status may be out of date. Awaiting a background update.';row.append(stale);}
  content.append(row);
 }
}
function renderHostingUsage(content,access){
 const limits=access&&access.limits&&typeof access.limits==='object'?access.limits:{};
 const usage=access&&access.usage&&typeof access.usage==='object'?access.usage:{};
 const count=value=>typeof value==='number'&&Number.isFinite(value)&&value>=0?Math.floor(value):null;
 const bytes=value=>typeof value==='number'&&Number.isFinite(value)&&value>=0?value:null;
 const display=(used,limit)=>String(used===null?'Unavailable':used)+(limit===null?' used':' / '+limit);
 const note=text=>{const p=document.createElement('p');p.textContent=text;content.append(p);};
 if(typeof access?.plan==='string'&&access.plan)note('Plan: '+access.plan);
 if(access?.limits)note('Allowances saved with your checkout.');
 const projectUsage=count(usage.projects),projectLimit=count(limits.projects);
 const uploadUsage=count(usage.uploads),uploadLimit=count(limits.uploads);
 const storageUsage=bytes(usage.upload_bytes),storageLimit=bytes(limits.upload_bytes);
 const mib=value=>value===null?'Unavailable':(value/1048576).toFixed(2);
 note('Projects: '+display(projectUsage,projectLimit));
 note('Saved upload ZIPs: '+display(uploadUsage,uploadLimit));
 note('Source ZIP storage: '+mib(storageUsage)+(storageLimit===null?'':' / '+mib(storageLimit))+' MiB');
 if(typeof limits.node==='boolean')note('Node.js: '+(limits.node?'included':'not included'));
 note('Usage includes all saved upload versions.');
 if(!Object.keys(limits).length)note(access?.allowed===false?'Plan limits will appear after billing verification.':'Platform defaults apply.');
}
async function loadBilling(workspace,version){
 stopBillingRefresh();
 const request=++billingGeneration;
 const [catalog,status,access]=await Promise.all([api('/api/billing/offers?workspace='+encodeURIComponent(workspace)),api('/api/billing/status?workspace='+encodeURIComponent(workspace)),api('/api/billing/access?workspace='+encodeURIComponent(workspace))]);
 if(version!==generation||request!==billingGeneration)return;
 const content=$('billing-content');content.replaceChildren();
 const subscriptions=Array.isArray(status.subscriptions)?status.subscriptions:[];
 const waiting=status.customer_state==='pending'||(status.checkout&&status.checkout.state!=='completed')||(status.checkout?.state==='completed'&&!subscriptions.some(sub=>sub.checkout_id===status.checkout.id&&sub.state!=='pending'));
 const returnMessage=$('billing-return-message');
 returnMessage.hidden=!location.pathname.startsWith('/billing/');
 if(!returnMessage.hidden){
  const active=subscriptions.some(sub=>sub.state==='active'&&sub.invoice_status==='paid'&&!sub.stale);
  returnMessage.textContent=active?'Your test subscription is active. No real money was charged.':location.pathname==='/billing/cancel'?'Checkout was canceled. You can continue when you are ready.':waiting?(billingPollCount<24?'Checking your subscription with Stripe. This page updates automatically.':'Stripe is taking longer to confirm this checkout. Use Refresh status to check again.'):'Your latest subscription status is shown below.';
 }
 if(waiting&&currentView==='billing'&&!document.hidden&&billingPollCount++<24){billingTimer=setTimeout(()=>{if(version===generation&&request===billingGeneration&&workspace===$('workspace').value)refreshCurrentBilling();},5000);}
 showBillingSubscriptions(content,subscriptions);
 const history=document.createElement('details');history.className='billing-details';const historyTitle=document.createElement('summary');historyTitle.textContent='Payment history';history.append(historyTitle);content.append(history);loadPaymentHistory(workspace,version,request,history);
 const note=text=>{const p=document.createElement('p');p.textContent=text;content.append(p);};
 const action=(text,fn)=>{const button=document.createElement('button');button.textContent=text;button.addEventListener('click',async()=>{button.disabled=true;try{await fn();if(version===generation)await loadBilling(workspace,version);}catch(e){if(version===generation)error(e);}finally{button.disabled=false;}});content.append(button);};
 if(access.mode==='test_subscription')note(access.allowed?'Hosting changes are enabled by your verified test subscription.':'Hosting changes are on hold. A current paid test subscription and payment check are required. Existing sites keep running.');
 const usage=document.createElement('details');usage.className='billing-details';const usageTitle=document.createElement('summary');usageTitle.textContent='Storage and usage';usage.append(usageTitle);renderHostingUsage(usage,access);content.append(usage);
 if(status.customer_state==='ready'&&billingManagement){action(subscriptions.length?'Manage subscription':'Manage payment methods',async()=>{
  const result=await api('/api/billing/manage',{workspace});
  if(version!==generation)return;
  const url=new URL(result.url);if(url.protocol!=='https:'||url.host!=='billing.stripe.com'||url.username||url.password)throw new Error('Invalid billing management link');
  location.assign(url.href);
 });}
 if(status.checkout){
  const checkout=status.checkout;
  const matchedSubscription=subscriptions.some(subscription=>subscription.checkout_id===checkout.id);
  note(checkout.state==='completed'?(matchedSubscription?'Checkout completed. Subscription status is shown above.':'Checkout completed. Billing status is awaiting reconciliation.') :checkout.state==='open'?'Your checkout is ready. Complete it on Stripe to activate your test subscription.':'Preparing checkout. This page updates automatically.');
  if(checkout.state!=='completed')note('Selected plan: '+checkout.plan);
  if(checkout.limits)note('Saved checkout allowances: '+checkout.limits.projects+' projects, '+checkout.limits.uploads+' saved source ZIPs, '+(checkout.limits.upload_bytes/1048576).toFixed(2)+' MiB of source ZIP storage. Node.js '+(checkout.limits.node?'included.':'not included.'));

  if(checkout.state==='open'&&checkout.url){const url=new URL(checkout.url);if(url.protocol==='https:'&&url.host==='checkout.stripe.com'&&!url.username&&!url.password){const link=document.createElement('a');link.href=url.href;link.target='_blank';link.rel='noopener noreferrer';link.className='primary-link';link.textContent='Continue to test checkout';content.append(link);}}
  return;
 }
 if(status.customer_state==='not_started'){note('Set up billing for this workspace to choose a hosting plan.');action('Set up test billing',()=>api('/api/billing/customer',{workspace}));return;}
 if(status.customer_state==='pending'){note('Setting up your billing account. This page updates automatically.');return;}
 note('Base prices are shown below. Confirm taxes and the final total on Stripe before completing checkout.');
 if(!catalog.offers.length)note('No hosting plans are currently available.');
 for(const offer of catalog.offers){
  note(offer.plan+' · '+(offer.price?billingPrice(offer.price):'Price temporarily unavailable'));
  if(offer.limits)note('Current plan limits: '+offer.limits.projects+' projects, '+offer.limits.uploads+' saved source ZIPs, '+(offer.limits.upload_bytes/1048576).toFixed(2)+' MiB of source ZIP storage. Node.js '+(offer.limits.node?'included.':'not included.'));
  if(offer.price){note(offer.price.tax_behavior==='inclusive'?'Tax included in the base price.':offer.price.tax_behavior==='exclusive'?'Tax may be added at checkout.':'Tax treatment will be confirmed at checkout.');action('Choose '+offer.plan,()=>api('/api/billing/checkout',{workspace,plan:offer.plan}));}
 }
}
$('billing-refresh').addEventListener('click',()=>{const workspace=$('workspace').value;if(testBilling&&workspaces.find(w=>w.id===workspace)?.role==='owner')loadBilling(workspace,generation).catch(error);});


function resetDomainPanel(){
 domainOrderGeneration++;$('domain-orders').replaceChildren();
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
 const prepare=document.createElement('button');prepare.type='button';prepare.textContent='Prepare domain order';let preparing=false;
 prepare.addEventListener('click',async()=>{
  if(preparing||version!==generation)return;
  if(Date.parse(offer.expires_at)<=Date.now()){error(new Error('Quote expired. Search again.'));return;}
  const workspace=$('workspace').value;
  preparing=true;prepare.disabled=true;
  try{const order=await api('/api/domains/orders',{workspace,quote:quote.id});if(version===generation){prepare.textContent=order.state==='canceled'?'Order canceled. Search for a new quote.':'Order prepared';await loadDomainOrders(workspace,version);}}
  catch(e){if(version===generation){error(e);prepare.disabled=false;}}
  finally{preparing=false;}
 });content.append(prepare);

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

async function loadDomainOrders(workspace,version){
 if(version!==generation||workspace!==$('workspace').value)return;
 const request=++domainOrderGeneration;
 const current=()=>version===generation&&request===domainOrderGeneration&&workspace===$('workspace').value;
 try{
  const data=await api('/api/domains/orders?workspace='+encodeURIComponent(workspace));if(!current())return;
  const content=$('domain-orders');content.replaceChildren();
  if(!data.orders.length){content.textContent='No domain orders yet.';return;}
  for(const order of data.orders){
   const row=document.createElement('div');row.className='project';
   const title=document.createElement('strong');title.textContent=order.offer.domain;row.append(title);
   const detail=document.createElement('p');detail.textContent=billingAmount(order.offer.registration_minor,order.offer.currency)+' for one year · '+(order.state==='canceled'?'Canceled':'Awaiting payment setup. Not registered.');row.append(detail);
   if(order.state==='awaiting_payment'){
    const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel order';let busy=false;
    cancel.addEventListener('click',async()=>{if(busy||!current())return;busy=true;cancel.disabled=true;try{await api('/api/domains/orders/cancel',{workspace,id:order.id});if(current())await loadDomainOrders(workspace,version);}catch(e){if(current()){error(e);cancel.disabled=false;}}finally{busy=false;}});row.append(cancel);
   }
   content.append(row);
  }
 }catch(e){if(current()){$('domain-orders').textContent='Domain order history is unavailable.';error(e);}}
}
$('domain-orders-refresh').addEventListener('click',()=>{const workspace=$('workspace').value;if(domainQuotes&&workspaces.find(w=>w.id===workspace)?.role==='owner')loadDomainOrders(workspace,generation);});
