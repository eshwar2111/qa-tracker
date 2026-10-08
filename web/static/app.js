'use strict';

// ---------- helpers ----------
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
const app = $('#app');

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

async function api(path, opts = {}) {
  const init = { ...opts };
  if (init.body && !(init.body instanceof FormData)) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(init.body);
  }
  const res = await fetch('/api' + path, init);
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

function toast(msg, err = false) {
  const el = document.createElement('div');
  el.className = 'toast' + (err ? ' err' : '');
  el.textContent = msg;
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), err ? 7000 : 4000);
}

const store = {
  get(k, d = null) { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* storage unavailable */ } },
  del(k) { try { localStorage.removeItem(k); } catch { /* storage unavailable */ } },
};

function ago(iso) {
  if (!iso) return '';
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  if (s < 86400 * 7) return Math.floor(s / 86400) + 'd ago';
  return new Date(iso).toLocaleDateString();
}
const fullTime = iso => iso ? new Date(iso).toLocaleString() : '';

const STATUS_LABEL = { untested: 'Untested', pass: 'Pass', fail: 'Fail', blocked: 'Blocked', in_fix: 'In fix', fixed: 'Fixed · retest' };
const statusBadge = s => `<span class="badge s-${esc(s)}">${esc(STATUS_LABEL[s] || s)}</span>`;
const resultBadge = r => r ? `<span class="badge s-${esc(r)}">${esc(r)}</span>` : '<span class="muted">—</span>';
const prioBadge = p => `<span class="badge b-prio b-${esc(p)}">${esc(p)}</span>`;
const regressBadge = c => c.reopen_count > 0 ? `<span class="badge b-regress" title="Failed again after a fix">Regression ×${c.reopen_count}</span>` : '';
const who = a => a === 'claude' ? '<span class="who-claude">Claude</span>' : '<span class="who-user">You</span>';

// ---------- state ----------
const state = {
  project: store.get('qa.project', ''),
  projects: [],
  areas: [],
  lastEvent: 0,
  deferredRefresh: false,
  render: null, // re-render function for the current view
};

