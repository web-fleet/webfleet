'use strict';
const state={csrf:''};
const $=(s)=>document.querySelector(s);
async function api(path,opts={}){const headers={'Content-Type':'application/json',...(opts.headers||{})};if(state.csrf&&opts.method&&opts.method!=='GET')headers['X-CSRF-Token']=state.csrf;const r=await fetch(path,{credentials:'same-origin',...opts,headers});let body={};try{body=await r.json()}catch{}if(!r.ok)throw new Error(body.error||`Request failed (${r.status})`);return body}
function showDashboard(session){state.csrf=session.csrf;$('#auth-stage').hidden=true;$('#dashboard').hidden=false}
async function boot(){try{showDashboard(await api('/api/session'));return}catch{}const s=await api('/api/setup/status');$('#auth-form').hidden=false;$('#auth-title').textContent=s.needs_setup?'Create your first admin':'Welcome back';$('#auth-copy').textContent=s.needs_setup?'This account owns the initial local Web Fleet installation.':'Sign in to view your fleet.';$('#auth-submit').textContent=s.needs_setup?'Create admin':'Sign in';$('#auth-form').dataset.mode=s.needs_setup?'setup':'login';$('#password').autocomplete=s.needs_setup?'new-password':'current-password'}
$('#auth-form').addEventListener('submit',async(e)=>{e.preventDefault();$('#auth-error').textContent='';try{const path=e.currentTarget.dataset.mode==='setup'?'/api/setup':'/api/login';showDashboard(await api(path,{method:'POST',body:JSON.stringify({email:$('#email').value,password:$('#password').value})}))}catch(err){$('#auth-error').textContent=err.message}});
$('#logout').addEventListener('click',async()=>{try{await api('/api/logout',{method:'POST'})}finally{location.reload()}});
boot().catch(e=>{$('#auth-title').textContent='Web Fleet could not start';$('#auth-copy').textContent=e.message});
