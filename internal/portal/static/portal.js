'use strict';
const $=id=>document.getElementById(id);
let csrf='',workspaces=[],generation=0,flow='',testBilling=false,billingManagement=false,inviteOnly=false,clientInvitations=false,githubConnections=false,billingGeneration=0,merchantEnabled=false,merchantCountries=[],merchantGeneration=0,productGeneration=0,merchantOrdersGeneration=0,domainQuotes=false,domainOrderGeneration=0,domainExpiryTimer;
let nodeStatusCards=new Map(),nodeStatusState=null;
let containerStatusCards=new Map(),containerStatusState=null;
let staticStatusCards=new Map(),staticStatusState=null;
let projectDeletionTimer=null,projectDeletionPollVersion=0,projectDeletionPollWorkspace='';
const projectDomainStates=new Map();
const projectRefreshes=new Map();
const domainRefreshes=new WeakMap();
const nodeStatusInterval=5000;
const workspacePanels={projects:'projects-panel',billing:'billing-panel',team:'member-panel',domains:'domain-panel',store:'merchant-panel',shared:'shared-panel'};
let currentView=location.pathname.startsWith('/billing/')?'billing':'projects';
let billingTimer=null,billingPollCount=0;
let selectedWebsite='';
let projectAvailability=null,containerHosting=false;
function stopBillingRefresh(){clearTimeout(billingTimer);billingTimer=null;}
function selectWorkspaceView(view){
 if(!workspacePanels[view]||$('nav-'+view).hidden)view='projects';
 currentView=view;
 $('workspace-title').textContent=view==='shared'?'Websites shared with you.':'Your clients. Your websites.';
 $('workspace-intro').textContent=view==='shared'?'Review your website and open its published address. Your developer manages updates.':'Publish client websites, connect their domains, and manage updates from one workspace.';
 for(const [name,id] of Object.entries(workspacePanels)){
  $(id).dataset.inactive=String(name!==view);
  $('nav-'+name).setAttribute('aria-current',name===view?'page':'false');
 }
 if(view!=='billing')stopBillingRefresh();
}
function configureWorkspaceNavigation(selected){
 const owner=selected?.role==='owner';
 for(const [view,available] of Object.entries({projects:true,shared:true,billing:owner&&testBilling,team:owner,domains:owner&&domainQuotes,store:owner&&merchantEnabled}))$('nav-'+view).hidden=!available;
 $('new-project-details').hidden=!selected||selected.role==='viewer';
 selectWorkspaceView(currentView);
}
function refreshCurrentBilling(){
 const workspace=$('workspace').value;
 if(document.hidden||$('workspace-view').hidden||currentView!=='billing'||!testBilling||workspaces.find(w=>w.id===workspace)?.role!=='owner')return;
 loadBilling(workspace,generation).catch(e=>{if(e.status===401)signedOut();else error(e);});
}
for(const view of Object.keys(workspacePanels))$('nav-'+view).addEventListener('click',()=>{selectWorkspaceView(view);if(view==='projects')showWebsite('');if(view==='shared')loadSharedWebsites();if(view==='billing'){billingPollCount=0;refreshCurrentBilling();}});
window.addEventListener('focus',()=>{billingPollCount=0;refreshCurrentBilling();});
document.addEventListener('visibilitychange',()=>{if(document.hidden)stopBillingRefresh();else{billingPollCount=0;refreshCurrentBilling();}});

const fragment=new URLSearchParams(location.hash.slice(1));
const githubCallbackParams=new URLSearchParams(location.search);
let githubCallbackCode=githubCallbackParams.get('code')||'';
let githubCallbackState=githubCallbackParams.get('state')||'';
const githubCallbackError=githubCallbackParams.get('error')||'';
let githubCallbackHandled=false;
if(githubCallbackCode||githubCallbackState||githubCallbackError)history.replaceState(null,'',location.pathname+location.hash);
let actionToken=fragment.get('verify')||fragment.get('reset')||fragment.get('invite')||fragment.get('client-invite')||'';
const initialFlow=fragment.has('verify')?'verify':fragment.has('reset')?'reset':fragment.has('invite')?'invite':fragment.has('client-invite')?'client-invite':'';
let pendingInvite=initialFlow==='invite'?actionToken:'';
let pendingClientInvite=fragment.get('client-invite')||'';
if(location.hash)history.replaceState(null,'',location.pathname+location.search);
async function api(path,body,signal){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined,signal});const data=await response.json();if(!response.ok){const error=new Error(data.error||'Request failed');error.status=response.status;throw error;}return data;}
function error(e){$('error').textContent=e.message;$('error').hidden=false;}
function stopNodeStatusRefresh(){const state=nodeStatusState;nodeStatusCards.clear();if(!state)return;state.stopped=true;clearTimeout(state.timer);state.timer=null;if(state.controller)state.controller.abort();if(nodeStatusState===state)nodeStatusState=null;nodeStatusCards.clear();}
function stopContainerStatusRefresh(){const state=containerStatusState;containerStatusCards.clear();containerStatusState=null;if(state){state.stopped=true;clearTimeout(state.timer);state.controller?.abort();}}
function signedOut(){sharedRequest++;$('shared-websites').replaceChildren();$('shared-status').textContent='';selectedWebsite='';stopStaticStatusRefresh();stopBillingRefresh();currentView='projects';stopNodeStatusRefresh();stopContainerStatusRefresh();generation++;$('error').hidden=true;resetDomainPanel();$('domain-panel').hidden=true;billingGeneration++;merchantGeneration++;productGeneration++;merchantOrdersGeneration++;$('billing-content').replaceChildren();$('billing-panel').hidden=true;$('merchant-content').replaceChildren();$('product-content').replaceChildren();$('merchant-orders-content').replaceChildren();$('merchant-panel').hidden=true;csrf='';workspaces=[];$('workspace-view').hidden=true;$('logout').hidden=true;$('login').hidden=false;$('account-flow').hidden=true;$('projects').replaceChildren();}
async function loadProjects(){projectAvailability=null;$('project-form').querySelector('button[type=submit],button').disabled=true;stopStaticStatusRefresh();stopBillingRefresh();billingPollCount=0;stopNodeStatusRefresh();const version=++generation;const workspace=$('workspace').value;const selected=workspaces.find(w=>w.id===workspace);resetDomainPanel();$('domain-panel').hidden=!domainQuotes||!selected||selected.role!=='owner';billingGeneration++;merchantGeneration++;productGeneration++;merchantOrdersGeneration++;$('billing-panel').hidden=!testBilling||!selected||selected.role!=='owner';$('billing-content').replaceChildren();$('merchant-panel').hidden=!merchantEnabled||!selected||selected.role!=='owner';$('merchant-content').replaceChildren();$('product-content').replaceChildren();$('merchant-orders-content').replaceChildren();$('project-form').hidden=!selected||selected.role==='viewer'; $('member-panel').hidden=!selected||selected.role!=='owner';$('members').replaceChildren();$('projects').replaceChildren();configureWorkspaceNavigation(selected);if(!workspace)return;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;renderProjectAvailability(data.availability);if(selectedWebsite&&!data.projects.some(project=>project.id===selectedWebsite))selectedWebsite='';for(const project of data.projects){await appendProject(project,selected.role,version);if(version!==generation)return;}if(!data.projects.length){$('projects').textContent='Your first website starts here. Create a project to get started.';$('new-project-details').open=true;}if(selected.role==='owner'){if(domainQuotes)loadDomainOrders(workspace,version);await loadMembers(workspace,version);if(version===generation&&testBilling)await loadBilling(workspace,version);if(version===generation&&merchantEnabled){await loadMerchant(workspace,version);loadMerchantProducts(workspace,version);loadMerchantOrders(workspace,version);}}if(version===generation)startNodeStatusRefresh(version,workspace);}
async function appendProject(project,role,version){if(version!==generation)return;const card=document.createElement('div');card.className='project';card.dataset.projectId=project.id;card.dataset.projectKind=project.kind;const name=document.createElement('strong');name.className='project-name';name.textContent=project.name;const kind=document.createElement('span');kind.textContent=(project.kind==='node'?'Node.js':project.kind==='container'?'Docker container':'Static website');const heading=document.createElement('div');heading.className='project-heading';const icon=document.createElement('span');icon.className='project-icon';icon.textContent=project.kind==='node'?'JS':project.kind==='container'?'IMG':'</>';const headingInfo=document.createElement('div');headingInfo.className='project-heading-info';headingInfo.append(name,kind);heading.append(icon,headingInfo);card.append(heading);$('projects').append(card);if(project.deleting){renderDeletingProject(card,project,version);startProjectDeletionPolling(version,$('workspace').value);return;}await projectUploads(card,project,role,version);if(version!==generation||!card.isConnected)return;organizeProject(card);await renderProjectDomains(card,project,role,version);if(version!==generation||!card.isConnected)return;renderProjectGithub(card,project,role,version);renderProjectRename(card,project,role,version);renderProjectDeletion(card,project,role,version);renderRuntimeLogs(card,project,role,version);renderProjectClients(card,project,role,version);}
async function loadSession(){const data=await api('/api/session');csrf=data.csrf;workspaces=data.workspaces;$('account').textContent=data.account.email;$('workspace').replaceChildren();for(const workspace of workspaces){const option=document.createElement('option');option.value=workspace.id;option.textContent=workspace.name+' · '+workspace.role;$('workspace').append(option);}$('login').hidden=true;$('workspace-view').hidden=false;$('logout').hidden=false;await loadProjects();const session=csrf;const shared=await loadSharedWebsites();if(session===csrf&&currentView==='projects'&&!$('projects').querySelector('.project')&&shared>0)selectWorkspaceView('shared');if(pendingInvite)showFlow('invite');if(pendingClientInvite)showFlow('client-invite');}
async function submit(form,fn){$('error').hidden=true;const button=form.querySelector('button');button.disabled=true;try{await fn();}catch(e){error(e);}finally{button.disabled=false;if(form.id==='project-form'&&projectAvailability)renderProjectAvailability(projectAvailability);}}
$('login-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{try{await api('/api/login',{email:$('email').value,password:$('password').value});}finally{$('password').value='';}await loadSession();if(githubConnections&&(githubCallbackCode||githubCallbackState||githubCallbackError))await completeGithubCallback();});});
$('project-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{const version=generation;const workspace=$('workspace').value;await api('/api/projects',{workspace,name:$('project-name').value,kind:$('project-kind').value});if(version!==generation)return;$('project-name').value='';$('new-project-details').open=false;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;renderProjectAvailability(data.availability);const role=workspaces.find(w=>w.id===workspace)?.role;for(const project of data.projects){if(!projectCard(project)){if(!$('projects').querySelector('.project'))$('projects').replaceChildren();await appendProject(project,role,version);}}startNodeStatusRefresh(version,workspace);});});
$('workspace').addEventListener('change',()=>{selectedWebsite='';loadProjects().catch(error);});
$('logout').addEventListener('click',async()=>{try{await api('/api/logout',{});signedOut();}catch(e){error(e);}});
function showFlow(kind){
 flow=kind;$('error').hidden=true;$('login').hidden=true;$('workspace-view').hidden=true;$('account-flow').hidden=false;$('account-form').hidden=false;
 const titles={register:'Create your account',resend:'Resend verification email',forgot:'Reset your password',reset:'Choose a new password',verify:'Verify your email',invite:'Join a workspace','client-invite':'Accept client access'};
 $('flow-title').textContent=titles[kind];$('flow-copy').textContent=kind==='resend'?'Enter your account email. Check your spam folder and wait at least a minute between requests.':kind==='invite'?'Accept this invitation using the verified account for the invited email address.':kind==='client-invite'?'Accept this invitation with the verified account for the invited email address. You will get read-only access to this website in Shared with me. If you need to register or verify first, keep this tab open and return to the original invitation after signing in.':kind==='verify'?'Confirm that you want to verify this email address.':kind==='register'?(inviteOnly?'Create your invited account.':'Create a workspace for your websites.'):kind==='reset'?'Use a password of at least 12 characters. Existing sessions will be signed out.':'Enter your account email to request a reset link.';
 for(const [name,visible] of [['email',kind==='register'||kind==='forgot'||kind==='resend'],['password',kind==='register'||kind==='reset'],['workspace',kind==='register']]){ $('flow-'+name+'-label').hidden=!visible;$('flow-'+name).required=visible;$('flow-'+name).value=''; }
 $('flow-submit').textContent=kind==='resend'?'Send verification link':kind==='invite'?'Join workspace':kind==='client-invite'?'Accept client access':kind==='verify'?'Verify email':kind==='reset'?'Change password':kind==='register'?'Create account':'Send reset link';
}
$('open-signup').addEventListener('click',()=>showFlow('register'));
$('open-forgot').addEventListener('click',()=>showFlow('forgot'));
$('open-resend').addEventListener('click',()=>showFlow('resend'));
$('back-login').addEventListener('click',()=>{actionToken='';$('error').hidden=true;$('account-flow').hidden=true;$('login').hidden=false;});
$('account-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{
 let path,body;
 if(flow==='invite'){path='/api/invitations/accept';body={token:pendingInvite||actionToken};}
 if(flow==='client-invite'){path='/api/client-invitations/accept';body={token:pendingClientInvite||actionToken};}
 if(flow==='register'){path='/api/register';body={email:$('flow-email').value,password:$('flow-password').value,workspace:$('flow-workspace').value};}
 if(flow==='resend'){path='/api/verification/resend';body={email:$('flow-email').value};}
 if(flow==='forgot'){path='/api/password/forgot';body={email:$('flow-email').value};}
 if(flow==='verify'){path='/api/verify';body={token:actionToken};}
 if(flow==='reset'){path='/api/password/reset';body={token:actionToken,password:$('flow-password').value};}
 const data=await api(path,body);$('flow-password').value='';$('account-form').hidden=true;$('flow-copy').textContent=data.message||'Access accepted.';if(flow==='reset'||flow==='verify')actionToken='';if(flow==='invite'){pendingInvite='';actionToken='';flow='';$('account-flow').hidden=true;await loadSession();}if(flow==='client-invite'){pendingClientInvite='';actionToken='';flow='';$('account-flow').hidden=true;$('workspace-view').hidden=false;$('login').hidden=true;$('logout').hidden=false;selectWorkspaceView('shared');await loadSharedWebsites();}
 });});