// ---------- routing ----------
function parseHash() {
  const h = location.hash.replace(/^#/, '') || '/';
  const [path, qs] = h.split('?');
  return { parts: path.split('/').filter(Boolean), q: new URLSearchParams(qs || '') };
}

function setNav(name) {
  $$('.top nav a').forEach(a => a.classList.toggle('active', a.dataset.nav === name));
}

async function route() {
  closeDialogs();
  const { parts, q } = parseHash();
  try {
    if (!state.project) {
      app.innerHTML = `<div class="card empty"><h2>No projects yet</h2><p>Create one from the CLI:</p><pre class="pre">qa project add voice-agent --name "Voice Agent" --repo "E:\\Voice Agent"
qa import suites\\voice-agent.json</pre></div>`;
      return;
    }
    if (parts[0] === 'cases' && parts[1]) { setNav('cases'); await viewCase(+parts[1]); }
    else if (parts[0] === 'cases') { setNav('cases'); await viewCases(q); }
    else if (parts[0] === 'runs' && parts[1]) { setNav('runs'); await viewRun(+parts[1], q); }
    else if (parts[0] === 'runs') { setNav('runs'); await viewRuns(); }
    else { setNav('dash'); await viewDashboard(); }
  } catch (e) {
    app.innerHTML = `<div class="card empty"><h2>Something went wrong</h2><p>${esc(e.message)}</p></div>`;
  }
}

const P = () => encodeURIComponent(state.project);

// ---------- dashboard ----------
async function viewDashboard() {
  const { summary: s } = await api(`/projects/${P()}/summary`);
  const tile = (st, n, label, href) => `<a class="tile ${st}" href="${href}"><div class="n">${n}</div><div class="l">${label}</div></a>`;
  const statuses = ['untested', 'pass', 'fail', 'blocked', 'in_fix', 'fixed'];
  const sev = ['critical', 'major', 'minor', 'trivial', 'unset'].filter(k => s.open_by_severity[k]);
  const passRate = s.total ? Math.round(100 * s.by_status.pass / s.total) : 0;

  app.innerHTML = `
    <h1>Dashboard</h1>
    <div class="tiles">
      ${tile('', s.total, `Cases · ${passRate}% passing`, '#/cases')}
      ${tile('fail', s.open_bugs, 'Open bugs', '#/cases?status=open')}
      ${tile('fixed', s.needs_retest, 'To test (new + fixed)', '#/cases?status=needs-retest')}
      ${statuses.map(st => tile(st, s.by_status[st] || 0, STATUS_LABEL[st], `#/cases?status=${st}`)).join('')}
    </div>
    <div class="grid cols-2">
      <div class="card overflow">
        <h2>By area</h2>
        <table class="list matrix">
          <tr><th>Area</th>${statuses.map(st => `<th>${STATUS_LABEL[st]}</th>`).join('')}<th>Total</th></tr>
          ${s.areas.map(a => `<tr><td><a href="#/cases?area=${esc(a.key)}">${esc(a.name)}</a></td>
            ${statuses.map(st => { const n = a.by_status[st] || 0; return `<td>${n ? `<a class="cell badge s-${st}" href="#/cases?area=${esc(a.key)}&status=${st}">${n}</a>` : '<span class="muted">·</span>'}</td>`; }).join('')}
            <td>${a.total}</td></tr>`).join('')}
        </table>
      </div>
      <div class="grid">
        <div class="card">
          <h2>Open bugs by severity</h2>
          ${sev.length ? sev.map(k => `<div class="row"><span class="badge ${k === 'critical' ? 'b-regress' : 'b-prio'}">${k}</span> ${s.open_by_severity[k]}</div>`).join('') : '<p class="muted">No open bugs.</p>'}
        </div>
        <div class="card">
          <h2>Regressions</h2>
          ${s.regressions.length ? `<ul class="feed">${s.regressions.map(c => `<li><a href="#/cases/${c.id}">${esc(c.title)}</a> ${statusBadge(c.status)} ${regressBadge(c)}</li>`).join('')}</ul>` : '<p class="muted">None — nothing has failed after a fix.</p>'}
        </div>
      </div>
    </div>
    <div class="card" style="margin-top:16px">
      <h2>Recent activity</h2>
      <ul class="feed">${s.recent.map(e => `<li>${who(e.actor)} ${eventText(e, true)} <span class="muted" title="${esc(fullTime(e.created_at))}">· ${ago(e.created_at)}</span></li>`).join('') || '<li class="muted">Nothing yet.</li>'}</ul>
    </div>`;
  state.render = viewDashboard;
}

// ---------- events → text ----------
function eventText(e, withCase) {
  const d = e.data || {};
  const c = withCase && e.case_id ? ` <a href="#/cases/${e.case_id}">${esc(e.case_title || e.case_key)}</a>` : '';
  switch (e.kind) {
    case 'project_created': return `created project ${esc(d.key)}`;
    case 'case_created': return `added case${c}`;
    case 'case_updated': {
      const reset = e.to_status === 'untested' && e.from_status !== 'untested' ? ' — <b>reset to untested</b>' : '';
      return `updated${c} (${esc((d.changed || []).join(', '))})${d.version ? ' → v' + d.version : ''}${reset}`;
    }
    case 'case_archived': return `archived${c}`;
    case 'result': return `marked${c} ${resultBadge(d.result)}${d.severity ? ` <span class="badge b-prio">${esc(d.severity)}</span>` : ''}${d.build ? ` <span class="muted mono">@${esc(d.build)}</span>` : ''}`;
    case 'reopened': return `<b>reopened</b>${c} — failed again after a fix (×${esc(d.reopen_count)})`;
    case 'claimed': return `started fixing${c}`;
    case 'fixed': return `fixed${c}${d.commit ? ` in <code>${esc(d.commit)}</code>` : ''}`;
    case 'comment': return `commented${c}`;
    case 'attachment': return `attached <a href="/api/attachments/${esc(d.attachment_id)}" target="_blank">${esc(d.filename)}</a>${c}`;
    case 'run_created': return `started run <a href="#/runs/${e.run_id}">${esc(d.name)}</a> (${esc(d.cases)} cases)`;
    case 'run_closed': return `closed run <a href="#/runs/${e.run_id}">#${e.run_id}</a>`;
    default: return esc(e.kind) + c;
  }
}

function eventBody(e) {
  const d = e.data || {};
  if (e.kind === 'result' && d.remarks) return d.remarks;
  if (e.kind === 'comment') return d.text;
  if (e.kind === 'fixed') return d.note + (d.files && d.files.length ? '\nFiles: ' + d.files.join(', ') : '');
  return '';
}

