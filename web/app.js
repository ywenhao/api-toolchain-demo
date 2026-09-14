const $ = (selector) => document.querySelector(selector);
let items = [], editing = null, stream = null, lastEventId = '', eventCount = 0, requestVersion = 0;
let manualDisconnect = false;
let role = new URLSearchParams(location.search).get('role') === 'admin' ? 'admin' : 'user';
const base = () => `/${role}/v1`;
function feedback(message, error = false) { $('#feedback').textContent = message; $('#feedback').classList.toggle('error', error); }
async function api(path, options = {}) {
  const response = await fetch(path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
  const body = await response.json();
  if (!response.ok) throw new Error(body.message || `HTTP ${response.status}`);
  return { body, status: response.status };
}
async function refresh() {
  const version = ++requestVersion;
  try {
    const { body } = await api(`${base()}/tasks?limit=100`);
    if (version !== requestVersion) return;
    items = body.tasks || [];
    $('#task-count').textContent = body.total;
    render();
  } catch (error) { feedback(error.message, true); }
}
function render() {
  const list = $('#task-list'); list.replaceChildren();
  if (!items.length) { const empty = document.createElement('div'); empty.className = 'empty'; empty.textContent = '还没有任务。在上方创建第一个任务，看看实时事件。'; list.append(empty); return; }
  for (const task of items) {
    const row = document.createElement('article'); row.className = `task${task.completed ? ' done' : ''}`;
    const check = document.createElement('input'); check.type = 'checkbox'; check.checked = task.completed; check.setAttribute('aria-label', `完成 ${task.title}`);
    check.addEventListener('change', async () => { check.disabled = true; try { await api(`${base()}/tasks/${encodeURIComponent(task.id)}`, { method: 'PATCH', body: JSON.stringify({ completed: check.checked }) }); feedback('200 OK · 完成状态已更新'); await refresh(); } catch (error) { check.checked = task.completed; feedback(error.message, true); } finally { check.disabled = false; } });
    const main = document.createElement('div'); main.className = 'task-main';
    const title = document.createElement('h3'); title.textContent = task.title; main.append(title);
    if (task.description) { const description = document.createElement('p'); description.textContent = task.description; main.append(description); }
    const id = document.createElement('span'); id.className = 'task-id'; id.textContent = task.id; main.append(id);
    const actions = document.createElement('div'); actions.className = 'task-actions';
    const edit = document.createElement('button'); edit.textContent = '编辑'; edit.setAttribute('aria-label', `编辑 ${task.title}`); edit.addEventListener('click', () => startEdit(task));
    const remove = document.createElement('button'); remove.textContent = '删除'; remove.setAttribute('aria-label', `删除 ${task.title}`);
    remove.addEventListener('click', async () => { remove.disabled = true; try { await api(`${base()}/tasks/${encodeURIComponent(task.id)}`, { method: 'DELETE' }); if (editing === task.id) resetForm(); feedback('200 OK · 任务已删除'); await refresh(); } catch (error) { feedback(error.message, true); remove.disabled = false; } });
    actions.append(edit, remove); row.append(check, main, actions); list.append(row);
  }
}
function startEdit(task) { editing = task.id; $('#title').value = task.title; $('#description').value = task.description; $('#form-title').textContent = '编辑任务'; $('#form-method').textContent = `PATCH ${base()}/tasks/{id}`; $('#submit').textContent = '保存修改'; $('#cancel-edit').hidden = false; $('#title').focus(); }
function resetForm() { editing = null; $('#task-form').reset(); $('#form-title').textContent = '创建一个任务'; $('#form-method').textContent = `POST ${base()}/tasks`; $('#submit').textContent = '创建任务 ＋'; $('#cancel-edit').hidden = true; }
$('#task-form').addEventListener('submit', async (event) => {
  event.preventDefault(); const button = $('#submit'); button.disabled = true;
  try {
    const path = editing ? `${base()}/tasks/${encodeURIComponent(editing)}` : `${base()}/tasks`;
    const { status } = await api(path, { method: editing ? 'PATCH' : 'POST', body: JSON.stringify({ title: $('#title').value, description: $('#description').value }) });
    feedback(`${status} ${status === 201 ? 'Created · 任务已创建' : 'OK · 任务已更新'}`); resetForm(); await refresh();
  } catch (error) { feedback(error.message, true); } finally { button.disabled = false; }
});
$('#cancel-edit').addEventListener('click', resetForm);
$('#refresh').addEventListener('click', refresh);
function connection(text, online) { $('#connection').replaceChildren(); const dot = document.createElement('i'); $('#connection').append(dot, document.createTextNode(text)); $('#connection').classList.toggle('online', online); }
function appendEvent(name, raw, id) {
  if (id) lastEventId = id;
  const list = $('#event-list'); list.querySelector('.event-placeholder')?.remove();
  const entry = document.createElement('li'); entry.className = 'event-entry';
  const heading = document.createElement('div'); const type = document.createElement('span'); type.className = `event-name ${name.split('.').at(-1)}`; type.textContent = name;
  const eventID = document.createElement('span'); eventID.className = 'event-id'; eventID.textContent = id ? `#${id}` : '—';
  const time = document.createElement('time'); time.textContent = new Date().toLocaleTimeString('zh-CN', { hour12: false }); heading.append(type, eventID, time);
  const body = document.createElement('pre'); try { body.textContent = JSON.stringify(JSON.parse(raw), null, 2); } catch { body.textContent = raw; }
  entry.append(heading, body); list.prepend(entry); while (list.children.length > 100) list.lastChild.remove();
  $('#event-count').textContent = `${++eventCount} 条事件`;
}
function connect() {
  manualDisconnect = false; connection('连接中', false);
  const query = lastEventId ? `?last_event_id=${encodeURIComponent(lastEventId)}` : '';
  stream = new EventSource(`${base()}/events${query}`); $('#toggle-stream').textContent = '断开';
  stream.onopen = () => connection('实时连接', true);
  stream.onerror = () => { if (!manualDisconnect) connection('自动重连中', false); };
  stream.addEventListener('ready', (event) => { appendEvent('ready', event.data, event.lastEventId); refresh(); });
  for (const name of ['task.created', 'task.updated', 'task.deleted', 'reset']) stream.addEventListener(name, (event) => { appendEvent(name, event.data, event.lastEventId); refresh(); });
}
$('#toggle-stream').addEventListener('click', () => { if (manualDisconnect) connect(); else { manualDisconnect = true; stream?.close(); connection('已断开', false); $('#toggle-stream').textContent = '重连'; } });
$('#clear-events').addEventListener('click', () => { $('#event-list').replaceChildren(); eventCount = 0; $('#event-count').textContent = '0 条事件'; });
window.addEventListener('pagehide', () => stream?.close());
fetch('/healthz').then((r) => { if (!r.ok) throw new Error(); $('#api-status').textContent = `● HTTP API ONLINE · ${location.host}`; }).catch(() => { $('#api-status').textContent = 'HTTP API OFFLINE'; });
setRole(role);

function setRole(value) {
  stream?.close();role=value;lastEventId='';requestVersion++;resetForm();
  $('#role-user').classList.toggle('selected',role==='user');$('#role-admin').classList.toggle('selected',role==='admin');
  $('#role-user').setAttribute('aria-pressed',String(role==='user'));$('#role-admin').setAttribute('aria-pressed',String(role==='admin'));
  $('#role-description').textContent=`${base()} · ${role==='user'?'用户端任务接口':'管理端任务接口 + /stats'}`;
  $('#docs-link').href=`/docs/${role}`;$('#docs-link').textContent=`${role==='user'?'用户端':'管理端'}文档 ↗`;
  $('#spec-link').href=`/openapi/${role}.json`;
  $('.stream-address code').textContent=`${base()}/events`;
  $('#event-list').replaceChildren();eventCount=0;$('#event-count').textContent='0 条事件';
  history.replaceState(null,'',role==='admin'?'/?role=admin':'/');feedback('');connect();
}
$('#role-user').addEventListener('click',()=>setRole('user'));
$('#role-admin').addEventListener('click',()=>setRole('admin'));