async function initialize(){
 containerHosting=(await api('/api/config')).container_hosting===true;$('container-kind').hidden=!containerHosting;$('container-filter').hidden=!containerHosting;
 const config=await api('/api/config');domainQuotes=config.domain_quotes===true;testBilling=config.test_billing===true;billingManagement=config.billing_management===true;inviteOnly=config.invite_only===true;clientInvitations=config.client_invitations===true;githubConnections=config.github_connections===true;merchantEnabled=config.merchant===true;merchantCountries=Array.isArray(config.merchant_countries)?config.merchant_countries.filter(country=>typeof country==='string'):[];$('open-signup').hidden=!config.signup;$('open-forgot').hidden=!config.account_mail;$('open-resend').hidden=!config.account_mail;$('registration-note').textContent=config.signup?(inviteOnly?'Registration is by invitation.':'Verify your email before signing in.'):'Registration is closed.';
 if(initialFlow==='invite'||initialFlow==='client-invite'){try{await loadSession();}catch(e){signedOut();if(e.status===401){$('login-copy').textContent=initialFlow==='client-invite'?'Sign in with the invited email to accept read-only client access. New users must register and verify their email first.':'Sign in with the invited email to accept. New users must register and verify their email first.';if(initialFlow==='client-invite'&&config.signup)$('open-signup').hidden=false;}else throw e;}return;}
 if(initialFlow){if(!config.account_mail)throw new Error('Account recovery is unavailable. Contact the operator.');showFlow(initialFlow);return;}
 try{await loadSession();}catch(e){signedOut();if(e.status!==401)throw e;return;}
 if(githubConnections&&(githubCallbackCode||githubCallbackState||githubCallbackError)){try{await completeGithubCallback();}catch(e){error(e);}}
}
initialize().catch(error);