// ---------- cases list ----------
async function viewCases(q) {
  if (!state.areas.length) state.areas = await api(`/projects/${P()}/areas`);
  const params = new URLSearchParams();
  for (const k of ['status', 'area', 'priority', 'q']) if (q.get(k)) params.set(k, q.get(k));
  const cases = await api(`/projects/${P()}/cases?${params}`);
  const setQ = (k, v) => { const n = new URLSearchParams(q); v ? n.set(k, v) : n.delete(k); location.hash = '#/cases?' + n; };
  const chip = (val, label) => `<button class="chip ${q.get('status') === val || (!val && !q.get('status')) ? 'on' : ''}" data-status="${val}">${label}</button>`;

  app.innerHTML = `
    <h1>Cases <span class="muted">(${cases.length})</span></h1>
    <div class="card">
      <div class="chips">
        ${chip('', 'All')}${chip('open', 'Open bugs')}${chip('needs-retest', 'Needs (re)test')}
        ${['untested', 'pass', 'fail', 'blocked', 'in_fix', 'fixed'].map(s => chip(s, STATUS_LABEL[s])).join('')}
      </div>
      <div class="row">
        <select id="f-area"><option value="">All areas</option>${state.areas.map(a => `<option value="${esc(a.key)}" ${q.get('area') === a.key ? 'selected' : ''}>${esc(a.name)}</option>`).join('')}</select>
        <select id="f-prio"><option value="">All priorities</option>${['P0', 'P1', 'P2', 'P3'].map(p => `<option ${q.get('priority') === p ? 'selected' : ''}>${p}</option>`).join('')}</select>
        <input id="f-q" type="search" placeholder="Search title, steps, key…" value="${esc(q.get('q') || '')}" style="flex:1;min-width:200px">
      </div>
      <div class="overflow">
      <table class="list">
        <tr><th>Case</th><th>Area</th><th>Pri</th><th>Status</th><th>Updated</th></tr>
        ${cases.map(c => `<tr class="click" data-id="${c.id}">
          <td><div>${esc(c.title)} ${regressBadge(c)}</div><div class="muted mono">${esc(c.key)}</div></td>
          <td>${esc(c.area_name)}</td><td>${prioBadge(c.priority)}</td>
          <td>${statusBadge(c.status)}${c.severity ? ` <span class="muted">${esc(c.severity)}</span>` : ''}</td>
          <td class="muted" title="${esc(fullTime(c.updated_at))}">${ago(c.updated_at)}</td></tr>`).join('')}
      </table>
      ${cases.length ? '' : '<p class="empty">No cases match.</p>'}
      </div>
    </div>`;
  $$('.chip', app).forEach(b => b.onclick = () => setQ('status', b.dataset.status));
  $('#f-area').onchange = e => setQ('area', e.target.value);
  $('#f-prio').onchange = e => setQ('priority', e.target.value);
  let t;
  $('#f-q').oninput = e => { clearTimeout(t); t = setTimeout(() => setQ('q', e.target.value.trim()), 350); };
  if (q.get('q')) { const i = $('#f-q'); i.focus(); i.setSelectionRange(i.value.length, i.value.length); }
  $$('tr.click', app).forEach(tr => tr.onclick = () => location.hash = '#/cases/' + tr.dataset.id);
  state.render = () => viewCases(q);
}

// ---------- shared case body ----------
function caseBody(c, lastFix, prevRemarks) {
  const showFix = lastFix && (c.status === 'fixed' || c.status === 'in_fix');
  return `
    ${showFix ? `<div class="fixnote"><div class="t">Claude's fix — what to retest</div><div style="white-space:pre-wrap">${esc(lastFix.note)}</div>
      ${lastFix.commit ? `<div class="muted">commit <code>${esc(lastFix.commit)}</code>${lastFix.files && lastFix.files.length ? ' · ' + esc(lastFix.files.join(', ')) : ''}</div>` : ''}</div>` : ''}
    ${prevRemarks ? `<div class="prev"><b>Previously reported:</b> ${esc(prevRemarks)}</div>` : ''}
    ${c.preconditions ? `<h3>Preconditions</h3><div class="pre">${esc(c.preconditions)}</div>` : ''}
    <h3>Steps</h3><ol class="steps">${c.steps.map(s => `<li>${esc(s)}</li>`).join('')}</ol>
    <h3>Expected</h3><div class="expected">${esc(c.expected)}</div>`;
}

