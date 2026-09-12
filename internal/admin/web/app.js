'use strict';
// Keep credentials in memory only. Refreshing the page requires reconnecting.
let token = '';
let refreshing = false;
let session = 0;
const cards = new Map();
const $ = id => document.getElementById(id);
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function api(path, method = 'GET') {
  const response = await fetch('/api/' + path, {method, headers: {Authorization: 'Bearer ' + token}, signal: AbortSignal.timeout(100000)});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}
function card(id) {
  const root = element('article');
  const heading = element('div', undefined, 'room-heading');
  const title = element('h2', id);
  const state = element('span', '', 'badge');
  heading.append(title, state);
  const link = element('a', 'Join room ↗');
  link.target = '_blank'; link.rel = 'noopener noreferrer';
  const status = element('p');
  const actions = element('div', undefined, 'actions');
  const buttons = ['start', 'restart', 'stop'].map(action => {
    const button = element('button', action[0].toUpperCase() + action.slice(1), 'secondary');
    button.addEventListener('click', async () => {
      buttons.forEach(b => b.disabled = true); view.busy = true;
      $('error').textContent = '';
      try { await api('rooms/' + encodeURIComponent(id) + '/' + action, 'POST'); }
      catch (error) { $('error').textContent = error.message; }
      finally { view.busy = false; await refresh(); }
    });
    actions.append(button); return button;
  });
  const details = element('details');
  details.append(element('summary', 'Recent logs'));
  const logs = element('pre'); details.append(logs);
  root.append(heading, status, link, actions, details);
  $('rooms').append(root);
  const view = {root, state, status, link, logs, buttons, busy: false};
  return view;
}
async function refresh() {
  if (!token || refreshing) return;
  const currentSession = session;
  refreshing = true;
  try {
    const rooms = await api('rooms');
    if (currentSession !== session) return;
    $('login').hidden = true; $('dashboard').hidden = false;
    $('summary').textContent = `${rooms.filter(r => r.state === 'running').length} running / ${rooms.length} configured`;
    for (const room of rooms) {
      if (!cards.has(room.id)) cards.set(room.id, card(room.id));
      const view = cards.get(room.id);
      view.state.textContent = room.state; view.state.dataset.state = room.state;
      view.status.textContent = room.error || (room.state === 'running' ? (room.link ? 'Room link received.' : 'Scripts loaded. Waiting for a room link; check logs for token or script errors.') : '');
      let safeLink = false;
      try { const url = new URL(room.link); safeLink = url.origin === 'https://www.webliero.com'; } catch {}
      view.link.hidden = !safeLink;
      if (safeLink) view.link.href = room.link;
      view.logs.textContent = room.logs.map(l => `${new Date(l.at).toLocaleTimeString()}  ${l.message}`).join('\n') || 'No logs yet.';
      view.buttons.forEach((b, i) => { b.disabled = view.busy || room.state === 'starting' || (i === 0 && room.state === 'running') || (i === 2 && room.state === 'stopped'); });
    }
  } catch (error) { $('error').textContent = 'Launcher: ' + error.message; }
  finally { refreshing = false; }
}
$('login').addEventListener('submit', async event => {
  event.preventDefault(); session++; token = $('token').value.trim(); $('token').value = ''; $('error').textContent = ''; await refresh();
});
$('logout').addEventListener('click', () => {
  session++; token = ''; cards.clear(); $('rooms').replaceChildren(); $('login').hidden = false; $('dashboard').hidden = true; $('error').textContent = '';
});
setInterval(refresh, 2500);