window.addEventListener('hashchange',()=>{
 const params=new URLSearchParams(location.hash.slice(1));
 const kind=params.has('verify')?'verify':params.has('reset')?'reset':params.has('invite')?'invite':params.has('client-invite')?'client-invite':'';
 if(!kind)return;actionToken=params.get(kind)||'';history.replaceState(null,'',location.pathname+location.search);if(kind==='invite')pendingInvite=actionToken;if(kind==='client-invite')pendingClientInvite=actionToken;if((kind==='invite'||kind==='client-invite')&&!csrf){signedOut();error(new Error('Sign in with the invited email. New users must register and verify their email first, then reopen the original invitation.'));return;}showFlow(kind);
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
function renderProjectRename(card,project,role,version){
 if(!['owner','developer'].includes(role)||project.deleting||!card.isConnected||version!==generation)return;
 const details=disclosure('Rename project','project-rename');const hint=document.createElement('p');hint.textContent='This changes the name shown here. Your website address stays the same.';details.append(hint);
 const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project name';const input=document.createElement('input');input.type='text';input.required=true;input.maxLength=100;input.autocomplete='off';input.spellcheck=false;input.value=project.name;label.append(input);const button=document.createElement('button');button.type='submit';button.textContent='Save name';form.append(label,button);details.append(form);form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const updated=await api('/api/projects/rename',{project:project.id,name:input.value});if(version!==generation||!card.isConnected||card.dataset.deleting==='true')return;if(!updated||updated.id!==project.id||typeof updated.name!=='string')throw new Error('Rename response was invalid.');Object.assign(project,updated);const headingName=card.querySelector('.project-name');if(headingName)headingName.textContent=project.name;input.value=project.name;details.open=false;const danger=card.querySelector('.project-danger');const dangerOpen=danger?.open===true;danger?.remove();renderProjectDeletion(card,project,role,version);const freshDanger=card.querySelector('.project-danger');if(freshDanger)freshDanger.open=dangerOpen;});});card.append(details);
}
function renderDeletingProject(card,project,version){
 card.dataset.deleting='true';card.classList.add('project-deleting');nodeStatusCards.delete(project.id);for(const child of [...card.children])if(!child.classList.contains('project-heading'))child.remove();
 const status=document.createElement('p');status.className='project-status';status.setAttribute('role','status');status.textContent=project.deletion_error?'Project deletion needs attention: '+project.deletion_error:'Deleting project… Disconnecting the website and removing its uploads and releases.';card.append(status);
 if(project.deletion_error){const note=document.createElement('p');note.className='deletion-error';note.textContent='Cleanup retries automatically. Contact support if this continues.';card.append(note);}
}
function renderProjectDeletion(card,project,role,version){
 if(role!=='owner'||!card.isConnected||version!==generation||project.deleting)return;
 const previous=card.querySelector('.project-danger');const wasOpen=previous?.open===true;previous?.remove();const details=disclosure('Danger zone','project-danger');details.open=wasOpen;const intro=document.createElement('p');intro.textContent='Deleting this project is irreversible. The website will go offline, uploads and releases will be deleted, and any custom domain will be disconnected. Domain registrations are unaffected.';details.append(intro);
 const form=document.createElement('form');const label=document.createElement('label');label.textContent='Type '+project.name+' to confirm';const input=document.createElement('input');input.type='text';input.required=true;input.autocomplete='off';input.spellcheck=false;input.setAttribute('aria-label','Type '+project.name+' to confirm');const button=document.createElement('button');button.type='submit';button.className='button-danger';button.textContent='Delete project';button.disabled=true;input.addEventListener('input',()=>{button.disabled=input.value!==project.name;});form.append(label,button);label.append(input);details.append(form);form.addEventListener('submit',event=>{event.preventDefault();if(input.value!==project.name)return;submit(form,async()=>{const accepted=await api('/api/projects/delete',{project:project.id,name:input.value});if(version!==generation)return;project.deleting=true;project.deletion_error=accepted.deletion_error||'';renderDeletingProject(card,project,version);startProjectDeletionPolling(version,$('workspace').value);});});card.append(details);
}
function removeProjectCard(card){const id=card.dataset.projectId;nodeStatusCards.delete(id);projectRefreshes.delete(id);projectDomainStates.delete(id);card.remove();}
function updateProjectsEmptyState(){if($('projects').querySelector('.project'))return;$('projects').textContent='Your first website starts here. Create a project to get started.';$('new-project-details').open=true;}
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
   const containerForm=card.querySelector('.container-release-form');const freshContainerForm=staging.querySelector('.container-release-form');
   if(containerForm&&freshContainerForm){
    const oldAccess=containerForm.querySelector('select[name="credential_id"]'), freshAccess=freshContainerForm.querySelector('select[name="credential_id"]');
    if(oldAccess&&freshAccess){const selected=oldAccess.value;oldAccess.replaceChildren(...[...freshAccess.options].map(option=>option.cloneNode(true)));oldAccess.disabled=freshAccess.disabled;oldAccess.value=[...oldAccess.options].some(option=>option.value===selected)?selected:'';}
    const oldEnvironment=containerForm.querySelector('select[name="environment_id"]'), freshEnvironment=freshContainerForm.querySelector('select[name="environment_id"]');
    if(oldEnvironment&&freshEnvironment){const selected=oldEnvironment.value;oldEnvironment.replaceChildren(...[...freshEnvironment.options].map(option=>option.cloneNode(true)));oldEnvironment.disabled=freshEnvironment.disabled;oldEnvironment.value=[...oldEnvironment.options].some(option=>option.value===selected)?selected:'';}
    freshContainerForm.replaceWith(containerForm);
   }
   const containerCredentials=card.querySelector('.container-credentials');const freshContainerCredentials=staging.querySelector('.container-credentials');
   if(containerCredentials&&freshContainerCredentials)freshContainerCredentials.replaceWith(containerCredentials);
   const containerEnvironments=card.querySelector('.container-environments');const freshContainerEnvironments=staging.querySelector('.container-environments');
   if(containerEnvironments&&freshContainerEnvironments)freshContainerEnvironments.replaceWith(containerEnvironments);
   const focusText=focused?.textContent;const focusTag=focused?.tagName;
   const heading=card.querySelector('.project-heading');const rename=card.querySelector('.project-rename');const domains=card.querySelector('.project-domains');const github=card.querySelector('.project-github');const danger=card.querySelector('.project-danger');
   const children=[heading,card.querySelector('.website-summary'),card.querySelector('.website-detail-links'),...staging.childNodes,card.querySelector('.project-runtime-logs'),card.querySelector('.project-clients'),domains,github,rename,danger].filter(Boolean);
   // Move retained nodes in place rather than detaching their inputs.
   for(const child of [...card.childNodes])if(!children.includes(child))child.remove();
   for(const child of children)card.append(child);
   const nodeEntry=nodeStatusCards.get(project.id);if(nodeEntry)nodeEntry.card=card;
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
 const children=[...card.children];const uploads=disclosure('Upload a new version','upload-details');const history=disclosure('Files & version history','project-history');
 let pastStatus=children.some(child=>child.classList.contains('project-workflow')||child.classList.contains('node-live'));
 for(const child of children){
  if(child.classList.contains('website-summary')||child.classList.contains('website-detail-links')||child.classList.contains('project-heading')||child.classList.contains('node-live')||child.classList.contains('project-rename')||child.classList.contains('project-workflow'))continue;
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

function uploadDownloadControl(row,upload,project,role,version){
 if(role!=='owner'&&role!=='developer')return;
 const button=document.createElement('button');button.type='button';button.textContent='Download ZIP';
 const note=document.createElement('p');note.className='muted upload-download-error';note.setAttribute('role','alert');note.hidden=true;
 button.addEventListener('click',async()=>{
  const card=row.closest('.project');
  if(version!==generation||!card?.isConnected||card.dataset.deleting==='true'||button.disabled)return;
  button.disabled=true;button.textContent='Preparing download…';note.hidden=true;
  let objectURL='';
  try{
   const response=await fetch('/api/uploads/download',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({project:project.id,id:upload.id})});
   if(!response.ok){let message='Download failed';try{const data=await response.json();message=data.error||message;}catch{try{const text=await response.text();if(text)message=text;}catch{}}const failure=new Error(message);failure.status=response.status;throw failure;}
   if(!(response.headers.get('Content-Type')||'').startsWith('application/zip'))throw new Error('Archive response was invalid.');
   const blob=await response.blob();
   if(blob.size===0||blob.size>10*1024*1024)throw new Error('Archive response was invalid.');
   if(version!==generation||!card?.isConnected||card.dataset.deleting==='true')return;
   const downloadURL=URL.createObjectURL(blob);objectURL=downloadURL;const link=document.createElement('a');link.href=downloadURL;link.download=String(upload.id)+'.zip';link.hidden=true;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(downloadURL),1000);objectURL='';
  }catch(e){
   if(objectURL)URL.revokeObjectURL(objectURL);
   if(e.status===401){signedOut();return;}
   if(version!==generation||!card?.isConnected||card.dataset.deleting==='true')return;
   note.textContent=e.message;note.hidden=false;
  }finally{
   if(version===generation&&card?.isConnected&&card.dataset.deleting!=='true'){button.disabled=false;button.textContent='Download ZIP';}
  }
 });
 row.append(button,note);
}

// Static publication polling.
function stopStaticStatusRefresh(){
 const state=staticStatusState;staticStatusState=null;staticStatusCards.clear();
 if(state){state.stopped=true;clearTimeout(state.timer);state.controller?.abort();}
}
function staticPublicationSnapshot(publication){return JSON.stringify([publication.available,publication.active,publication.site,publication.jobs]);}
function trackStaticStatus(project,role,version,publication){
 if(version!==generation||project.deleting)return;
 const pending=!publication.available||publication.jobs.some(job=>job.state==='queued'||job.state==='running');
 if(!pending){staticStatusCards.delete(project.id);return;}
 staticStatusCards.set(project.id,{project,role,version,snapshot:staticPublicationSnapshot(publication)});
 if(!staticStatusState||staticStatusState.stopped){staticStatusState={version,workspace:$('workspace').value,inFlight:false,stopped:false,timer:null,controller:null};}
 const state=staticStatusState;
 if(!state.inFlight&&state.timer===null)state.timer=setTimeout(()=>pollStaticStatus(state),5000);
}
async function pollStaticStatus(state){
 if(!state||state.stopped||state!==staticStatusState||state.version!==generation||state.workspace!==$('workspace').value||state.inFlight)return;
 clearTimeout(state.timer);state.timer=null;
 if(!staticStatusCards.size){staticStatusState=null;return;}
 if(document.hidden||currentView!=='projects'){state.timer=setTimeout(()=>pollStaticStatus(state),5000);return;}
 state.inFlight=true;state.controller=new AbortController();
 const current=()=>state===staticStatusState&&!state.stopped&&state.version===generation&&state.workspace===$('workspace').value;
 try{
  for(const [id,entry] of [...staticStatusCards]){
   if(!current())return;
   const card=projectCard(entry.project);
   if(!card?.isConnected||card.dataset.deleting==='true'){staticStatusCards.delete(id);continue;}
   try{
    const publication=await api('/api/publications?project='+encodeURIComponent(id),undefined,state.controller.signal);
    if(!current())return;
    if(staticStatusCards.get(id)!==entry||!card.isConnected||card.dataset.deleting==='true')continue;
    const snapshot=staticPublicationSnapshot(publication);
    if(snapshot!==entry.snapshot){
     await refreshProject(entry.project,entry.role,entry.version);
     // The refresh registers its own newer snapshot. Never replace it with this response.
    }
   }catch(e){
    if(!current())return;
    if(e.status===401){signedOut();return;}
    if(staticStatusCards.get(id)===entry&&(e.status===403||e.status===404))staticStatusCards.delete(id);
    // Transient failures retain the snapshot so the next tick retries.
   }
  }
 }finally{
  state.inFlight=false;state.controller=null;
  if(current()){
   if(staticStatusCards.size)state.timer=setTimeout(()=>pollStaticStatus(state),5000);
   else staticStatusState=null;
  }
 }
}
// End static publication polling.

// Upload transport reports bytes sent separately from server validation.
function sendProjectUpload(project,file,token,onProgress){
 return new Promise((resolve,reject)=>{
  const xhr=new XMLHttpRequest();xhr.open('POST','/api/uploads?project='+encodeURIComponent(project));xhr.timeout=120000;
  xhr.setRequestHeader('Content-Type','application/zip');xhr.setRequestHeader('X-CSRF-Token',token);
  xhr.upload.onprogress=event=>{if(event.lengthComputable)onProgress(Math.min(100,Math.round(event.loaded/event.total*100)));};
  xhr.upload.onload=()=>onProgress(100);
  xhr.onload=()=>{let data;try{data=JSON.parse(xhr.responseText);}catch{reject(new Error('Upload response was invalid. Refresh the file history before retrying.'));return;}
   if(xhr.status<200||xhr.status>=300){const error=new Error(data.error||'Upload failed');error.status=xhr.status;reject(error);return;}resolve(data);
  };
  xhr.onerror=()=>reject(new Error('Upload connection lost. Check file history before retrying.'));
  xhr.ontimeout=()=>reject(new Error('Upload timed out. Check file history before retrying.'));
  xhr.onabort=()=>reject(new Error('Upload cancelled.'));
  xhr.send(file);
 });
}
// End upload transport.
function createProgress(label){
 const box=document.createElement('div');box.className='operation-progress';
 const caption=document.createElement('span');caption.className='progress-caption';caption.textContent=label;caption.setAttribute('role','status');
 const bar=document.createElement('progress');bar.max=100;bar.setAttribute('aria-label',label);box.append(caption,bar);
 return {box,caption,bar};
}
async function uploadWithProgress(project,file,form,version){
 form.querySelector('.operation-progress')?.remove();
 const progress=createProgress('Uploading files · 0%');progress.bar.value=0;form.append(progress.box);
 const current=()=>version===generation&&form.isConnected;
 try{
  const data=await sendProjectUpload(project.id,file,csrf,percent=>{
   if(!current())return;
   if(percent>=100){progress.caption.textContent='Upload sent · Checking your files…';progress.bar.removeAttribute('value');progress.bar.setAttribute('aria-label','Checking uploaded files');}
   else{progress.bar.value=percent;progress.caption.textContent='Uploading files · '+percent+'%';progress.bar.setAttribute('aria-label','Uploading files');}
  });
  if(current()){progress.bar.value=100;progress.bar.setAttribute('aria-label','Upload complete');progress.caption.textContent='Upload complete · Files checked';}
  return data;
 }catch(e){if(current()){progress.box.dataset.failed='true';progress.bar.hidden=true;progress.caption.textContent=e.message;}throw e;}
}

async function projectUploads(card,project,role,version){
 if(project.kind==='container')return containerProjectUploads(card,project,role,version);
 const result=await api('/api/uploads?project='+encodeURIComponent(project.id));if(version!==generation)return;
 if(project.kind==='node')return nodeProjectUploads(card,project,role,version,result);
 const publication=await api('/api/publications?project='+encodeURIComponent(project.id));if(version!==generation)return;
 trackStaticStatus(project,role,version,publication);
 const pending=publication.jobs.some(j=>j.state==='queued'||j.state==='running');const active=publication.jobs.find(j=>j.id===publication.active);
 const publishKey=crypto.randomUUID();card.append(renderWorkflow(project,role,result.uploads,publication,async flow=>{
  if(version!==generation||project.deleting)return;
  await api('/api/publications',{project:project.id,upload:flow.id,key:publishKey});if(version===generation)await refreshProject(project,role,version);
 }));
 if(active&&publication.site){const link=document.createElement('a');link.href=publication.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent=(new URL(publication.site).hostname.endsWith('.sslip.io')?'Visit website':'Visit '+new URL(publication.site).hostname)+' ↗';card.append(link);}
 const refresh=document.createElement('button');refresh.textContent='Refresh release status';refresh.addEventListener('click',()=>refreshProject(project,role,version).catch(error));card.append(refresh);
 for(const job of publication.jobs){const line=document.createElement('p');line.textContent='Revision '+job.revision+' · '+job.state+(job.id===publication.active?' · Current':'')+(job.state==='running'?' · Applying or awaiting reconciliation':'');card.append(line);
  if(publication.available&&role==='owner'&&job.state==='running'){const resume=document.createElement('button');resume.textContent='Resume revision '+job.revision;resume.addEventListener('click',async()=>{if(!confirm('Resume revision '+job.revision+'? Its saved website files will be published using your permission. A worker that is still active cannot be interrupted.'))return;resume.disabled=true;try{await api('/api/publications/resume',{project:project.id,job:job.id,upload:job.upload_id});if(version===generation)await refreshProject(project,role,version);}catch(e){error(e);resume.disabled=false;}});card.append(resume);}
 }
 const copy=document.createElement('p');copy.textContent=project.kind==='node'?'ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Omit node_modules and secrets.':'ZIP up to 10 MiB. Include index.html at the root. Omit secrets.';card.append(copy);
 if(role!=='viewer'){
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload ZIP';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');await uploadWithProgress(project,file,form,version);input.value='';if(version===generation)await refreshProject(project,role,version);});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
 if(publication.available&&role!=='viewer'){const publish=document.createElement('button');const previous=publication.jobs.some(j=>j.upload_id===upload.id&&j.state==='succeeded');const isCurrent=active&&active.upload_id===upload.id;publish.textContent=isCurrent?'Current upload':previous?'Restore this upload':'Publish this upload';publish.disabled=pending||isCurrent;const requestKey=crypto.randomUUID();publish.addEventListener('click',async()=>{publish.disabled=true;try{await api('/api/publications',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await refreshProject(project,role,version);}catch(e){error(e);publish.disabled=false;}});row.append(publish);}
  uploadDownloadControl(row,upload,project,role,version);uploadDeleteControl(row,upload,project,role,version);card.append(row);
 }
}

