'use strict';
const $=id=>document.getElementById(id);
let csrf='',workspaces=[],generation=0,flow='';
const fragment=new URLSearchParams(location.hash.slice(1));
let actionToken=fragment.get('verify')||fragment.get('reset')||'';
const initialFlow=fragment.has('verify')?'verify':fragment.has('reset')?'reset':'';
if(location.hash)history.replaceState(null,'',location.pathname+location.search);
async function api(path,body){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined});const data=await response.json();if(!response.ok){const error=new Error(data.error||'Request failed');error.status=response.status;throw error;}return data;}
function error(e){$('error').textContent=e.message;$('error').hidden=false;}
function signedOut(){generation++;csrf='';workspaces=[];$('workspace-view').hidden=true;$('logout').hidden=true;$('login').hidden=false;$('account-flow').hidden=true;$('projects').replaceChildren();}
async function loadProjects(){const version=++generation;const workspace=$('workspace').value;const selected=workspaces.find(w=>w.id===workspace);$('project-form').hidden=!selected||selected.role==='viewer';$('projects').replaceChildren();if(!workspace)return;const data=await api('/api/projects?workspace='+encodeURIComponent(workspace));if(version!==generation)return;for(const project of data.projects){const card=document.createElement('div');card.className='project';const name=document.createElement('strong');name.textContent=project.name;const kind=document.createElement('span');kind.textContent=(project.kind==='node'?'Node.js':'Static website')+' · Awaiting upload support';card.append(name,kind);$('projects').append(card);}if(!data.projects.length)$('projects').textContent='No projects yet. Create your first project above.';}
async function loadSession(){const data=await api('/api/session');csrf=data.csrf;workspaces=data.workspaces;$('account').textContent=data.account.email;$('workspace').replaceChildren();for(const workspace of workspaces){const option=document.createElement('option');option.value=workspace.id;option.textContent=workspace.name+' · '+workspace.role;$('workspace').append(option);}$('login').hidden=true;$('workspace-view').hidden=false;$('logout').hidden=false;await loadProjects();}
async function submit(form,fn){$('error').hidden=true;const button=form.querySelector('button');button.disabled=true;try{await fn();}catch(e){error(e);}finally{button.disabled=false;}}
$('login-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{try{await api('/api/login',{email:$('email').value,password:$('password').value});}finally{$('password').value='';}await loadSession();});});
$('project-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{await api('/api/projects',{workspace:$('workspace').value,name:$('project-name').value,kind:$('project-kind').value});$('project-name').value='';await loadProjects();});});
$('workspace').addEventListener('change',()=>loadProjects().catch(error));
$('logout').addEventListener('click',async()=>{try{await api('/api/logout',{});signedOut();}catch(e){error(e);}});
function showFlow(kind){
 flow=kind;$('error').hidden=true;$('login').hidden=true;$('workspace-view').hidden=true;$('account-flow').hidden=false;$('account-form').hidden=false;
 const titles={register:'Create your account',forgot:'Reset your password',reset:'Choose a new password',verify:'Verify your email'};
 $('flow-title').textContent=titles[kind];$('flow-copy').textContent=kind==='verify'?'Confirm that you want to verify this email address.':kind==='register'?'Create a workspace for your websites. Use a password of at least 12 characters.':kind==='reset'?'Use a password of at least 12 characters. Existing sessions will be signed out.':'Enter your account email to request a reset link.';
 for(const [name,visible] of [['email',kind==='register'||kind==='forgot'],['password',kind==='register'||kind==='reset'],['workspace',kind==='register']]){ $('flow-'+name+'-label').hidden=!visible;$('flow-'+name).required=visible;$('flow-'+name).value=''; }
 $('flow-submit').textContent=kind==='verify'?'Verify email':kind==='reset'?'Change password':kind==='register'?'Create account':'Send reset link';
}
$('open-signup').addEventListener('click',()=>showFlow('register'));
$('open-forgot').addEventListener('click',()=>showFlow('forgot'));
$('back-login').addEventListener('click',()=>{actionToken='';$('error').hidden=true;$('account-flow').hidden=true;$('login').hidden=false;});
$('account-form').addEventListener('submit',event=>{event.preventDefault();submit(event.currentTarget,async()=>{
 let path,body;
 if(flow==='register'){path='/api/register';body={email:$('flow-email').value,password:$('flow-password').value,workspace:$('flow-workspace').value};}
 if(flow==='forgot'){path='/api/password/forgot';body={email:$('flow-email').value};}
 if(flow==='verify'){path='/api/verify';body={token:actionToken};}
 if(flow==='reset'){path='/api/password/reset';body={token:actionToken,password:$('flow-password').value};}
 const data=await api(path,body);$('flow-password').value='';$('account-form').hidden=true;$('flow-copy').textContent=data.message;if(flow==='reset'||flow==='verify')actionToken='';
 });});
async function initialize(){
 const config=await api('/api/config');$('open-signup').hidden=!config.signup;$('open-forgot').hidden=!config.account_mail;$('registration-note').textContent=config.signup?'Verify your email before signing in.':'Registration is closed.';
 if(initialFlow){if(!config.account_mail)throw new Error('Account recovery is unavailable. Contact the operator.');showFlow(initialFlow);return;}
 try{await loadSession();}catch(e){signedOut();if(e.status!==401)throw e;}
}
initialize().catch(error);

window.addEventListener('hashchange',()=>{
 const params=new URLSearchParams(location.hash.slice(1));
 const kind=params.has('verify')?'verify':params.has('reset')?'reset':'';
 if(!kind)return;actionToken=params.get(kind)||'';history.replaceState(null,'',location.pathname+location.search);showFlow(kind);
});