// ---------- case detail ----------
async function viewCase(id) {
  const c = await api(`/cases/${id}`);
  const lastFail = [...c.events].reverse().find(e => e.kind === 'result' && (e.data.result === 'fail' || e.data.result === 'blocked'));
  const prev = (c.status === 'fixed' || c.status === 'in_fix') && lastFail ? lastFail.data.remarks : '';
  app.innerHTML = `
    <p><a href="#/cases">← Cases</a></p>
    <div class="grid cols-2">
      <div class="card">
        <div class="row">${statusBadge(c.status)} ${prioBadge(c.priority)} ${regressBadge(c)} <span class="muted">${esc(c.area_name)} · v${c.version}</span></div>
        <h1>${esc(c.title)}</h1>
        <div class="muted mono">#${c.id} · ${esc(c.key)}</div>
        ${caseBody(c, c.last_fix, prev)}
        <div class="actions row">
          <button class="btn pass" data-r="pass">Pass</button>
          <button class="btn danger" data-r="fail">Fail…</button>
          <button class="btn warn" data-r="blocked">Blocked…</button>
          <span class="muted">Marking here records the result outside any run.</span>
        </div>
        ${c.attachments.length ? `<h3>Attachments</h3>${gallery(c.attachments)}` : ''}
        ${c.versions.length ? `<details style="margin-top:14px"><summary>Previous versions (${c.versions.length})</summary>
          ${c.versions.map(v => `<div class="card" style="margin-top:8px"><b>v${v.version}</b> <span class="muted">${esc(v.title)}</span>
            <ol class="steps">${(v.steps || []).map(s => `<li>${esc(s)}</li>`).join('')}</ol><div class="muted">Expected: ${esc(v.expected)}</div></div>`).join('')}
        </details>` : ''}
      </div>
      <div class="card">
        <h2>Timeline</h2>
        <ul class="timeline">${c.events.slice().reverse().map(e => `
          <li class="k-${esc(e.kind)} ${e.kind === 'result' ? 'r-' + esc(e.data.result) : ''}">
            <div>${who(e.actor)} ${eventText(e, false)}</div>
            ${eventBody(e) ? `<div class="body">${esc(eventBody(e))}</div>` : ''}
            <div class="when" title="${esc(fullTime(e.created_at))}">${ago(e.created_at)}${e.run_id ? ` · <a href="#/runs/${e.run_id}">run #${e.run_id}</a>` : ''}</div>
          </li>`).join('')}</ul>
        <h3>Add a comment</h3>
        <textarea id="comment" rows="3" placeholder="Extra context for Claude (Ctrl+Enter)"></textarea>
        <div class="row end"><button class="btn" id="comment-btn">Comment</button></div>
      </div>
    </div>`;
  $$('[data-r]', app).forEach(b => b.onclick = () => mark(c, b.dataset.r, null));
  const sendComment = async () => {
    const text = $('#comment').value.trim();
    if (!text) return;
    try { await api(`/cases/${id}/comment`, { method: 'POST', body: { text } }); await viewCase(id); }
    catch (e) { toast(e.message, true); }
  };
  $('#comment-btn').onclick = sendComment;
  $('#comment').onkeydown = e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) sendComment(); };
  state.render = () => viewCase(id);
}

function gallery(atts) {
  return `<div class="gallery">${atts.map(a => a.mime.startsWith('image/')
    ? `<a href="/api/attachments/${a.id}" target="_blank" title="${esc(a.filename)}"><img src="/api/attachments/${a.id}" alt="${esc(a.filename)}" loading="lazy"></a>`
    : `<a class="file" href="/api/attachments/${a.id}" target="_blank">📄 ${esc(a.filename)} <span class="muted">${Math.ceil(a.size / 1024)} KB</span></a>`).join('')}</div>`;
}

// ---------- marking + remarks drawer ----------
// mark records a result. Fail/blocked open the drawer first; resolves to the
// updated case, or null if cancelled.
async function mark(c, result, runId) {
  let extra = { remarks: '', severity: '' , files: [] };
  if (result === 'fail' || result === 'blocked') {
    extra = await openDrawer(c, result);
    if (!extra) return null;
  }
  try {
    const body = { result, remarks: extra.remarks, severity: extra.severity };
    if (runId) body.run_id = runId;
    const updated = await api(`/cases/${c.id}/result`, { method: 'POST', body });
    for (const f of extra.files) {
      const fd = new FormData();
      fd.append('file', f, f.name);
      if (runId) fd.append('run_id', runId);
      try { await api(`/cases/${c.id}/attachments`, { method: 'POST', body: fd }); }
      catch (e) { toast(`Attachment ${f.name}: ${e.message}`, true); }
    }
    store.del('qa.draft.' + c.id);
    if (!runId && state.render) await state.render();
    return updated;
  } catch (e) {
    toast(e.message, true);
    return null;
  }
}

let drawerResolve = null;
let pendingFiles = [];

function openDrawer(c, result) {
  const d = $('#drawer');
  $('#drawer-title').textContent = result === 'fail' ? 'Report failure' : 'Mark blocked';
  $('#drawer-case').textContent = `#${c.id} · ${c.title}`;
  $('#drawer-submit').textContent = result === 'fail' ? 'Submit failure' : 'Submit blocked';
  $('#drawer-submit').className = 'btn ' + (result === 'fail' ? 'danger' : 'warn');
  $('#severity').closest('.row').hidden = result !== 'fail';
  const draft = store.get('qa.draft.' + c.id, null);
  $('#remarks').value = draft ? draft.remarks : '';
  $('#severity').value = (draft && draft.severity) || (c.priority === 'P0' ? 'critical' : 'major');
  $('#remarks').placeholder = result === 'fail'
    ? 'What did you see vs. what was expected? Exact text on screen, timing, anything odd…'
    : 'What blocked you? (missing setup, depends on another bug, …)';
  pendingFiles = [];
  renderPending();
  d.hidden = false;
  d.dataset.caseId = c.id;
  d.dataset.result = result;
  setTimeout(() => $('#remarks').focus(), 0);
  return new Promise(res => { drawerResolve = res; });
}