// Pending container guidance updates without replacing upload or settings forms.
function containerWaitLabel(createdAt,now=Date.now()){
 const seconds=Math.max(0,Math.floor(now/1000-Number(createdAt)));
 if(!Number.isFinite(seconds))return 'Waiting for an update';
 return seconds<60?'Requested less than a minute ago':'Waiting '+Math.floor(seconds/60)+' min';
}
function updateContainerWaitLabels(){
 for(const label of document.querySelectorAll('.container-wait-age'))label.textContent=containerWaitLabel(label.dataset.requestedAt);
}
function appendContainerWaitDetails(panel,job){
 const age=document.createElement('p');age.className='container-wait-age';age.dataset.requestedAt=String(job.created_at);age.textContent=containerWaitLabel(job.created_at);panel.append(age);
 const note=document.createElement('p');note.className='operation-delay';note.dataset.requestedAt=String(job.created_at);note.hidden=Date.now()/1000-job.created_at<300;
 note.textContent=job.state==='queued'?'The worker has not started this deployment yet. You can cancel the queued request below, or contact support with the reference below.':'Publishing is taking longer than usual. We have not confirmed the outcome yet. You can leave this page; checks continue in the background. Contact support with the reference below if this continues. A second deployment is paused until this one is resolved.';
 const reference=document.createElement('p');reference.className='operation-reference';reference.textContent='Support reference: '+job.id;
 panel.append(note,reference);
}
// End pending container guidance.
function containerJobActive(job){return job&&(job.state==='queued'||job.state==='running');}
function containerSnapshot(data){return JSON.stringify([data.available,data.active,data.site,data.releases,data.deployments,data.private_images,data.credentials,data.environment_settings,data.environments]);}
function trackContainerStatus(project,role,version,data){
 if(version!==generation||project.deleting)return;
 const pending=!data.available||(data.deployments||[]).some(containerJobActive);
 if(!pending){containerStatusCards.delete(project.id);return;}
 containerStatusCards.set(project.id,{project,role,version,snapshot:containerSnapshot(data)});
 if(!containerStatusState||containerStatusState.stopped)containerStatusState={version,workspace:$('workspace').value,inFlight:false,stopped:false,timer:null,controller:null};
 if(!containerStatusState.inFlight&&containerStatusState.timer===null)containerStatusState.timer=setTimeout(()=>pollContainerStatus(containerStatusState),5000);
}
async function pollContainerStatus(state){
 if(!state||state.stopped||state!==containerStatusState||state.version!==generation||state.workspace!==$('workspace').value||state.inFlight)return;
 clearTimeout(state.timer);state.timer=null;if(!containerStatusCards.size){containerStatusState=null;return;}
 if(document.hidden||currentView!=='projects'){state.timer=setTimeout(()=>pollContainerStatus(state),5000);return;}
 state.inFlight=true;state.controller=new AbortController();
 try{for(const [id,entry] of [...containerStatusCards]){if(state.stopped||state!==containerStatusState)break;const card=projectCard(entry.project);if(!card?.isConnected||card.dataset.deleting==='true'){containerStatusCards.delete(id);continue;}try{const data=await api('/api/container?project='+encodeURIComponent(id),undefined,state.controller.signal);if(state.stopped||state!==containerStatusState)break;if(containerStatusCards.get(id)!==entry)continue;if(containerSnapshot(data)!==entry.snapshot)await refreshProject(entry.project,entry.role,entry.version);}catch(e){if(e.status===401){signedOut();return;}if(e.status===403||e.status===404)containerStatusCards.delete(id);}}}finally{state.inFlight=false;state.controller=null;if(state===containerStatusState&&!state.stopped){if(containerStatusCards.size)state.timer=setTimeout(()=>pollContainerStatus(state),5000);else containerStatusState=null;}}
}
function renderContainerLive(entry,data){
 const {live,project,role,version}=entry;
 const oldForm=live.querySelector('.container-release-form');
 const oldCredentials=live.querySelector('.container-credentials');
 const oldEnvironments=live.querySelector('.container-environments');
 const releases=Array.isArray(data.releases)?data.releases:[], deployments=Array.isArray(data.deployments)?data.deployments:[];
 const active=data.active||null, pending=deployments.find(containerJobActive), latest=deployments[0];
 live.replaceChildren();
 const workflow=document.createElement('section');workflow.className='project-workflow';
 const title=document.createElement('h3'),text=document.createElement('p');
 if(pending){
  workflow.dataset.tone='busy';title.textContent=pending.state==='queued'?'Your deployment is queued':'Publishing your container';
  text.textContent=pending.state==='queued'?'The deployment worker will start this release shortly. You can cancel it before work begins.':active?'Your previous release remains selected until the new release passes its checks.':'We are checking the running application and its HTTPS route before marking it live.';
 }else if(latest?.state==='failed'){
  workflow.dataset.tone='attention';title.textContent='Deployment needs attention';text.textContent='This deployment did not complete. Review your image settings, then publish a saved release again. Your previous successful release is retained.';
 }else if(!data.available){
  workflow.dataset.tone='busy';title.textContent='Container hosting setup is pending';text.textContent='You can check an image and save a release now. Publishing becomes available when hosting setup finishes.';
 }else if(active){
  workflow.dataset.tone='live';title.textContent='Your container is live';text.textContent='Check a new image for your next update, or restore a previously published release below.';
 }else{
  workflow.dataset.tone='next';title.textContent=releases.length?'Ready to publish':'Check your container image';text.textContent=releases.length?'Choose Deploy beside a saved release below.':'Enter an image reference, its listening port and a health-check path.';
 }
 workflow.append(title,text);
 if(pending||!data.available){const progress=createProgress(pending?(pending.state==='queued'?'Waiting for the deployment worker…':'Checking application and HTTPS readiness…'):'Waiting for hosting setup…');progress.bar.removeAttribute('value');workflow.append(progress.box);}
 if(pending)appendContainerWaitDetails(workflow,pending);
 live.append(workflow);
 if(active&&data.site){const link=document.createElement('a');link.href=data.site;link.target='_blank';link.rel='noopener noreferrer';link.className='site-link';link.textContent='Visit website ↗';live.append(link);}
 const refresh=document.createElement('button');refresh.type='button';refresh.className='refresh-status';refresh.textContent='Refresh status';refresh.addEventListener('click',()=>refreshProject(project,role,version).catch(error));live.append(refresh);
 const limits=document.createElement('p');limits.className='muted';limits.textContent=data.environment_settings?(data.private_images?'Docker Hub or GHCR images. Linux ARM64, non-root and stateless. Up to 512 MiB compressed image layers. Environment bundles support up to 64 variables, 8 KiB per value and 32 KiB total.':'Public Docker Hub or GHCR images only. Linux ARM64, non-root and stateless. Up to 512 MiB compressed image layers. Environment bundles support up to 64 variables, 8 KiB per value and 32 KiB total.'):(data.private_images?'Docker Hub or GHCR images. Linux ARM64, non-root and stateless. Up to 512 MiB of compressed image layers. Environment variables are not supported yet.':'Public Docker Hub or GHCR images only. Linux ARM64, non-root and stateless. Up to 512 MiB of compressed image layers. Private images and environment settings are not available yet.');live.append(limits);
 if(role!=='viewer'){
  const form=oldForm||document.createElement('form');form.className='container-release-form';
  let credential=form.querySelector('select[name=credential_id]');
 if(credential){const selected=credential.value;credential.replaceChildren(Object.assign(document.createElement('option'),{value:'',textContent:'Public image'}));for(const item of (data.credentials||[])){const option=document.createElement('option');option.value=item.id;option.textContent=item.label+' · '+item.registry;credential.append(option);}credential.value=(data.credentials||[]).some(item=>item.id===selected)?selected:'';credential.disabled=!data.private_images;}
  let environment=form.querySelector('select[name=environment_id]');
  if(environment){const selected=environment.value;environment.replaceChildren(Object.assign(document.createElement('option'),{value:'',textContent:'No environment variables'}));for(const item of (data.environments||[])){const option=document.createElement('option');option.value=item.id;option.textContent=item.label+' · '+item.names.length+' variables';environment.append(option);}environment.value=(data.environments||[]).some(item=>item.id===selected)?selected:'';environment.disabled=!data.environment_settings;}
  if(!oldForm){
   credential=document.createElement('select');credential.name='credential_id';const pub=document.createElement('option');pub.value='';pub.textContent='Public image';credential.append(pub);for(const item of (data.credentials||[])){const option=document.createElement('option');option.value=item.id;option.textContent=item.label+' · '+item.registry;credential.append(option);}credential.disabled=!data.private_images;
   const credentialLabel=document.createElement('label');credentialLabel.textContent='Image access';credentialLabel.append(credential);form.append(credentialLabel);
   environment=document.createElement('select');environment.name='environment_id';const noEnvironment=document.createElement('option');noEnvironment.value='';noEnvironment.textContent='No environment variables';environment.append(noEnvironment);for(const item of (data.environments||[])){const option=document.createElement('option');option.value=item.id;option.textContent=item.label+' · '+item.names.length+' variables';environment.append(option);}environment.disabled=!data.environment_settings;const environmentLabel=document.createElement('label');environmentLabel.textContent='Environment bundle';environmentLabel.append(environment);form.append(environmentLabel);
   const image=document.createElement('input');image.name='reference';image.required=true;image.maxLength=512;image.placeholder='ghcr.io/example/app:stable';
   const port=document.createElement('input');port.name='port';port.type='number';port.min='1024';port.max='65535';port.value='8080';port.required=true;
   const health=document.createElement('input');health.name='health_path';health.value='/';health.required=true;health.maxLength=512;
   for(const [name,input] of [['Image reference',image],['Port (1024 to 65535)',port],['Health path',health]]){const label=document.createElement('label');label.textContent=name;label.append(input);form.append(label);}
   const button=document.createElement('button');button.type='submit';button.className='button button-dark';button.textContent='Check image and save release';form.append(button);
   let requestKey='',lastInput='';
   form.addEventListener('submit',async event=>{
    event.preventDefault();if(form.dataset.busy||version!==generation)return;
    const input={reference:image.value.trim(),port:Number(port.value),health_path:health.value.trim(),credential_id:credential.value,environment_id:environment.value};const signature=JSON.stringify([input.reference,input.port,input.health_path,input.credential_id,input.environment_id]);if(!requestKey||signature!==lastInput){requestKey=crypto.randomUUID();lastInput=signature;}
    form.dataset.busy='true';button.disabled=true;form.querySelector('.container-release-error')?.remove();
    const progress=createProgress('Checking image metadata…');progress.bar.removeAttribute('value');form.append(progress.box);
    try{
     await api('/api/container/releases',{project:project.id,key:requestKey,...input});
     if(version!==generation)return;
     await refreshProject(project,role,version);requestKey='';
     const history=projectCard(project)?.querySelector('.container-history');if(history)history.open=true;
    }catch(e){if(version===generation){const note=document.createElement('p');note.className='container-release-error workflow-error';note.setAttribute('role','alert');note.textContent=e.message;form.append(note);}}
    finally{progress.box.remove();form.dataset.busy='';button.disabled=false;}
   });
  }
  live.append(form);
  if(data.private_images){
   const access=oldCredentials||disclosure('Private registry access','container-credentials');
   if(!oldCredentials){
    const hint=document.createElement('p');hint.className='muted';hint.textContent='Use a registry token with permission to pull your image. Save new access when rotating a token; retained releases keep their original access. Up to 20 saved credentials per website.';access.append(hint);
    const credentialForm=document.createElement('form');credentialForm.className='container-credential-form';
    const labelInput=document.createElement('input');labelInput.name='label';labelInput.maxLength=80;labelInput.placeholder='Production registry';
    const registry=document.createElement('select');registry.name='registry';
    for(const [value,name] of [['ghcr.io','GitHub Container Registry'],['docker.io','Docker Hub']]){const option=document.createElement('option');option.value=value;option.textContent=name;registry.append(option);}
    const username=document.createElement('input');username.name='username';username.maxLength=256;username.autocomplete='off';username.spellcheck=false;
    const password=document.createElement('input');password.name='password';password.type='password';password.maxLength=8192;password.autocomplete='new-password';
    const fields=[labelInput,registry,username,password];
    for(const [name,input] of [['Credential label',labelInput],['Registry',registry],['Registry username',username],['Access token',password]]){input.required=true;const label=document.createElement('label');label.textContent=name;label.append(input);credentialForm.append(label);}
    const save=document.createElement('button');save.type='submit';save.className='button button-dark';save.textContent='Save registry access';credentialForm.append(save);
    let requestKey=crypto.randomUUID();
    credentialForm.addEventListener('input',()=>{requestKey=crypto.randomUUID();});
    credentialForm.addEventListener('change',()=>{requestKey=crypto.randomUUID();});
    credentialForm.addEventListener('submit',async event=>{
     event.preventDefault();if(credentialForm.dataset.busy||version!==generation)return;
     credentialForm.dataset.busy='true';save.disabled=true;for(const field of fields)field.disabled=true;
     credentialForm.querySelector('.credential-feedback')?.remove();
     try{
      const result=await api('/api/container/credentials',{project:project.id,key:requestKey,label:labelInput.value.trim(),registry:registry.value,username:username.value,password:password.value});
      username.value='';password.value='';requestKey=crypto.randomUUID();
      if(version!==generation)return;
      const note=document.createElement('p');note.className='credential-feedback';note.setAttribute('role','status');note.textContent='Registry access saved.';credentialForm.append(note);
      try{await refreshProject(project,role,version);if(version!==generation)return;const next=projectCard(project)?.querySelector('select[name="credential_id"]');if(next&&result.id)next.value=result.id;}
      catch(e){if(version===generation)note.textContent='Registry access saved. Refresh status to update the image selector.';}
     }catch(e){if(version===generation){const note=document.createElement('p');note.className='credential-feedback workflow-error';note.setAttribute('role','alert');note.textContent=e.message;credentialForm.append(note);}}
     finally{credentialForm.dataset.busy='';save.disabled=false;for(const field of fields)field.disabled=false;}
    });
    access.append(credentialForm);
   }
   live.append(access);
  }
  if(data.environment_settings){
   const environments=oldEnvironments||disclosure('Environment variables','container-environments');
   if(!oldEnvironments){
    const intro=document.createElement('p');intro.className='muted';
    intro.textContent='Save a version of your environment settings, then select it when preparing a release. Saving alone does not change your website. Restoring a release uses its original settings. Up to 50 saved versions, 64 variables per version, 8192 bytes per value and 32 KiB for all names and values combined.';
    environments.append(intro);
    const envForm=document.createElement('form');envForm.className='container-environment-form';
    const labelInput=document.createElement('input');labelInput.name='label';labelInput.required=true;labelInput.maxLength=80;labelInput.placeholder='Production settings';
    const label=document.createElement('label');label.textContent='Settings label';label.append(labelInput);envForm.append(label);
    const rows=document.createElement('div');rows.className='environment-rows';
    const add=document.createElement('button');add.type='button';add.textContent='Add variable';
    const save=document.createElement('button');save.type='submit';save.className='button button-dark';save.textContent='Save environment settings';
    const feedback=document.createElement('p');feedback.className='environment-feedback';feedback.setAttribute('role','status');
    let requestKey=crypto.randomUUID();
    const updateAdd=()=>{add.disabled=envForm.dataset.busy==='true'||rows.children.length>=64;};
    const changed=()=>{requestKey=crypto.randomUUID();feedback.textContent='';feedback.className='environment-feedback';feedback.setAttribute('role','status');};
    function addRow(){
     if(rows.children.length>=64)return;
     const row=document.createElement('div');row.className='environment-row';
     const key=document.createElement('input');key.name='name';key.required=true;key.maxLength=128;key.pattern='[A-Za-z_][A-Za-z0-9_]*';key.spellcheck=false;key.autocomplete='off';key.placeholder='API_TOKEN';
     const secret=document.createElement('input');secret.name='value';secret.type='password';secret.maxLength=8192;secret.autocomplete='new-password';secret.spellcheck=false;
     const nameLabel=document.createElement('label');nameLabel.textContent='Variable name';nameLabel.append(key);
     const valueLabel=document.createElement('label');valueLabel.textContent='Value';valueLabel.append(secret);
     const remove=document.createElement('button');remove.type='button';remove.textContent='Remove variable';
     remove.addEventListener('click',()=>{changed();row.remove();updateAdd();});
     row.append(nameLabel,valueLabel,remove);rows.append(row);
    }
    add.addEventListener('click',()=>{changed();addRow();updateAdd();});
    envForm.addEventListener('input',changed);envForm.addEventListener('change',changed);
    envForm.addEventListener('submit',async event=>{
     event.preventDefault();if(envForm.dataset.busy||version!==generation)return;
     envForm.dataset.busy='true';for(const input of envForm.querySelectorAll('input,select,button'))input.disabled=true;
     feedback.textContent='';feedback.className='environment-feedback';feedback.setAttribute('role','status');
     try{
      const values=Object.create(null),encoder=new TextEncoder();let total=0;
      for(const row of rows.children){
       const key=row.querySelector('[name=name]').value.trim(),value=row.querySelector('[name=value]').value;
       if(!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)||key.length>128||Object.hasOwn(values,key))throw new Error('Use unique variable names with letters, numbers and underscores, starting with a letter or underscore.');
       if(value.includes('\0'))throw new Error('Values cannot contain NUL bytes.');
       const bytes=encoder.encode(value).byteLength;
       if(bytes>8192)throw new Error('Each value must be at most 8192 bytes.');
       total+=encoder.encode(key).byteLength+bytes;
       if(total>32768)throw new Error('Names and values together must be at most 32 KiB.');
       values[key]=value;
      }
      const result=await api('/api/container/environments',{project:project.id,key:requestKey,label:labelInput.value.trim(),values});
      rows.replaceChildren();labelInput.value='';requestKey=crypto.randomUUID();
      if(version!==generation)return;
      feedback.textContent='Environment settings saved.';
      try{
       await refreshProject(project,role,version);if(version!==generation)return;
       const next=projectCard(project)?.querySelector('select[name="environment_id"]');if(next&&result.id)next.value=result.id;
      }catch(e){if(version===generation)feedback.textContent='Environment settings saved. Refresh status to update the settings selector.';}
     }catch(e){if(version===generation){feedback.className='environment-feedback workflow-error';feedback.setAttribute('role','alert');feedback.textContent=e.message;}}
     finally{envForm.dataset.busy='';for(const input of envForm.querySelectorAll('input,select,button'))input.disabled=false;updateAdd();}
    });
    addRow();envForm.append(rows,add,save,feedback);environments.append(envForm);
   }
   live.append(environments);
  }
 }
 const history=disclosure('Saved releases and deployment history','project-history container-history');history.open=releases.length>0;live.append(history);
 if(!releases.length){const empty=document.createElement('p');empty.textContent='Your checked images will appear here. Checking an image does not publish it.';history.append(empty);}
 for(const release of releases){
  const row=document.createElement('div'),label=document.createElement('p');
  label.textContent=(release.input?.reference||'Container image')+' · Release '+release.revision+' · Port '+release.input?.port+' · '+release.input?.health_path;
  row.append(label);
  const wasPublished=deployments.some(item=>item.release_id===release.id&&item.state==='succeeded');
  const deploy=document.createElement('button');deploy.type='button';deploy.textContent=active?.release_id===release.id?'Current release':wasPublished?'Restore release':'Deploy release';deploy.disabled=!data.available||!!pending||active?.release_id===release.id;
  const key=crypto.randomUUID();deploy.addEventListener('click',async()=>{if(version!==generation)return;deploy.disabled=true;try{await api('/api/container/deployments',{project:project.id,release:release.id,key});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation){error(e);deploy.disabled=!data.available||!!pending;}}});row.append(deploy);history.append(row);
 }
 if(deployments.length){const heading=document.createElement('h4');heading.textContent='Deployment history';history.append(heading);}
 for(const deployment of deployments){
  const row=document.createElement('div'),label=document.createElement('p');label.textContent='Deployment '+deployment.revision+' · '+deployment.state+(deployment.id===active?.id?' · Current':'')+' · '+new Date(deployment.created_at*1000).toLocaleString();row.append(label);
  if(deployment.state==='queued'){const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel deployment';cancel.addEventListener('click',async()=>{if(version!==generation)return;cancel.disabled=true;try{await api('/api/container/deployments/cancel',{project:project.id,id:deployment.id});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation){error(e);cancel.disabled=false;}}});row.append(cancel);}
  history.append(row);
 }
 trackContainerStatus(project,role,version,data);
}
async function containerProjectUploads(card,project,role,version){
 if(role==='viewer'){const note=document.createElement('p');note.className='project-status';note.textContent='Container releases and deployment controls are available to workspace owners and developers.';card.append(note);return;}
 const data=await api('/api/container?project='+encodeURIComponent(project.id));if(version!==generation)return;
 const live=document.createElement('div');live.className='node-live container-live';card.append(live);
 renderContainerLive({live,project,role,version},data);
}

