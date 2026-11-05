const $=s=>document.querySelector(s);
const labels={backlog:'Backlog',in_progress:'In progress',blocked:'Blocked',done:'Done'};
let requests=[],requestSelected=null,queuesView=false;
let tasks=[],projects=[],queues=[],selected=null,memory=false,busy=false,connected=false,admin=false;
let projectId=localStorage.getItem('featherkan-project')||'default',queueId='',createKind='project',refreshGeneration=0;
let actor=localStorage.getItem('board-actor')||'',token=sessionStorage.getItem('board-token')||'';
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const date=s=>new Date(s).toLocaleString();
function message(text){$('#message').textContent=text;$('#message').hidden=!text}
async function api(path,body){const r=await fetch(path,{headers:{'Authorization':token?'Bearer '+token:'','Content-Type':'application/json'},...(body?{method:'POST',body:JSON.stringify(body)}:{})});const data=await r.json();if(!r.ok)throw Error(data.error||'Request failed');return data}
async function refresh(){
 const generation=++refreshGeneration;
 try {
  const [config,projectList]=await Promise.all([api('/api/config'),api('/api/projects')]);
  if(generation!==refreshGeneration)return;
  projects=projectList;admin=config.admin;
  if(!projects.some(p=>p.id===projectId)){projectId='default';queueId=''}
  const scope=projectId;
  const selectedQueue=queueId;
  const [taskList,queueList,requestList]=await Promise.all([api('/api/tasks?projectId='+encodeURIComponent(scope)),api('/api/queues?projectId='+encodeURIComponent(scope)),selectedQueue?api('/api/requests?projectId='+encodeURIComponent(scope)+'&queueId='+encodeURIComponent(selectedQueue)):Promise.resolve([])]);
  if(generation!==refreshGeneration||scope!==projectId)return;
  tasks=taskList;queues=queueList;requests=requestList;
  if(queueId&&!queues.some(q=>q.id===queueId))queueId='';
  $('#project-select').innerHTML=projects.map(p=>`<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('');$('#project-select').value=projectId;
  $('#queue-filter').innerHTML='<option value="">Choose a queue</option>'+queues.map(q=>`<option value="${esc(q.id)}">${esc(q.name)}</option>`).join('');$('#queue-filter').value=queueId;
  $('#new-project').hidden=!admin;$('#new-queue').hidden=!admin;$('#access-role').textContent=admin?'Administrator':'Member';
  connected=true;$('#sync').textContent='Connected';$('#sync-dot').className='online';message('');render();
  if(selected){const latest=tasks.find(t=>t.id===selected.id);$('#changed').hidden=!latest||latest.revision===selected.revision}
  $('#mcp-indicator').textContent=config.mcp?' · MCP enabled':'';
  $('#mcp-help').textContent=(config.mcp?'Agent MCP: '+location.origin+'/mcp. ':'MCP is off; use HTTP API or start with -mcp. ')+(config.adminConfigured?'Use the separate admin token for administrative access.':'Set BOARD_ADMIN_TOKEN on the server to enable administrative access.');
 }catch(e){if(generation!==refreshGeneration)return;connected=false;$('#sync').textContent='Disconnected';$('#sync-dot').className='';message(e.message)}
}
function render(){
 $('#board').hidden=queuesView;$('#request-list').hidden=!queuesView;$('#queue-controls').hidden=!queuesView;$('#task-filters').hidden=queuesView;$('#new-task').hidden=queuesView;$('#queues-tab').classList.toggle('active',queuesView);

 const names=[...new Set(tasks.map(t=>t.assignee).filter(Boolean))].sort();const previous=$('#filter-assignee').value;$('#filter-assignee').innerHTML='<option value="">All assignees</option><option value="__none">Unassigned</option>'+names.map(n=>`<option value="${esc(n)}">${esc(n)}</option>`).join('');$('#filter-assignee').value=previous;$('#agents').innerHTML=names.map(n=>`<option value="${esc(n)}">`).join('');
 const q=$('#search').value.toLowerCase(),assignee=$('#filter-assignee').value;const filtered=tasks.filter(t=>(!q||JSON.stringify(t).toLowerCase().includes(q))&&(!assignee||(assignee==='__none'?!t.assignee:t.assignee===assignee)));
 const scoped=tasks;$('#done-count').textContent=tasks.filter(t=>t.status==='done').length;const queue=queues.find(q=>q.id===queueId);$('#claim-next').hidden=!queue;$('#new-request').hidden=!queue;$('#queue-help').textContent=queuesView?(queue?queue.description+' · '+requests.filter(r=>r.status==='active').length+'/'+queue.maxInFlight+' active · '+requests.filter(r=>r.status==='pending').length+' waiting':'Choose or create a queue for supervisor requests.'):(projects.find(p=>p.id===projectId)?.description||'');$('#board').classList.toggle('memory',memory);$('#board-tab').classList.toggle('active',!queuesView&&!memory);$('#memory-tab').classList.toggle('active',!queuesView&&memory);
 $('#request-list').innerHTML=requests.map(r=>`<button class="request-card" data-request="${esc(r.id)}"><span><strong>${esc(r.id.slice(0,6).toUpperCase())}</strong> · ${esc(r.status)} · ${esc(r.claimedBy||'Unclaimed')}</span><pre>${esc(JSON.stringify(r.payload,null,2))}</pre><span class="hint">Submitted by ${esc(r.createdBy)} · ${esc(date(r.createdAt))}</span></button>`).join('')||'<p class="hint">No requests in this queue.</p>';
 document.querySelectorAll('[data-request]').forEach(el=>el.onclick=()=>openRequest(requests.find(r=>r.id===el.dataset.request)));
 $('#board').innerHTML=(memory?['done']:Object.keys(labels)).map(status=>{const list=filtered.filter(t=>t.status===status).sort((a,b)=>b.updatedAt.localeCompare(a.updatedAt));return `<section class="column ${status}"><div class="column-head"><h2><span class="status-dot"></span>${labels[status]} <span class="badge">${list.length}</span></h2><button class="add-column quiet" data-status="${status}" aria-label="New task in ${labels[status]}">＋</button></div><div class="cards">${list.map(t=>`<button class="card" data-id="${esc(t.id)}"><div class="card-top"><span class="task-code">${esc(t.id.slice(0,6).toUpperCase())}</span><span class="note-count">${t.comments.length?'☰ '+t.comments.length:''}</span></div><h3>${esc(t.title)}</h3>${t.description?`<p class="card-description">${esc(t.description)}</p>`:''}<div class="card-footer"><span class="owner ${t.assignee?'':'unassigned'}"><span class="avatar">${t.assignee?esc(t.assignee.slice(0,2).toUpperCase()):'—'}</span>${esc(t.assignee||'Unassigned')}</span><span class="revision">v${t.revision}</span></div></button>`).join('')||`<div class="empty"><span>${status==='done'?'✓':'◇'}</span><p>${q||assignee?'No matching tasks':status==='backlog'?'Ready for the next idea':status==='in_progress'?'No work claimed yet':status==='blocked'?'No blockers right now':'Completed work lives here'}</p></div>`}</div></section>`}).join('');
 $('#count').textContent=queuesView?`${requests.length} requests`:`${scoped.length} tasks · ${scoped.filter(t=>t.status==='in_progress').length} in progress · ${scoped.filter(t=>t.status==='blocked').length} blocked`;
 document.querySelectorAll('.card').forEach(el=>el.onclick=()=>openTask(tasks.find(t=>t.id===el.dataset.id)));
 document.querySelectorAll('.add-column').forEach(el=>el.onclick=()=>openTask(null,el.dataset.status));
}
function openTask(t,status='backlog'){
 if(!actor){settings();return} selected=t?structuredClone(t):null;$('#title').value=t?.title||'';$('#assignee').value=t?.assignee||'';$('#description').value=t?.description||'';$('#status').value=t?.status||status;$('#admin-note').hidden=!admin;
 $('#dialog-title').textContent=t?'Task details':'Create a task';$('#task-id').textContent=t?'TASK '+t.id.slice(0,6).toUpperCase():'NEW TASK';$('#save-task').textContent=t?'Save changes':'Create task';$('#revision').textContent=t?'Revision '+t.revision:'';
 $('#task-context').hidden=!t;$('#claim-task').hidden=!t||t.status==='done'||!!(!admin&&t.assignee&&t.assignee!==actor);$('#release-task').hidden=!t||(!admin&&t.assignee!==actor);$('#task-error').hidden=true;$('#changed').hidden=true;$('#comment').value='';renderContext();if(!$('#task-dialog').open)$('#task-dialog').showModal();
}
function renderContext(){if(!selected)return;$('#comment-count').textContent=selected.comments.length;$('#comments').innerHTML=selected.comments.map(c=>`<article class="comment"><div><strong>${esc(c.author)}</strong><time>${esc(date(c.at))}</time></div><p>${esc(c.text)}</p></article>`).join('')||'<p class="hint">Leave the first note. Good context prevents duplicate work.</p>';$('#history').innerHTML=[...selected.history].reverse().map(c=>`<article class="history-entry"><strong>${esc(c.author)}</strong> ${esc(c.text)}<time>${esc(date(c.at))}</time></article>`).join('')}
async function act(action,fields){if(busy)return;if(!connected){$('#task-error').textContent='Reconnect before making changes.';$('#task-error').hidden=false;return}busy=true;document.querySelectorAll('#task-dialog button').forEach(b=>b.disabled=true);try{const result=await api('/api/actions/'+action,{actor,projectId,...fields});await refresh();return result}catch(e){$('#task-error').textContent=e.message;$('#task-error').hidden=false}finally{busy=false;document.querySelectorAll('#task-dialog button').forEach(b=>b.disabled=false)}}
$('#task-form').onsubmit=async e=>{e.preventDefault();const r=await act(selected?'update_task':'create_task',{...(selected?{id:selected.id,revision:selected.revision}:{}),title:$('#title').value,description:$('#description').value,assignee:$('#assignee').value,status:$('#status').value});if(r)openTask(r)};
$('#claim-task').onclick=async()=>{const r=await act('claim_task',{id:selected.id,revision:selected.revision});if(r)openTask(r)};
$('#release-task').onclick=async()=>{const r=await act('release_task',{id:selected.id,revision:selected.revision});if(r)openTask(r)};
$('#comment-form').onsubmit=async e=>{e.preventDefault();const r=await act('add_comment',{id:selected.id,text:$('#comment').value});if(r){const onlyOwnComment=r.revision===selected.revision+1;selected.comments=r.comments;if(onlyOwnComment){selected.revision=r.revision;$('#revision').textContent='Revision '+r.revision}renderContext();$('#comment').value='';$('#changed').hidden=onlyOwnComment}};
$('#reload-task').onclick=()=>{const t=tasks.find(t=>t.id===selected.id);if(t)openTask(t)};
$('#close-task').onclick=()=>$('#task-dialog').close();$('#close-settings').onclick=()=>$('#settings-dialog').close();
function settings(){$('#actor').value=actor;$('#token').value=token;$('#settings-dialog').showModal()}
$('#settings-button').onclick=settings;$('#settings-form').onsubmit=e=>{e.preventDefault();actor=$('#actor').value.trim();if(!actor)return;token=$('#token').value.trim();localStorage.setItem('board-actor',actor);sessionStorage.setItem('board-token',token);$('#settings-dialog').close();$('#task-dialog').close();$('#request-dialog').close();selected=null;requestSelected=null;refresh()};
$('#new-task').onclick=()=>openTask(null);$('#search').oninput=render;$('#filter-assignee').onchange=render;$('#board-tab').onclick=()=>{queuesView=false;memory=false;render()};$('#memory-tab').onclick=()=>{queuesView=false;memory=true;render()};
$('#project-select').onchange=()=>{projectId=$('#project-select').value;localStorage.setItem('featherkan-project',projectId);queueId='';selected=null;tasks=[];queues=[];requests=[];connected=false;$('#filter-assignee').value='';$('#search').value='';render();refresh()};
$('#queue-filter').onchange=()=>{queueId=$('#queue-filter').value;requests=[];render();refresh()};
function openCreate(kind){createKind=kind;$('#create-heading').textContent=kind==='project'?'New project':'New supervisor queue';$('#capacity-label').hidden=kind!=='queue';$('#create-queue-hint').hidden=kind!=='queue';$('#create-name').value='';$('#create-description').value='';$('#create-capacity').value='1';$('#create-error').hidden=true;$('#create-dialog').showModal()}
$('#new-project').onclick=()=>openCreate('project');$('#new-queue').onclick=()=>openCreate('queue');$('#close-create').onclick=()=>$('#create-dialog').close();
$('#create-form').onsubmit=async e=>{e.preventDefault();$('#create-submit').disabled=true;try{const result=await api('/api/actions/create_'+createKind,{projectId,name:$('#create-name').value,description:$('#create-description').value,...(createKind==='queue'?{maxInFlight:Number($('#create-capacity').value)}:{})});if(createKind==='project'){projectId=result.id;queueId='';localStorage.setItem('featherkan-project',projectId)}else{queueId=result.id}$('#create-dialog').close();await refresh()}catch(error){$('#create-error').textContent=error.message;$('#create-error').hidden=false}finally{$('#create-submit').disabled=false}};
$('#claim-next').onclick=async()=>{if(!actor){settings();return}$('#claim-next').disabled=true;try{const task=await api('/api/actions/claim_next',{projectId,queueId,actor});await refresh();if(task)openRequest(task);else message('No eligible work: this queue is empty or at its active-work limit.')}catch(e){message(e.message)}finally{$('#claim-next').disabled=false}};
$('#queues-tab').onclick=()=>{queuesView=true;render()};
function openRequest(r){
 if(!actor){settings();return}requestSelected=r?structuredClone(r):null;
 $('#request-heading').textContent=r?'Request '+r.id.slice(0,6).toUpperCase():'New request';
 $('#request-meta').textContent=r?r.status+' · '+(r.claimedBy||'Unclaimed')+' · revision '+r.revision:'General JSON data for the supervisor to parse.';
 $('#request-payload').value=JSON.stringify(r?r.payload:{},null,2);$('#request-payload').readOnly=!!r;
 $('#request-result').value=r?.result===undefined?'':JSON.stringify(r.result,null,2);$('#request-failure').value=r?.error||'';
 $('#request-outcome').hidden=!r;$('#enqueue-request').hidden=!!r;$('#reload-request').hidden=!r;$('#request-history-details').hidden=!r;
 const owner=r&&(admin||r.claimedBy===actor);$('#complete-request').hidden=!owner||r.status!=='active';$('#fail-request').hidden=!owner||r.status!=='active';$('#release-request').hidden=!owner||!['active','failed'].includes(r.status);
 $('#request-history').innerHTML=(r?.history||[]).map(h=>`<p>${esc(h.author)} · ${esc(h.text)} · ${esc(date(h.at))}</p>`).join('');$('#request-error').hidden=true;
 if(!$('#request-dialog').open)$('#request-dialog').showModal();
}
$('#new-request').onclick=()=>openRequest(null);$('#close-request').onclick=()=>$('#request-dialog').close();
async function requestAction(action){
 if(busy)return;busy=true;document.querySelectorAll('#request-dialog button').forEach(b=>b.disabled=true);
 try{
 const fields=requestSelected?{id:requestSelected.id,revision:requestSelected.revision}:{};
 if(action==='enqueue_request')fields.payload=JSON.parse($('#request-payload').value);
 if(['complete_request','fail_request'].includes(action)&&$('#request-result').value.trim())fields.result=JSON.parse($('#request-result').value);
 if(action==='fail_request')fields.text=$('#request-failure').value;
 const r=await api('/api/actions/'+action,{projectId,queueId,actor,...fields});await refresh();openRequest(r);
 }catch(e){$('#request-error').textContent=e.message;$('#request-error').hidden=false}finally{busy=false;document.querySelectorAll('#request-dialog button').forEach(b=>b.disabled=false)}
}
$('#request-form').onsubmit=e=>{e.preventDefault();if(!requestSelected)requestAction('enqueue_request')};
$('#complete-request').onclick=()=>requestAction('complete_request');$('#fail-request').onclick=()=>requestAction('fail_request');$('#release-request').onclick=()=>requestAction('release_request');
$('#reload-request').onclick=async()=>{try{openRequest(await api('/api/actions/get_request',{projectId,queueId,id:requestSelected.id}))}catch(e){$('#request-error').textContent=e.message;$('#request-error').hidden=false}};
refresh();setInterval(refresh,5000);if(!actor)settings();