function closeDrawer(value) {
  const d = $('#drawer');
  if (d.hidden) return;
  d.hidden = true;
  const r = drawerResolve;
  drawerResolve = null;
  if (r) r(value);
  if (state.deferredRefresh) { state.deferredRefresh = false; refreshView(); }
}

function submitDrawer() {
  const d = $('#drawer');
  const remarks = $('#remarks').value.trim();
  if (d.dataset.result === 'fail' && !remarks) {
    toast('Describe what went wrong — Claude needs it to fix the bug.', true);
    $('#remarks').focus();
    return;
  }
  closeDrawer({ remarks, severity: d.dataset.result === 'fail' ? $('#severity').value : '', files: pendingFiles });
}

function renderPending() {
  $('#pending-files').innerHTML = pendingFiles.map((f, i) => `<li>${esc(f.name)} <span class="muted">${Math.ceil(f.size / 1024)} KB</span> <a href="#" data-rm="${i}">remove</a></li>`).join('');
  $$('[data-rm]', $('#pending-files')).forEach(a => a.onclick = e => { e.preventDefault(); pendingFiles.splice(+a.dataset.rm, 1); renderPending(); });
}

function addFiles(list) {
  for (const f of list) {
    if (f.size > 20 * 1024 * 1024) { toast(`${f.name} is over 20 MB`, true); continue; }
    pendingFiles.push(f);
  }
  renderPending();
}

function initDrawer() {
  $('#drawer-cancel').onclick = () => closeDrawer(null);
  $('#drawer-submit').onclick = submitDrawer;
  $('#drawer').addEventListener('mousedown', e => { if (e.target.id === 'drawer') closeDrawer(null); });
  $('#remarks').addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submitDrawer(); } });
  const saveDraft = () => {
    const id = $('#drawer').dataset.caseId;
    if (id) store.set('qa.draft.' + id, { remarks: $('#remarks').value, severity: $('#severity').value });
  };
  $('#remarks').addEventListener('input', saveDraft);
  $('#severity').addEventListener('change', saveDraft);
  const drop = $('#drop');
  drop.addEventListener('dragover', e => { e.preventDefault(); drop.classList.add('over'); });
  drop.addEventListener('dragleave', () => drop.classList.remove('over'));
  drop.addEventListener('drop', e => { e.preventDefault(); drop.classList.remove('over'); addFiles(e.dataTransfer.files); });
  drop.addEventListener('click', () => {
    const i = document.createElement('input');
    i.type = 'file'; i.multiple = true;
    i.onchange = () => addFiles(i.files);
    i.click();
  });
  // Paste anywhere in the drawer: images become files; long pasted text into
  // the drop zone becomes a .txt log attachment.
  $('#drawer').addEventListener('paste', e => {
    const files = [...(e.clipboardData?.files || [])];
    if (files.length) {
      e.preventDefault();
      addFiles(files.map((f, i) => f.name && f.name !== 'image.png' ? f : new File([f], `screenshot-${Date.now()}-${i}.png`, { type: f.type })));
      return;
    }
    if (document.activeElement === drop) {
      const text = e.clipboardData?.getData('text');
      if (text) { e.preventDefault(); addFiles([new File([text], `log-${Date.now()}.txt`, { type: 'text/plain' })]); }
    }
  });
  $('#help-close').onclick = () => { $('#help').hidden = true; };
  $('#help').addEventListener('mousedown', e => { if (e.target.id === 'help') $('#help').hidden = true; });
}

function closeDialogs() {
  closeDrawer(null);
  $('#help').hidden = true;
}