async function nodeProjectUploads(card,project,role,version,result){
 if(role==='viewer'){
  const copy=document.createElement('p');copy.textContent='Node.js project status is available to owners and developers. Your saved uploads are shown below.';card.append(copy);
  return nodeUploadRows(card,project,role,version,result, null);
 }
 const node=await api('/api/node?project='+encodeURIComponent(project.id));if(version!==generation)return;
 const entry={card,project,role,version,node,uploads:result.uploads,buildKeys:new Map(),deployKeys:new Map(),uploadButtons:[],live:document.createElement('div')};entry.live.className='node-live';card.append(entry.live);nodeStatusCards.set(project.id,entry);renderNodeLive(entry,node);
 return nodeUploadRows(card,project,role,version,result,{node,builds:Array.isArray(node.builds)?node.builds:[],releases:Array.isArray(node.releases)?node.releases:[]});
}

// Project workflow state.
function projectWorkflow(kind,uploads,data){
 const latest=uploads[0];const jobs=kind==='node'?(data.deployments||[]):(data.jobs||[]);
 const builds=data.builds||[];const releases=data.releases||[];
 const active=kind==='node'?data.active:jobs.find(j=>j.id===data.active);
 const building=builds.find(nodeJobActive);const publishing=jobs.find(nodeJobActive);
 if(publishing)return {step:kind==='node'?3:2,title:'Publishing your website',text:publishing.message||'This page updates automatically. You can leave it open while we finish.',busy:true};
 if(building)return {step:2,title:'Building your website',text:building.message||'Preparing your files for hosting. Your current website stays online.',busy:true};
 if(!latest)return {step:1,title:'Add your website files',text:'Upload a ZIP to get started. We’ll guide you through publishing it.',action:'upload',label:'Upload website'};
 if(!data.available)return {step:1,title:'Waiting for a hosting assignment',text:'Your files are saved, but no hosting slot is assigned yet. This may need operator attention if capacity is full. Status updates automatically.'};
 if(kind==='node'){
  const build=builds.find(b=>b.upload_id===latest.id);
  const release=releases.find(r=>r.build_id===build?.id);
  if(release&&active?.release_id===release.build_id)return {step:4,title:'Your website is live',text:'Upload a new version when you’re ready to update it.',action:'upload',label:'Upload new version',live:true};
  const attempt=release&&jobs.find(job=>job.release_id===release.build_id);
  if(release&&attempt?.state==='failed')return {step:3,title:'Publishing needs attention',text:attempt.message||'This deployment failed. Retry the saved release or restore a previously published release from history.',action:'publish',id:release.build_id,label:'Retry publishing'};
  if(release)return {step:3,title:active?'Your update is ready':'Ready to publish',text:'The build succeeded. Publish it to make this version available on your website.',action:'publish',id:release.build_id,label:active?'Publish update':'Publish website'};
  return {step:2,title:build?.state==='failed'?'Your build needs attention':'Files uploaded · Build next',text:build?.state==='failed'?(build.message||'Check your project files, then try building again.'):'Build your Node.js project before publishing it. This does not change your live website.',action:'build',id:latest.id,label:build?.state==='failed'?'Retry build':'Build website'};
 }
 if(active?.upload_id===latest.id)return {step:3,title:'Your website is live',text:'Upload a new version when you’re ready to update it.',action:'upload',label:'Upload new version',live:true};
 const failed=jobs.find(j=>j.upload_id===latest.id)?.state==='failed';
 return {step:2,title:failed?'Publishing needs attention':active?'Your update is ready':'Ready to publish',text:failed?'The previous attempt failed. Retry publishing your uploaded files.':'Your HTML and CSS files are ready. Publish them to make your website available.',action:'publish',id:latest.id,label:failed?'Retry publishing':active?'Publish update':'Publish website'};
}
// End project workflow state.
function renderWorkflow(project,role,uploads,data,perform){
 const flow=projectWorkflow(project.kind,uploads,data);const panel=document.createElement('section');panel.className='project-workflow';panel.dataset.tone=flow.live?'live':flow.busy?'busy':'next';
 const steps=document.createElement('ol');steps.className='workflow-steps';steps.setAttribute('aria-label','Publishing progress');
 for(const [index,label] of (project.kind==='node'?['Upload','Build','Publish']:['Upload','Publish']).entries()){
  const step=document.createElement('li');step.textContent=(index+1)+'. '+label;step.className=index+1<flow.step?'complete':index+1===flow.step?'current':'';if(index+1===flow.step)step.setAttribute('aria-current','step');steps.append(step);
 }
 const title=document.createElement('h3');title.textContent=flow.title;const text=document.createElement('p');text.textContent=flow.text;panel.append(steps,title,text);
 if(flow.busy){const progress=createProgress(flow.title+'…');panel.append(progress.box);
  const pending=(project.kind==='node'?[...(data.deployments||[]),...(data.builds||[])]:data.jobs||[]).find(nodeJobActive);
  if(pending?.created_at){const started=document.createElement('p');started.className='operation-requested';started.textContent='Requested '+new Date(pending.created_at*1000).toLocaleString();panel.append(started);
   const delay=document.createElement('p');delay.className='operation-delay';delay.dataset.requestedAt=String(pending.created_at);delay.hidden=Date.now()/1000-pending.created_at<300;delay.textContent='This request has been pending for more than five minutes. It may still be running. Check its history below or contact support; avoid submitting a duplicate request.';panel.append(delay);
  }
  if(pending?.id){const reference=document.createElement('p');reference.className='operation-reference';reference.textContent='Support reference: '+pending.id;panel.append(reference);}
 }
 if(flow.action&&role!=='viewer'){
  const button=document.createElement('button');button.type='button';button.className='workflow-primary';button.textContent=flow.label;
  const note=document.createElement('p');note.className='workflow-error';note.hidden=true;note.setAttribute('role','alert');
  button.addEventListener('click',async()=>{
   if(flow.action==='upload'){const section=panel.closest('.project')?.querySelector('.upload-details');if(section){section.open=true;section.scrollIntoView({behavior:'smooth',block:'center'});section.querySelector('input[type=file]')?.focus();}return;}
   button.disabled=true;note.hidden=true;const pending=createProgress(flow.action==='build'?'Requesting build…':'Requesting publication…');panel.append(pending.box);
   try{await perform(flow);}catch(e){note.textContent=e.message||'Could not complete this action. Please try again.';note.hidden=false;}finally{pending.box.remove();button.disabled=false;}
  });panel.append(button,note);
 }
 return panel;
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
 const workflow=renderWorkflow(project,role,entry.uploads||[],node,async flow=>{
  if(version!==generation||!live.isConnected||project.deleting)return;
  const keys=flow.action==='build'?entry.buildKeys:entry.deployKeys;
  const key=keys.get(flow.id)||crypto.randomUUID();keys.set(flow.id,key);entry.mutating=true;
  try{await api(flow.action==='build'?'/api/node/builds':'/api/node/deployments',flow.action==='build'?{project:project.id,upload:flow.id,key}:{project:project.id,release:flow.id,key});if(version===generation)await refreshProject(project,role,version);}finally{entry.mutating=false;}
 });live.append(workflow);
 if(active&&node.site){
  const link=document.createElement('a');link.href=node.site;link.target='_blank';link.rel='noopener noreferrer';link.textContent=(new URL(node.site).hostname.endsWith('.sslip.io')?'Visit website':'Visit '+new URL(node.site).hostname)+' ↗';link.className='site-link';live.append(link);
 }
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Refresh status';refresh.className='refresh-status';refresh.addEventListener('click',()=>refreshProject(project,role,version).catch(error));live.append(refresh);const history=disclosure('Build & release history','project-history');history.open=historyOpen;live.append(history);
 for(const build of builds){
  const line=document.createElement('p');line.textContent='Build · '+build.state+' · '+new Date(build.created_at*1000).toLocaleString();history.append(line);
  if(role!=='viewer'&&typeof build.message==='string'&&build.message){const progress=document.createElement('p');progress.className='muted';progress.textContent=build.message;history.append(progress);}
  else if(build.state==='failed'){const hint=document.createElement('p');hint.textContent=role==='viewer'?'Build failed.':'Build failed. Review your project or contact the operator, then choose Build again.';history.append(hint);}
  if(role!=='viewer'&&build.state==='failed'){
   const details=disclosure('View build output','build-output');const output=document.createElement('pre');output.textContent='Open to load the available build output.';details.append(output);
   let loaded=false;details.addEventListener('toggle',async()=>{if(!details.open||loaded)return;loaded=true;output.textContent='Loading build output…';try{const data=await api('/api/node/build-log?project='+encodeURIComponent(project.id)+'&id='+encodeURIComponent(build.id));if(version!==generation||!details.isConnected)return;output.textContent=data.available?data.log:'No captured output is available for this build. Older builds and failures before the builder starts may not have a log.';}catch(e){loaded=false;if(version===generation&&details.isConnected)output.textContent=e.message;}});history.append(details);
  }

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
  const row=document.createElement('div');const label=document.createElement('p');label.textContent='Revision '+deployment.revision+' · '+deployment.state+(deployment.id===active?.id?' · Current':'')+' · Requested '+new Date(deployment.created_at*1000).toLocaleString();row.append(label);if(deployment.message){const explanation=document.createElement('p');explanation.className='muted';explanation.textContent=deployment.message;row.append(explanation);}
  if(role!=='viewer'&&deployment.state==='queued'){const cancel=document.createElement('button');cancel.type='button';cancel.textContent='Cancel deployment';cancel.addEventListener('click',async()=>{cancel.disabled=true;entry.mutating=true;try{await api('/api/node/deployments/cancel',{project:project.id,id:deployment.id});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);cancel.disabled=false;}finally{entry.mutating=false;}});row.append(cancel);}
  history.append(row);
 }
 entry.node=node;entry.active=!node.available||builds.some(nodeJobActive)||deployments.some(nodeJobActive);
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
   results.forEach((result,index)=>{if(!entries[index].card.isConnected||entries[index].card.dataset.deleting==='true'){nodeStatusCards.delete(entries[index].project.id);return;}if(result.status==='fulfilled')renderNodeLive(entries[index],result.value);else if(result.reason?.status===401)unauthorized=true;else{let note=entries[index].live.querySelector('.workflow-refresh-error');if(!note){note=document.createElement('p');note.className='workflow-refresh-error';entries[index].live.append(note);}note.textContent='Automatic status updates are temporarily unavailable. Use Refresh status to retry.';entries[index].snapshot=null;if(result.reason?.status===403||result.reason?.status===404)entries[index].active=false;}});
 state.inFlight=false;state.controller=null;
 if(!unauthorized&&[...nodeStatusCards.values()].some(entry=>entry.version===state.version&&entry.active))state.timer=setTimeout(()=>pollNodeStatus(state),nodeStatusInterval);else if(unauthorized)signedOut();
}
function startNodeStatusRefresh(version,workspace){
 if(version!==generation||workspace!==$('workspace').value)return;
 if([...nodeStatusCards.values()].some(entry=>entry.version===version&&entry.active)){if(nodeStatusState){nodeStatusState.stopped=true;clearTimeout(nodeStatusState.timer);nodeStatusState.controller?.abort();}const state={version,workspace,timer:null,inFlight:false,stopped:false,controller:null};nodeStatusState=state;state.timer=setTimeout(()=>pollNodeStatus(state),nodeStatusInterval);}
}

