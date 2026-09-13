'use strict';
let token = '';
let refreshing = false;
let session = 0;
let editingID = '';
let selectedScripts = [];
let importing = false;
let saving = false;
let startTarget = null;
const cards = new Map();
const $ = id => document.getElementById(id);
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function api(path, method = 'GET', body) {
  const headers = {Authorization: 'Bearer ' + token};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch('/api/' + path, {method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(100000)});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}
function roomPath(id) { return 'rooms/' + encodeURIComponent(id); }
function openStart(room, action) {
  startTarget = {id: room.id, action};
  $('start-title').textContent = `${action === 'restart' ? 'Restart' : 'Start'} ${room.name}`;
  $('start-form').querySelector('button[type=submit]').textContent = action === 'restart' ? 'Restart room' : 'Start room';
  $('start-token').value = ''; $('start-error').textContent = '';
  $('start-dialog').showModal(); $('start-token').focus();
}
async function action(view, name, roomToken) {
  view.busy = true; view.buttons.forEach(b => b.disabled = true); $('error').textContent = '';
  try { await api(roomPath(view.room.id) + '/' + name, 'POST', roomToken === undefined ? undefined : {token: roomToken}); }
  catch (error) { $('error').textContent = error.message; }
  finally { view.busy = false; await refresh(); }
}
function card(room) {
  const root = element('article'); root.dataset.id = room.id;
  const heading = element('div', undefined, 'room-heading');
  const title = element('h2'); const state = element('span', '', 'badge'); heading.append(title, state);
  const settings = element('p', '', 'help');
  const link = element('a', 'Join room ↗'); link.target = '_blank'; link.rel = 'noopener noreferrer';
  const status = element('p');
  const actions = element('div', undefined, 'actions');
  const view = {root, title, state, settings, status, link, buttons: [], busy: false, room};
  for (const name of ['start', 'restart', 'stop', 'edit', 'delete']) {
    const button = element('button', name[0].toUpperCase() + name.slice(1), 'secondary'); button.dataset.action = name;
    button.addEventListener('click', async () => {
      if (name === 'start' || name === 'restart') return openStart(view.room, name);
      if (name === 'edit') {
        try { openEditor(await api(roomPath(view.room.id))); } catch (error) { $('error').textContent = error.message; }
        return;
      }
      if (name === 'delete') {
        if (!confirm(`Delete saved room “${view.room.name}” and its scripts?`)) return;
        try { await api(roomPath(view.room.id), 'DELETE'); await refresh(); } catch (error) { $('error').textContent = error.message; }
        return;
      }
      await action(view, name);
    });
    actions.append(button); view.buttons.push(button);
  }
  const details = element('details'); details.append(element('summary', 'Recent logs'));
  view.logs = element('pre'); details.append(view.logs);
  const scripts = element('details'); scripts.append(element('summary', 'Saved scripts')); view.scripts = element('ol'); scripts.append(view.scripts);
  root.append(heading, settings, status, link, actions, scripts, details); $('rooms').append(root);
  return view;
}
async function refresh() {
  if (!token || refreshing) return;
  const currentSession = session; refreshing = true;
  try {
    const rooms = await api('rooms'); if (currentSession !== session) return;
    $('login').hidden = true; $('dashboard').hidden = false; $('empty').hidden = rooms.length > 0;
    $('summary').textContent = `${rooms.filter(r => r.state === 'running').length} running / ${rooms.length} saved`;
    const ids = new Set(rooms.map(r => r.id));
    for (const [id, view] of cards) if (!ids.has(id)) { view.root.remove(); cards.delete(id); }
    for (const room of rooms) {
      if (!cards.has(room.id)) cards.set(room.id, card(room));
      const view = cards.get(room.id); view.room = room; view.title.textContent = room.name;
      view.state.textContent = room.state; view.state.dataset.state = room.state;
      view.settings.textContent = `${room.settings.maxPlayers} players · ${room.settings.public ? 'Public' : 'Unlisted'} · ${room.scriptNames.length} scripts`;
      view.status.textContent = room.error || (room.state === 'running' ? (room.link ? 'Room link received.' : 'Scripts loaded. Waiting for a room link; check logs for token or script errors.') : 'Ready to start with a fresh token.');
      let safeLink = false;
      try { safeLink = new URL(room.link).origin === 'https://www.webliero.com'; } catch {}
      view.link.hidden = !safeLink; if (safeLink) view.link.href = room.link;
      view.logs.textContent = room.logs.map(l => `${new Date(l.at).toLocaleTimeString()}  ${l.message}`).join('\n') || 'No logs yet.';
      view.scripts.replaceChildren(...room.scriptNames.map(name => element('li', name)));
      view.buttons.forEach(button => {
        const name = button.dataset.action;
        button.disabled = view.busy || room.state === 'starting' || ((name === 'start' || name === 'edit' || name === 'delete') && room.state === 'running') || ((name === 'stop' || name === 'restart') && room.state === 'stopped');
      });
    }
  } catch (error) { if (currentSession === session) $('error').textContent = 'Launcher: ' + error.message; }
  finally { refreshing = false; }
}
function renderScripts() {
  $('script-list').replaceChildren();
  const bytes = selectedScripts.reduce((sum, s) => sum + new TextEncoder().encode(s.source).length, 0);
  $('script-count').textContent = `${selectedScripts.length} scripts · ${(bytes / 1024).toFixed(1)} KiB / 4096 KiB`;
  selectedScripts.forEach((script, index) => {
    const row = element('li'); row.append(element('span', script.name));
    const controls = element('div', undefined, 'script-controls');
    for (const [label, delta] of [['↑', -1], ['↓', 1], ['Remove', 0]]) {
      const button = element('button', label, 'secondary'); button.type = 'button';
      button.setAttribute('aria-label', `${delta ? (delta < 0 ? 'Move up' : 'Move down') : 'Remove'} ${script.name}`);
      button.disabled = importing || (delta === -1 && index === 0) || (delta === 1 && index === selectedScripts.length - 1);
      button.onclick = () => {
        if (delta) [selectedScripts[index], selectedScripts[index + delta]] = [selectedScripts[index + delta], selectedScripts[index]];
        else selectedScripts.splice(index, 1);
        renderScripts();
      };
      controls.append(button);
    }
    row.append(controls); $('script-list').append(row);
  });
}
function editorBusy(busy) { $('room-form').querySelectorAll('button, input').forEach(control => control.disabled = busy); }
async function importFiles(fileList) {
  if (importing) return;
  importing = true; editorBusy(true); $('editor-error').textContent = '';
  try {
    const files = Array.from(fileList).filter(f => {
      const path = f.webkitRelativePath || f.name;
      return /\.js$/i.test(path) && !path.split('/').some(part => part.startsWith('.') || part === 'node_modules');
    }).sort((a, b) => {
      const x = a.webkitRelativePath || a.name, y = b.webkitRelativePath || b.name;
      return x < y ? -1 : x > y ? 1 : 0;
    });
    if (!files.length) throw new Error('No JavaScript files found in this selection.');
    if (files.length > 100 || files.reduce((sum, f) => sum + f.size, 0) > 4 * 1024 * 1024) throw new Error('Choose at most 100 scripts and 4 MiB in total.');
    const next = selectedScripts.slice();
    for (const file of files) {
      const name = file.webkitRelativePath ? file.webkitRelativePath.split('/').slice(1).join('/') : file.name;
      const script = {name, source: await file.text()};
      const index = next.findIndex(s => s.name === name);
      if (index < 0) next.push(script); else next[index] = script;
    }
    if (next.length > 100 || next.reduce((sum, s) => sum + new TextEncoder().encode(s.source).length, 0) > 4 * 1024 * 1024) throw new Error('The combined script list exceeds 100 files or 4 MiB.');
    selectedScripts = next;
    if (selectedScripts.some(s => /\bWLInit\s*\(/.test(s.source))) $('script-creates').checked = true;
  } catch (error) { $('editor-error').textContent = error.message; }
  finally { importing = false; editorBusy(false); renderScripts(); $('script-files').value = ''; $('script-folder').value = ''; }
}
function openEditor(profile) {
  $('room-form').reset(); editingID = profile?.id || ''; selectedScripts = profile?.scripts || [];
  $('editor-title').textContent = editingID ? 'Edit room' : 'Add room';
  $('room-name').value = profile?.name || ''; $('max-players').value = profile?.settings.maxPlayers || 12;
  $('room-public').checked = profile?.settings.public || false; $('room-password').value = profile?.settings.password || '';
  $('script-creates').checked = profile?.scriptCreatesRoom || false;
  $('editor-error').textContent = ''; renderScripts(); $('editor').showModal(); $('room-name').focus();
}
$('script-files').onchange = event => importFiles(event.target.files);
$('script-folder').onchange = event => importFiles(event.target.files);
$('clear-scripts').onclick = () => { selectedScripts = []; renderScripts(); };
$('add-room').onclick = () => openEditor();
$('editor-close').onclick = () => $('editor').close();
$('editor').addEventListener('cancel', event => { if (importing || saving) event.preventDefault(); });
$('editor').addEventListener('close', () => { $('create-token').value = ''; selectedScripts = []; });
$('room-form').addEventListener('submit', async event => {
  event.preventDefault(); if (importing) return;
  const start = event.submitter?.value === 'start';
  const roomToken = $('create-token').value.trim();
  if (start && !roomToken) { $('editor-error').textContent = 'Paste a WebLiero token, or choose Save for later.'; $('create-token').focus(); return; }
  const profile = {name: $('room-name').value.trim(), settings: {maxPlayers: Number($('max-players').value), public: $('room-public').checked, password: $('room-password').value}, scriptCreatesRoom: $('script-creates').checked, scripts: selectedScripts};
  saving = true; editorBusy(true); $('editor-error').textContent = ''; $('create-token').value = '';
  try {
    let id = editingID;
    if (id) await api(roomPath(id), 'PUT', profile);
    else id = (await api('rooms', 'POST', profile)).id;
    $('editor').close(); await refresh();
    if (start) {
      try { await api(roomPath(id) + '/start', 'POST', {token: roomToken}); }
      catch (error) { $('error').textContent = 'Room saved, but startup failed: ' + error.message; }
      await refresh();
    }
  } catch (error) { $('editor-error').textContent = error.message; }
  finally { saving = false; editorBusy(false); }
});
$('start-cancel').onclick = () => $('start-dialog').close();
$('start-dialog').addEventListener('close', () => { $('start-token').value = ''; });
$('start-form').addEventListener('submit', async event => {
  event.preventDefault(); const roomToken = $('start-token').value.trim();
  if (!roomToken) return;
  const target = startTarget; $('start-token').value = ''; $('start-dialog').close();
  const view = cards.get(target.id); if (view) await action(view, target.action, roomToken);
});
$('login').addEventListener('submit', async event => {
  event.preventDefault(); session++; token = $('token').value.trim(); $('token').value = ''; $('error').textContent = ''; await refresh();
});
$('logout').addEventListener('click', () => {
  session++; token = ''; cards.clear(); $('rooms').replaceChildren(); $('editor').close(); $('start-dialog').close(); $('login').hidden = false; $('dashboard').hidden = true; $('error').textContent = '';
});
setInterval(refresh, 2500);