// ---------- runs ----------
async function viewRuns() {
  if (!state.areas.length) state.areas = await api(`/projects/${P()}/areas`);
  const [runs, sumRes] = await Promise.all([api(`/projects/${P()}/runs`), api(`/projects/${P()}/summary`)]);
  const s = sumRes.summary;
  app.innerHTML = `
    <h1>Runs</h1>
    <div class="card">
      <h2>Start a test run</h2>
      <div class="row">
        <input id="r-build" type="text" placeholder="Build / commit under test (e.g. bf8bb49)" style="flex:1;min-width:220px" value="${esc(store.get('qa.lastBuild', ''))}">
        <input id="r-name" type="text" placeholder="Name (optional)">
        <select id="r-filter">
          <option value="fixed">Claude's fixes to retest — ${s.by_status.fixed}</option>
          <option value="needs-retest">Untested + fixed — ${s.needs_retest}</option>
          <option value="open">Open bugs — ${s.open_bugs}</option>
          <option value="all">Everything — ${s.total}</option>
          ${['P0', 'P1', 'P2', 'P3'].map(p => `<option value="priority:${p}">Priority ${p}</option>`).join('')}
          ${state.areas.map(a => `<option value="area:${esc(a.key)}">Area: ${esc(a.name)}</option>`).join('')}
        </select>
        <button class="btn primary" id="r-go">Start run</button>
      </div>
      ${s.by_status.fixed ? `<p class="muted">${s.by_status.fixed} case(s) were fixed by Claude and are waiting for your retest.</p>` : ''}
    </div>
    <div class="card" style="margin-top:16px">
      <table class="list">
        <tr><th>Run</th><th>Build</th><th>Progress</th><th>Results</th><th>Started</th><th></th></tr>
        ${runs.map(r => `<tr class="click" data-id="${r.id}">
          <td><b>${esc(r.name)}</b> <span class="muted">#${r.id} · ${esc(r.filter)}</span></td>
          <td class="mono">${esc(r.build)}</td>
          <td style="min-width:160px">${progressBar(r.progress)}<div class="muted">${r.progress.done}/${r.progress.total}</div></td>
          <td>${r.progress.pass ? `<span class="badge s-pass">${r.progress.pass} pass</span> ` : ''}${r.progress.fail ? `<span class="badge s-fail">${r.progress.fail} fail</span> ` : ''}${r.progress.blocked ? `<span class="badge s-blocked">${r.progress.blocked} blocked</span>` : ''}</td>
          <td class="muted">${ago(r.created_at)}</td>
          <td>${r.closed_at ? '<span class="badge s-skip">closed</span>' : ''}</td></tr>`).join('')}
      </table>
      ${runs.length ? '' : '<p class="empty">No runs yet.</p>'}
    </div>`;
  $('#r-filter').value = s.by_status.fixed ? 'fixed' : s.needs_retest ? 'needs-retest' : 'all';
  $('#r-go').onclick = async () => {
    const build = $('#r-build').value.trim();
    store.set('qa.lastBuild', build);
    try {
      const r = await api(`/projects/${P()}/runs`, { method: 'POST', body: { build, name: $('#r-name').value.trim(), filter: $('#r-filter').value } });
      location.hash = '#/runs/' + r.id;
    } catch (e) { toast(e.message, true); }
  };
  $$('tr.click', app).forEach(tr => tr.onclick = () => location.hash = '#/runs/' + tr.dataset.id);
  state.render = viewRuns;
}

function progressBar(p) {
  const w = n => p.total ? (100 * n / p.total).toFixed(1) + '%' : '0';
  return `<div class="progress"><span class="p" style="width:${w(p.pass)}"></span><span class="f" style="width:${w(p.fail)}"></span><span class="b" style="width:${w(p.blocked)}"></span><span class="s" style="width:${w(p.skip)}"></span></div>`;
}

// The run player: list on the left, one focused case on the right, keyboard driven.
const player = { run: null, idx: 0, show: 'todo' };

async function viewRun(id, q) {
  const run = await api(`/runs/${id}`);
  const keepCase = player.run && player.run.id === id ? visibleCases()[player.idx]?.id : null;
  player.run = run;
  if (q && q.get('show')) player.show = q.get('show');
  if (!player.run.progress.total) player.show = 'all';
  let vis = visibleCases();
  if (player.show === 'todo' && !vis.length && run.progress.done) { player.show = 'all'; vis = visibleCases(); }
  player.idx = keepCase ? Math.max(0, vis.findIndex(c => c.id === keepCase)) : 0;
  renderRun();
  state.render = () => viewRun(id);
}

function visibleCases() {
  const cs = player.run.cases;
  if (player.show === 'todo') return cs.filter(c => !c.result);
  if (player.show === 'failed') return cs.filter(c => c.result === 'fail' || c.result === 'blocked');
  return cs;
}