function githubRepositoryName(connection){return connection?.repository||connection?.repository_full_name||connection?.RepositoryFullName||'';}
function githubConnectionValue(connection,name){return connection?.[name]??connection?.[name.replace(/(^|_)([a-z])/g,(_,a,b)=>b.toUpperCase())]??'';}
function githubStartButton(project,details,status,container){
 const button=document.createElement('button');button.type='button';button.textContent='Connect GitHub';
 button.addEventListener('click',async()=>{button.disabled=true;status.textContent='Opening GitHub authorization…';try{const data=await api('/api/github/start',{project:project.id});if(typeof data.url!=='string'||data.url!=='https://github.com/login/oauth/authorize'&&!data.url.startsWith('https://github.com/login/oauth/authorize?'))throw new Error('GitHub authorization URL was invalid.');location.assign(data.url);}catch(e){status.textContent=e.message;button.disabled=false;}});(container||details).append(button);return button;
}
function githubRenderChooser(content,project,details,status){
 const chooser=document.createElement('div');chooser.className='github-chooser';const loading=document.createElement('p');loading.className='muted';loading.textContent='Loading repositories…';chooser.append(loading);content.append(chooser);
 api('/api/github/repositories?project='+encodeURIComponent(project.id)).then(data=>{if(!chooser.isConnected)return;chooser.replaceChildren();const repos=Array.isArray(data.repositories)?data.repositories:[];if(!repos.length){const empty=document.createElement('p');empty.className='muted';empty.textContent='No repositories are available. Install or manage the Launchstead GitHub App, then restart the connection.';chooser.append(empty);const restart=githubStartButton(project,details,status,chooser);restart.textContent='Restart GitHub connection';return;}
  const form=document.createElement('form');const repoLabel=document.createElement('label');repoLabel.textContent='Repository';const repoSelect=document.createElement('select');repoSelect.required=true;repos.forEach(repo=>{const installation=Number(repo.InstallationID??repo.installation_id??repo.installationId);const repository=Number(repo.RepositoryID??repo.repository_id??repo.repositoryId);if(!Number.isSafeInteger(installation)||installation<=0||!Number.isSafeInteger(repository)||repository<=0)return;const option=document.createElement('option');option.value=String(repository);option.dataset.installation=String(installation);option.dataset.branch=String(repo.DefaultBranch??repo.default_branch??repo.defaultBranch??'main');option.textContent=repo.RepositoryFullName||repo.repository_full_name||repo.repository||'Repository';repoSelect.append(option);});repoLabel.append(repoSelect);const branchLabel=document.createElement('label');branchLabel.textContent='Branch';const branch=document.createElement('input');branch.required=true;branch.value=repoSelect.selectedOptions[0]?.dataset.branch||'main';branchLabel.append(branch);const directoryLabel=document.createElement('label');directoryLabel.textContent='Directory (optional)';const directory=document.createElement('input');directory.placeholder='/';directoryLabel.append(directory);const connect=document.createElement('button');connect.type='submit';connect.textContent='Connect repository';form.append(repoLabel,branchLabel,directoryLabel,connect);repoSelect.addEventListener('change',()=>{branch.value=repoSelect.selectedOptions[0]?.dataset.branch||'main';});form.addEventListener('submit',async event=>{event.preventDefault();connect.disabled=true;status.textContent='Saving GitHub connection…';const selected=repoSelect.selectedOptions[0];try{if(!selected)throw new Error('No valid GitHub repositories are available.');await api('/api/github/connect',{project:project.id,installation_id:Number(selected.dataset.installation),repository_id:Number(selected.value),branch:branch.value.trim(),directory:directory.value.trim()});if(chooser.isConnected)await renderGithubContent(content,project,details,status);}catch(e){status.textContent=e.message;connect.disabled=false;}});chooser.append(form);
 }).catch(e=>{if(!chooser.isConnected)return;chooser.replaceChildren();const message=document.createElement('p');message.className='muted';message.textContent=e.message+' Install or manage the Launchstead GitHub App, then restart the connection.';chooser.append(message);const restart=githubStartButton(project,details,status,chooser);restart.textContent='Restart GitHub connection';});
}
async function renderGithubContent(content,project,details,status,choose){
 const renderToken=String(Number(details.dataset.githubRenderToken||0)+1);details.dataset.githubRenderToken=renderToken;
 const current=()=>details.isConnected&&details.dataset.githubRenderToken===renderToken;
 content.replaceChildren();status.textContent='Loading GitHub connection…';
 try{const data=await api('/api/github/connection?project='+encodeURIComponent(project.id));if(!current())return;const connection=data.connection;const pending=data.pending===true;status.textContent='';
  if(connection?.connected===true){const heading=document.createElement('strong');heading.textContent='Repository connected';const repo=document.createElement('p');repo.className='github-repository';repo.textContent=githubRepositoryName(connection);const meta=document.createElement('p');meta.className='muted';meta.textContent='Branch: '+(githubConnectionValue(connection,'branch')||'default')+(githubConnectionValue(connection,'directory')?' · Directory: '+githubConnectionValue(connection,'directory'):'');const note=document.createElement('p');note.className='github-note';note.textContent='Repository connected. GitHub publishing is not enabled yet; ZIP uploads still work.';const disconnect=document.createElement('button');disconnect.type='button';disconnect.className='button-danger';disconnect.textContent='Disconnect';disconnect.addEventListener('click',async()=>{if(!confirm('Disconnect this GitHub repository from '+project.name+'?'))return;disconnect.disabled=true;try{await api('/api/github/disconnect',{project:project.id});if(content.isConnected)await renderGithubContent(content,project,details,status);}catch(e){status.textContent=e.message;disconnect.disabled=false;}});content.append(heading,repo,meta,note,disconnect);status.textContent='';return;}
  if(pending){const note=document.createElement('p');note.className='muted';note.textContent='GitHub authorization completed. Choose a repository to finish connecting GitHub.';content.append(note);githubRenderChooser(content,project,details,status);return;}
  const note=document.createElement('p');note.className='muted';note.textContent='Connect a repository to keep its source available alongside your ZIP uploads.';content.append(note);const restart=githubStartButton(project,details,status,content);restart.textContent='Connect GitHub';
 }catch(e){if(!current())return;status.textContent=e.message;const retry=githubStartButton(project,details,status,content);retry.textContent='Restart GitHub connection';}
}
function renderProjectGithub(card,project,role,version){
 if(!githubConnections||role!=='owner'||project.deleting||version!==generation||!['static','node'].includes(project.kind))return;
 const existing=card.querySelector('.project-github');if(existing)return;const details=disclosure('GitHub connection','project-github');const status=document.createElement('p');status.className='github-status muted';status.setAttribute('role','status');const content=document.createElement('div');content.className='github-content';details.append(status,content);card.append(details);details.addEventListener('toggle',()=>{if(details.open&&!details.dataset.loaded){details.dataset.loaded='true';renderGithubContent(content,project,details,status);}});if(details.open)renderGithubContent(content,project,details,status);
}
async function completeGithubCallback(){
 if(githubCallbackHandled)return;
 if(!githubCallbackCode&&!githubCallbackState&&!githubCallbackError)return;
 githubCallbackHandled=true;
 if(githubCallbackError)throw new Error('GitHub authorization was cancelled or expired. Reopen the GitHub connection and try again.');
 if(!githubCallbackCode||!githubCallbackState)throw new Error('GitHub authorization response was incomplete. Reopen the GitHub connection and try again.');
 const code=githubCallbackCode,state=githubCallbackState;githubCallbackCode='';githubCallbackState='';const data=await api('/api/github/callback',{code,state});const project=typeof data.project==='string'?data.project:data.project?.id;const callbackWorkspace=data.workspace||data.flow?.attempt?.WorkspaceID||data.flow?.attempt?.workspace_id||data.flow?.attempt?.workspaceId;if(!project)throw new Error('GitHub authorization completed without a project.');if(callbackWorkspace&&workspaces.some(workspace=>workspace.id===callbackWorkspace)&&$('workspace').value!==callbackWorkspace){$('workspace').value=callbackWorkspace;selectedWebsite='';await loadProjects();}showWebsite(project);setTimeout(()=>{const card=projectCard({id:project});const github=card?.querySelector('.project-github');if(github){github.open=true;github.dataset.loaded='true';github.querySelector('.github-content')&&renderGithubContent(github.querySelector('.github-content'),{id:project},github,github.querySelector('.github-status'),true);}},0);
}