function renderRun() {
  const r = player.run;
  const vis = visibleCases();
  if (player.idx >= vis.length) player.idx = Math.max(0, vis.length - 1);
  const cur = vis[player.idx];
  const closed = !!r.closed_at;
  const tab = (k, label, n) => `<button class="chip ${player.show === k ? 'on' : ''}" data-show="${k}">${label} (${n})</button>`;
  const failed = r.cases.filter(c => c.result === 'fail' || c.result === 'blocked').length;
  const done = r.progress.done === r.progress.total;

  app.innerHTML = `
    <p><a href="#/runs">← Runs</a></p>
    <div class="card" style="margin-bottom:16px">
      <div class="row" style="margin:0">
        <h1 style="margin:0">${esc(r.name)}</h1>
        <span class="muted mono">${esc(r.build)}</span>
        <span class="muted">${esc(r.filter)}</span>
        ${closed ? '<span class="badge s-skip">closed</span>' : ''}
        <div class="spacer"></div>
        <span><b>${r.progress.done}</b>/${r.progress.total} done · <span style="color:var(--pass)">${r.progress.pass} pass</span> · <span style="color:var(--fail)">${r.progress.fail} fail</span> · ${r.progress.blocked} blocked · ${r.progress.skip} skip</span>
        <button class="btn" id="help-btn" title="Shortcuts">?</button>
        ${!closed ? `<button class="btn" id="close-run">${done ? 'Close run' : 'Close early'}</button>` : ''}
      </div>
      <div style="margin-top:10px">${progressBar(r.progress)}</div>
      ${done && !closed ? `<p style="margin:10px 0 0">🎉 All cases have a result. ${failed ? `${failed} failed — tell Claude to <b>pick up the bugs</b>.` : 'Everything passed.'}</p>` : ''}
    </div>
    <div class="player">
      <div class="card" style="padding:10px 0">
        <div class="chips" style="padding:0 12px 10px">
          ${tab('todo', 'To do', r.progress.total - r.progress.done)}${tab('failed', 'Failed', failed)}${tab('all', 'All', r.progress.total)}
        </div>
        <div class="rc-list">${vis.map((c, i) => `
          <div class="rc-item ${i === player.idx ? 'cur' : ''}" data-i="${i}">
            <span class="dot ${esc(c.result)}"></span>
            <span class="t" title="${esc(c.title)}">${esc(c.title)}</span>
            ${c.last_fix && (c.status === 'fixed') ? '<span class="badge s-fixed" title="Claude fixed this">fix</span>' : ''}
            ${prioBadge(c.priority)}
          </div>`).join('') || '<p class="empty">Nothing here.</p>'}</div>
      </div>
      <div class="card focus-card">${cur ? `
        <div class="row" style="margin-top:0">${prioBadge(cur.priority)} ${statusBadge(cur.status)} ${regressBadge(cur)} <span class="muted">${esc(cur.area_name)}</span>
          <div class="spacer"></div><span class="muted">${player.idx + 1} / ${vis.length}</span></div>
        <h2><a href="#/cases/${cur.id}">${esc(cur.title)}</a></h2>
        <div class="muted mono">#${cur.id} · ${esc(cur.key)}${cur.case_version !== cur.version ? ` · <b>updated since run started (v${cur.case_version}→v${cur.version})</b>` : ''}</div>
        ${caseBody(cur, cur.last_fix, null)}
        ${cur.result ? `<div class="row"><span class="muted">This run:</span> ${resultBadge(cur.result)} ${cur.remarks ? `<span>${esc(cur.remarks)}</span>` : ''}</div>` : ''}
        ${closed ? '' : `<div class="actions row">
          <button class="btn pass" data-r="pass">Pass <kbd>P</kbd></button>
          <button class="btn danger" data-r="fail">Fail <kbd>F</kbd></button>
          <button class="btn warn" data-r="blocked">Blocked <kbd>B</kbd></button>
          <button class="btn" data-r="skip">Skip <kbd>S</kbd></button>
          <div class="spacer"></div>
          <button class="btn" id="prev">← <kbd>K</kbd></button>
          <button class="btn" id="next"><kbd>J</kbd> →</button>
        </div>`}` : `<p class="empty">${player.show === 'todo' ? 'Every case in this run has a result.' : 'No cases in this view.'}</p>`}
      </div>
    </div>`;

  $$('[data-show]', app).forEach(b => b.onclick = () => { player.show = b.dataset.show; player.idx = 0; renderRun(); });
  $$('.rc-item', app).forEach(el => el.onclick = () => { player.idx = +el.dataset.i; renderRun(); });
  $$('[data-r]', app).forEach(b => b.onclick = () => runMark(b.dataset.r));
  $('#prev') && ($('#prev').onclick = () => move(-1));
  $('#next') && ($('#next').onclick = () => move(1));
  $('#help-btn').onclick = () => { $('#help').hidden = false; };
  const cr = $('#close-run');
  if (cr) cr.onclick = async () => {
    if (!done && !confirm(`${r.progress.total - r.progress.done} case(s) have no result. Close anyway?`)) return;
    try { await api(`/runs/${r.id}/close`, { method: 'POST' }); await viewRun(r.id); } catch (e) { toast(e.message, true); }
  };
  const curEl = $('.rc-item.cur', app);
  if (curEl) curEl.scrollIntoView({ block: 'nearest' });
}