function nodeUploadRows(card,project,role,version,result,state){
 const copy=document.createElement('p');copy.textContent='ZIP up to 10 MiB. Include package.json with a start script and package-lock.json at the root. Customize index.html in the starter, then ZIP the source files and upload, build, and deploy. Omit node_modules and secrets.';card.append(copy);
 if(role!=='viewer'){
  const starter=document.createElement('a');starter.href='/examples/node-website.zip';starter.download='node-website.zip';starter.textContent='Download a starter website';card.append(starter);
  const form=document.createElement('form');const label=document.createElement('label');label.textContent='Project ZIP';const input=document.createElement('input');input.type='file';input.accept='.zip,application/zip';input.required=true;label.append(input);const button=document.createElement('button');button.textContent='Upload ZIP';form.append(label,button);card.append(form);
  form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{const file=input.files[0];if(!file||file.size>10*1024*1024)throw new Error('Select a ZIP no larger than 10 MiB.');await uploadWithProgress(project,file,form,version);input.value='';if(version===generation)await refreshProject(project,role,version);});});
 }
 for(const upload of result.uploads){const row=document.createElement('div');const text=document.createElement('p');text.textContent='Validated · '+upload.files+(upload.files===1?' file · ':' files · ')+(upload.compressed_bytes/1024).toFixed(1)+' KiB · '+new Date(upload.created_at*1000).toLocaleString();row.append(text);
  if(state&&role!=='viewer'){
   const build=state.builds.find(item=>item.upload_id===upload.id);const saved=build&&state.releases.some(release=>release.build_id===build.id);const busy=state.builds.some(item=>item.state==='queued'||item.state==='running');const button=document.createElement('button');button.type='button';button.textContent=saved?'Built':build&&build.state==='queued'?'Build queued':build&&build.state==='running'?'Building':build?'Build again':'Build';button.disabled=busy||!!saved||!state.node.available;nodeStatusCards.get(project.id).uploadButtons.push({button,upload});const requestKey=crypto.randomUUID();button.addEventListener('click',async()=>{button.disabled=true;const entry=nodeStatusCards.get(project.id);entry.mutating=true;try{await api('/api/node/builds',{project:project.id,upload:upload.id,key:requestKey});if(version===generation)await refreshProject(project,role,version);}catch(e){if(version===generation)error(e);button.disabled=false;}finally{entry.mutating=false;}});row.append(button);
  }
  uploadDownloadControl(row,upload,project,role,version);uploadDeleteControl(row,upload,project,role,version);card.append(row);
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

// Portfolio controls only change visibility; deployment state remains server-owned.
function setWebsiteText(element,text){if(element.textContent!==text)element.textContent=text;}
function showWebsite(id){
 selectedWebsite=id;filterWebsiteCards();
 const target=id?projectCard({id}):$('website-search');
 if(target){if(id)target.setAttribute('tabindex','-1');target.focus({preventScroll:true});target.scrollIntoView({block:'start',behavior:'auto'});}
}
function updateWebsiteSummary(card){
 let summary=card.querySelector(':scope > .website-summary');
 if(!summary){
  summary=document.createElement('div');summary.className='website-summary';
  const status=document.createElement('p');status.className='website-summary-status';
  const address=document.createElement('p');address.className='website-summary-address';
  const manage=document.createElement('button');manage.type='button';manage.className='button button-dark';manage.textContent='Manage website';manage.addEventListener('click',()=>showWebsite(card.dataset.projectId));
  summary.append(status,address,manage);card.querySelector('.project-heading')?.after(summary);
 }
 const workflow=card.querySelector('.project-workflow');
 setWebsiteText(summary.querySelector('.website-summary-status'),workflow?.querySelector('h3')?.textContent||card.querySelector('.project-status')?.textContent||'Loading website status…');
 const link=card.querySelector('.site-link');
 let address='No published address yet';
 if(link){try{address=new URL(link.href).hostname;}catch{}}
 setWebsiteText(summary.querySelector('.website-summary-address'),address);
 const tone=workflow?.dataset.tone||'idle';if(summary.dataset.tone!==tone)summary.dataset.tone=tone;
 const name=card.querySelector('.project-name')?.textContent||'website';
 summary.querySelector('button').setAttribute('aria-label','Manage '+name);
 let links=card.querySelector(':scope > .website-detail-links');
 if(!links){
  links=document.createElement('nav');links.className='website-detail-links';links.setAttribute('aria-label','Website sections');
  for(const [label,selector] of [['Publishing','.project-workflow'],['Logs','.project-runtime-logs'],['Domains','.project-domains'],['GitHub','.project-github'],['Clients','.project-clients'],['Settings','.project-rename']]){
   const button=document.createElement('button');button.type='button';button.textContent=label;button.dataset.target=selector;
   button.addEventListener('click',()=>{const section=card.querySelector(selector);if(!section)return;if(section.tagName==='DETAILS')section.open=true;section.setAttribute('tabindex','-1');section.focus({preventScroll:true});section.scrollIntoView({block:'start',behavior:'auto'});});links.append(button);
  }
  summary.after(links);
 }
 for(const button of links.children)button.hidden=!card.querySelector(button.dataset.target);
}
function filterWebsiteCards(){
 const query=$('website-search').value.trim().toLocaleLowerCase();
 const kind=$('website-type-filter').value;
 const cards=Array.from($('projects').querySelectorAll(':scope > .project[data-project-id]'));
 let shown=0;
 for(const card of cards){
  updateWebsiteSummary(card);
  const matches=(!query||(card.querySelector('.project-name')?.textContent||'').toLocaleLowerCase().includes(query))&&(kind==='all'||card.dataset.projectKind===kind);
  card.hidden=selectedWebsite?card.dataset.projectId!==selectedWebsite:!matches;
  card.classList.toggle('website-detail-active',card.dataset.projectId===selectedWebsite);
  if(matches)shown++;
 }
 const detail=!!selectedWebsite;
 $('projects-panel').classList.toggle('showing-website',detail);
 $('website-detail-toolbar').hidden=!detail;
 setWebsiteText($('website-count'),cards.length?shown+' of '+cards.length+' websites':'');
 $('website-no-results').hidden=detail||!cards.length||shown!==0;
}
$('back-to-websites').addEventListener('click',()=>showWebsite(''));
$('website-search').addEventListener('input',filterWebsiteCards);
$('website-type-filter').addEventListener('change',filterWebsiteCards);
new MutationObserver(filterWebsiteCards).observe($('projects'),{childList:true,subtree:true,characterData:true});
(function configureAppearance(){
 const input=$('theme-choice'),media=window.matchMedia('(prefers-color-scheme: dark)');
 let preference='system';
 try{const stored=localStorage.getItem('launchstead-theme');if(['system','light','dark'].includes(stored))preference=stored;}catch{}
 function apply(){document.documentElement.dataset.theme=preference==='system'?(media.matches?'dark':'light'):preference;input.value=preference;}
 input.addEventListener('change',()=>{preference=input.value;try{localStorage.setItem('launchstead-theme',preference);}catch{}apply();});
 media.addEventListener('change',apply);apply();
})();

function renderProjectAvailability(value){
 projectAvailability=value;
 const notice=$('project-availability');
 if(!value){notice.textContent='Website allowance could not be loaded. Creation will check availability.';return;}
 const usage=typeof value.limit==='number'?value.used+' of '+value.limit+' workspace websites used. ':value.used+' workspace websites. ';
 notice.textContent=usage+value.message;
 for(const option of $('project-kind').options){option.disabled=value[option.value]===false;option.hidden=option.value==='container'&&!containerHosting;}
 if($('project-kind').selectedOptions[0]?.disabled){const next=[...$('project-kind').options].find(option=>!option.disabled);if(next)$('project-kind').value=next.value;}
 $('project-form').querySelector('button[type=submit],button').disabled=!value.static&&!value.node&&!value.container;
}

$('new-project-details').addEventListener('toggle',async()=>{
 if(!$('new-project-details').open)return;
 const version=generation,workspace=$('workspace').value;if(!workspace)return;
 try{const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version===generation)renderProjectAvailability(data.availability);}catch(e){if(version===generation)error(e);}
});

// Elapsed time is not a failure signal. Update the waiting notice independently
// of status snapshots, which may remain unchanged while a worker is busy.
setInterval(()=>{if(document.hidden||$('workspace-view').hidden)return;updateContainerWaitLabels();for(const note of document.querySelectorAll('.operation-delay'))note.hidden=Date.now()/1000-Number(note.dataset.requestedAt)<300;},30000);

function renderRuntimeLogs(card,project,role,version){
 if(role==='viewer')return;
 const details=disclosure('Runtime output','project-runtime-logs');
 const copy=document.createElement('p');copy.textContent='Recent output from your deployed website. Common credential patterns are redacted, but avoid printing secrets. This is a snapshot, not a live stream.';
 const refresh=document.createElement('button');refresh.type='button';refresh.textContent='Load recent output';
 const status=document.createElement('p');status.setAttribute('role','status');
 const output=document.createElement('pre');output.textContent='No output loaded yet.';
 refresh.addEventListener('click',async()=>{refresh.disabled=true;status.textContent='Reading runtime output…';try{const data=await api('/api/runtime-logs?project='+encodeURIComponent(project.id));if(version!==generation||!details.isConnected)return;output.textContent=data.log||'No recent output was returned.';status.textContent='Fetched '+new Date(data.fetched_at*1000).toLocaleString();refresh.textContent='Refresh output';}catch(e){if(version===generation&&details.isConnected){output.textContent='';status.textContent=e.message;}}finally{refresh.disabled=false;}});
 details.append(copy,refresh,status,output);card.append(details);
}

// Client review is separate from workspace membership and project management.
function renderProjectClients(card,project,role,version){
 if(role!=='owner'||project.deleting||version!==generation)return;
 const details=disclosure('Client access','project-clients');
 const copy=document.createElement('p');copy.textContent='Share this website’s name, publication status, and connected address. Clients cannot edit it or see files, logs, billing, or other websites. Team membership gives broader access and is managed separately.';
 const note=document.createElement('p');note.className='muted';note.textContent=clientInvitations?'New recipients must be approved by the operator before you send an invitation. They then register, verify their email, and accept the email invitation for site-only read-only access. Existing verified accounts can receive access without registering. Maximum 20 clients per website.':'The client must already have a verified Launchstead account. Registration is currently limited to approved people. Maximum 20 clients per website.';
 const list=document.createElement('div');const status=document.createElement('p');status.setAttribute('role','status');
 const form=document.createElement('form');const label=document.createElement('label');label.textContent='Existing verified account (no email)';const input=document.createElement('input');input.type='email';input.required=true;input.maxLength=254;input.autocomplete='email';label.append(input);
 const grant=document.createElement('button');grant.type='submit';grant.textContent='Give read-only access';form.append(label,grant);form.className='client-direct-grant-form';
 const inviteList=document.createElement('div');inviteList.className='client-invitations';
 const inviteForm=document.createElement('form');inviteForm.className='client-invite-form';const inviteLabel=document.createElement('label');inviteLabel.textContent='Invite client by email';const inviteInput=document.createElement('input');inviteInput.type='email';inviteInput.required=true;inviteInput.maxLength=254;inviteInput.autocomplete='email';inviteLabel.append(inviteInput);const inviteButton=document.createElement('button');inviteButton.type='submit';inviteButton.textContent='Send invitation';inviteForm.append(inviteLabel,inviteButton);
 const refresh=document.createElement('button');refresh.type='button';refresh.className='button-quiet';refresh.textContent='Refresh client list';
 let listRequest=0;
 const current=()=>version===generation&&details.isConnected&&card.dataset.deleting!=='true';
 async function load(){
  const request=++listRequest;
  status.textContent='Loading client access…';
  const data=await api('/api/project-clients?project='+encodeURIComponent(project.id));if(!current()||request!==listRequest)return;
   list.replaceChildren();status.textContent=data.clients.length?'':'No clients have access to this website.';
  for(const client of data.clients){
   const row=document.createElement('div');row.className='client-access-row';const email=document.createElement('span');email.textContent=client.email;
   const remove=document.createElement('button');remove.type='button';remove.className='button-quiet';remove.textContent='Remove access';remove.setAttribute('aria-label','Remove access for '+client.email);
   remove.addEventListener('click',async()=>{remove.disabled=true;try{await api('/api/project-clients',{project:project.id,email:client.email,grant:false});if(current())await load();}catch(e){if(current())status.textContent=e.message;}finally{remove.disabled=false;}});
   row.append(email,remove);list.append(row);
  }
  if(clientInvitations){
   inviteList.replaceChildren();const invites=await api('/api/client-invitations?project='+encodeURIComponent(project.id));if(!current()||request!==listRequest)return;
   if(!(invites.invitations||[]).length){const empty=document.createElement('p');empty.className='muted';empty.textContent='No pending invitations. Email delivery is queued when an invitation is sent.';inviteList.append(empty);}
   for(const invite of invites.invitations||[]){const row=document.createElement('div');row.className='client-access-row';const text=document.createElement('span');text.textContent=invite.email+' · pending · expires '+new Date(invite.expires_at*1000).toLocaleDateString();const revoke=document.createElement('button');revoke.type='button';revoke.className='button-quiet';revoke.textContent='Revoke invitation';revoke.addEventListener('click',async()=>{revoke.disabled=true;try{await api('/api/client-invitations/revoke',{project:project.id,id:invite.id});if(current())await load();}catch(e){if(current())status.textContent=e.message;}finally{revoke.disabled=false;}});row.append(text,revoke);inviteList.append(row);}
  }
 }
 form.addEventListener('submit',event=>{event.preventDefault();submit(form,async()=>{await api('/api/project-clients',{project:project.id,email:input.value.trim(),grant:true});if(current()){input.value='';await load();}});});
 inviteForm.addEventListener('submit',event=>{event.preventDefault();submit(inviteForm,async()=>{await api('/api/client-invitations',{project:project.id,email:inviteInput.value.trim()});if(current()){inviteInput.value='';await load();}});});
 refresh.addEventListener('click',()=>load().catch(e=>{if(current())status.textContent=e.message;}));
 details.addEventListener('toggle',()=>{if(details.open)load().catch(e=>{if(current())status.textContent=e.message;});});
 details.append(copy,note);if(clientInvitations)details.append(inviteForm,inviteList);details.append(form,status,list,refresh);card.append(details);
}
let sharedRequest=0;
async function loadSharedWebsites(){
 const request=++sharedRequest;const session=csrf;const content=$('shared-websites');const status=$('shared-status');
 content.replaceChildren();status.textContent='Loading shared websites…';
 try{
  const data=await api('/api/shared-websites');if(request!==sharedRequest||session!==csrf)return;
  status.textContent=data.websites.length?'Read-only access. Contact the website owner to request changes.':'No websites have been shared with this account.';
  for(const site of data.websites){
   const card=document.createElement('article');card.className='shared-website';const title=document.createElement('h3');title.textContent=site.name;
   const state=document.createElement('p');state.textContent=(site.kind==='node'?'Node.js application':'Static website')+' · '+(site.published?'A version has been published':'Not published yet');card.append(title,state);
   // Hostname only, never render arbitrary URLs or markup from a response.
   if(typeof site.domain==='string'&&/^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(site.domain)){
    const link=document.createElement('a');link.href='https://'+site.domain;link.textContent='Open '+site.domain;link.target='_blank';link.rel='noopener noreferrer';card.append(link);
   }else{const note=document.createElement('p');note.className='muted';note.textContent='No connected website address is ready yet.';card.append(note);}
   content.append(card);
  }
  return data.websites.length;
 }catch(e){if(request===sharedRequest&&session===csrf){if(e.status===401)signedOut();else status.textContent=e.message;}}
}
$('shared-refresh').addEventListener('click',loadSharedWebsites);