function move(d) {
  const n = visibleCases().length;
  if (!n) return;
  player.idx = Math.min(n - 1, Math.max(0, player.idx + d));
  renderRun();
}

async function runMark(result) {
  const r = player.run;
  if (!r || r.closed_at) return;
  const vis = visibleCases();
  const cur = vis[player.idx];
  if (!cur) return;
  const updated = await mark(cur, result, r.id);
  if (!updated) return;
  const fresh = await api(`/runs/${r.id}`);
  player.run = fresh;
  // Advance: in "To do" the marked case disappears so the same index is the
  // next case; elsewhere step forward.
  if (player.show !== 'todo') player.idx = Math.min(player.idx + 1, visibleCases().length - 1);
  renderRun();
}

// ---------- keyboard ----------
document.addEventListener('keydown', e => {
  const tag = (e.target.tagName || '').toLowerCase();
  if (e.key === 'Escape') { closeDialogs(); return; }
  if (tag === 'input' || tag === 'textarea' || tag === 'select' || e.ctrlKey || e.metaKey || e.altKey) return;
  if (!$('#drawer').hidden) return;
  if (e.key === '?') { $('#help').hidden = !$('#help').hidden; return; }
  const inRun = parseHash().parts[0] === 'runs' && parseHash().parts[1] && player.run;
  if (!inRun) return;
  const k = e.key.toLowerCase();
  const actions = { j: () => move(1), k: () => move(-1), p: () => runMark('pass'), f: () => runMark('fail'), b: () => runMark('blocked'), s: () => runMark('skip'),
    n: () => { const i = visibleCases().findIndex((c, i) => i > player.idx && !c.result); if (i >= 0) { player.idx = i; renderRun(); } } };
  if (actions[k]) { e.preventDefault(); actions[k](); }
});

// ---------- live updates ----------
async function refreshView() {
  if (!$('#drawer').hidden || ['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName)) {
    state.deferredRefresh = true;
    return;
  }
  if (state.render) { try { await state.render(); } catch { /* next poll retries */ } }
}

async function poll() {
  if (!state.project) return;
  try {
    let evs = [];
    for (;;) { // page until caught up
      const page = await api(`/events?project=${P()}&since=${state.lastEvent}`);
      evs = evs.concat(page);
      if (page.length) state.lastEvent = page[page.length - 1].id;
      if (page.length < 200) break;
    }
    $('#live').classList.remove('off');
    if (!evs.length) return;
    const byClaude = evs.filter(e => e.actor === 'claude');
    if (byClaude.length) {
      const n = k => byClaude.filter(e => e.kind === k).length;
      const parts = [];
      if (n('fixed')) parts.push(`fixed ${n('fixed')} case(s) — ready to retest`);
      if (n('claimed')) parts.push(`started fixing ${n('claimed')}`);
      if (n('case_created')) parts.push(`added ${n('case_created')} case(s)`);
      if (n('case_updated')) parts.push(`updated ${n('case_updated')} case(s)`);
      if (n('comment')) parts.push(`left ${n('comment')} comment(s)`);
      if (parts.length) toast('Claude ' + parts.join(', '));
      state.areas = [];
    }
    await refreshView();
  } catch {
    $('#live').classList.add('off');
  }
}

// ---------- boot ----------
async function boot() {
  initDrawer();
  try {
    state.projects = await api('/projects');
  } catch (e) {
    app.innerHTML = `<div class="card empty">Cannot reach the server: ${esc(e.message)}</div>`;
    return;
  }
  if (!state.projects.find(p => p.key === state.project)) state.project = state.projects[0]?.key || '';
  const sel = $('#project');
  sel.innerHTML = state.projects.map(p => `<option value="${esc(p.key)}" ${p.key === state.project ? 'selected' : ''}>${esc(p.name)}</option>`).join('');
  sel.hidden = state.projects.length < 2;
  sel.onchange = async () => {
    state.project = sel.value; store.set('qa.project', state.project);
    state.areas = []; player.run = null; await syncCursor(); location.hash = '#/'; route();
  };
  await syncCursor();
  window.addEventListener('hashchange', route);
  await route();
  setInterval(poll, 3000);
}

async function syncCursor() {
  if (!state.project) return;
  try { state.lastEvent = (await api(`/projects/${P()}/summary`)).latest_event; } catch { /* poll will retry */ }
}

boot();
