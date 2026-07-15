package main

// statusPageHTML is the unauthenticated resource-page shell. It contains no
// account data; all data loads go through the management-key-gated API with
// the key the user enters in the browser.
const statusPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OpenCode Go Pool</title>
<style>
:root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, sans-serif; }
body { margin: 0; padding: 24px; background: Canvas; color: CanvasText; }
h1 { font-size: 20px; margin: 0 0 16px; }
.card { border: 1px solid color-mix(in srgb, CanvasText 18%, transparent); border-radius: 10px; padding: 16px; margin-bottom: 16px; }
table { border-collapse: collapse; width: 100%; font-size: 13px; }
th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid color-mix(in srgb, CanvasText 12%, transparent); vertical-align: top; }
th { font-weight: 600; }
input[type=password] { padding: 6px 10px; border-radius: 6px; border: 1px solid color-mix(in srgb, CanvasText 25%, transparent); min-width: 260px; background: transparent; color: inherit; }
button { padding: 6px 14px; border-radius: 6px; border: 1px solid color-mix(in srgb, CanvasText 25%, transparent); background: transparent; color: inherit; cursor: pointer; }
button:hover { background: color-mix(in srgb, CanvasText 8%, transparent); }
.ok { color: #16a34a; } .bad { color: #dc2626; } .warn { color: #d97706; }
.muted { opacity: .65; font-size: 12px; }
#error { color: #dc2626; margin: 8px 0; }
.bar { display: inline-block; width: 90px; height: 8px; border-radius: 4px; background: color-mix(in srgb, CanvasText 12%, transparent); vertical-align: middle; margin-right: 6px; }
.bar > i { display: block; height: 100%; border-radius: 4px; background: #16a34a; }
.bar > i.warn { background: #d97706; } .bar > i.bad { background: #dc2626; }
</style>
</head>
<body>
<h1>OpenCode Go Pool</h1>
<div class="card">
  <label>CPA Management Key
    <input type="password" id="key" placeholder="management key" autocomplete="off">
  </label>
  <button id="load">Load</button>
  <button id="refresh" title="Trigger a dashboard quota refresh">Refresh quota</button>
  <span class="muted" id="meta"></span>
  <div id="error"></div>
</div>
<div class="card"><div id="content" class="muted">Enter the management key and press Load.</div></div>
<script>
const BASE = '/v0/management/plugins/opencode-go-pool';
const keyInput = document.getElementById('key');
try { keyInput.value = sessionStorage.getItem('ocgp-key') || ''; } catch (_) {}
function headers() {
  const value = keyInput.value.trim();
  const h = { 'Content-Type': 'application/json' };
  if (value) h['Autho' + 'rization'] = 'Bearer ' + value;
  return h;
}
function fmtTime(iso) { return iso ? new Date(iso).toLocaleString() : ''; }
function pct(value) {
  if (value === undefined || value === null || value < 0) return '<span class="muted">n/a</span>';
  const cls = value >= 97 ? 'bad' : value >= 80 ? 'warn' : '';
  return '<span class="bar"><i class="' + cls + '" style="width:' + Math.min(100, value) + '%"></i></span>' + value + '%';
}
function windowCell(w) {
  if (!w) return '<span class="muted">n/a</span>';
  let out = pct(w.usage_percent);
  if (w.blocked_until) out += '<br><span class="bad">blocked until ' + fmtTime(w.blocked_until) + '</span>';
  else if (w.reset_at) out += '<br><span class="muted">resets ' + fmtTime(w.reset_at) + '</span>';
  return out;
}
async function api(path, options) {
  const resp = await fetch(BASE + path, Object.assign({ headers: headers() }, options || {}));
  if (!resp.ok && resp.status !== 202) throw new Error('HTTP ' + resp.status + (resp.status === 401 ? ' (bad management key?)' : ''));
  return resp.json();
}
async function load() {
  document.getElementById('error').textContent = '';
  try {
    try { sessionStorage.setItem('ocgp-key', keyInput.value.trim()); } catch (_) {}
    const data = await api('/status');
    document.getElementById('meta').textContent =
      'v' + data.version + ' · threshold ' + data.threshold_percent + '% · sticky sessions ' + data.sticky_bindings + ' · ' + fmtTime(data.generated_at);
    let html = '';
    if (data.config_error) html += '<div class="bad">config error: ' + data.config_error + '</div>';
    html += '<table><tr><th>Account</th><th>Health</th><th>5h</th><th>Weekly</th><th>Monthly</th><th>Requests</th><th>Dashboard</th><th></th></tr>';
    for (const acct of data.accounts) {
      const health = acct.blocked
        ? '<span class="bad">' + acct.blocked + '</span>'
        : '<span class="ok">healthy</span>';
      const dash = acct.dashboard_configured
        ? (acct.dashboard_error
            ? '<span class="warn">' + acct.dashboard_error + '</span>'
            : (acct.dashboard_refreshed_at ? '<span class="muted">' + fmtTime(acct.dashboard_refreshed_at) + '</span>' : '<span class="muted">pending</span>'))
        : '<span class="muted">not configured</span>';
      html += '<tr><td><b>' + acct.name + '</b><br><span class="muted">…' + acct.key_suffix + '</span></td>'
        + '<td>' + health + (acct.last_error ? '<br><span class="muted">' + acct.last_error + '</span>' : '') + '</td>'
        + '<td>' + windowCell(acct.windows['5h']) + '</td>'
        + '<td>' + windowCell(acct.windows['weekly']) + '</td>'
        + '<td>' + windowCell(acct.windows['monthly']) + '</td>'
        + '<td><span class="ok">' + acct.success + '</span> / <span class="bad">' + acct.failed + '</span>'
        + (acct.last_used_at ? '<br><span class="muted">' + fmtTime(acct.last_used_at) + '</span>' : '') + '</td>'
        + '<td>' + dash + '</td>'
        + '<td><button data-unblock="' + acct.name + '">Unblock</button> <button data-config="' + acct.name + '">Configure</button></td></tr>'
        + '<tr data-form="' + acct.name + '" style="display:none"><td colspan="8">'
        + '<div style="display:flex; gap:8px; flex-wrap:wrap; align-items:center; padding:4px 0">'
        + '<input data-ws="' + acct.name + '" placeholder="workspace ID (wrk_…)" value="' + (acct.workspace_id || '') + '" style="min-width:220px">'
        + '<input data-cookie="' + acct.name + '" type="password" placeholder="' + (acct.cookie_set ? 'auth cookie (saved — paste to replace)' : 'auth cookie value') + '" style="min-width:320px">'
        + '<button data-save="' + acct.name + '">Save</button>'
        + '<button data-clear="' + acct.name + '">Clear</button>'
        + '<span class="muted">From opencode.ai DevTools: the "auth" cookie; workspace ID is in the dashboard URL. One key = one account; both protocol entries share its global limit.</span>'
        + '</div></td></tr>';
    }
    html += '</table>';
    document.getElementById('content').innerHTML = html;
    const on = (selector, handler) => {
      for (const btn of document.querySelectorAll(selector)) btn.addEventListener('click', () => handler(btn).catch(err => {
        document.getElementById('error').textContent = String(err);
      }));
    };
    on('[data-unblock]', async btn => {
      await api('/unblock', { method: 'POST', body: JSON.stringify({ account: btn.dataset.unblock }) });
      load();
    });
    on('[data-config]', async btn => {
      const row = document.querySelector('[data-form="' + btn.dataset.config + '"]');
      row.style.display = row.style.display === 'none' ? '' : 'none';
    });
    on('[data-save]', async btn => {
      const name = btn.dataset.save;
      await api('/account-config', { method: 'POST', body: JSON.stringify({
        account: name,
        workspace_id: document.querySelector('[data-ws="' + name + '"]').value.trim(),
        cookie: document.querySelector('[data-cookie="' + name + '"]').value.trim(),
      })});
      setTimeout(load, 1200);
    });
    on('[data-clear]', async btn => {
      await api('/account-config', { method: 'POST', body: JSON.stringify({ account: btn.dataset.clear, clear: true }) });
      load();
    });
  } catch (err) {
    document.getElementById('error').textContent = String(err);
  }
}
document.getElementById('load').addEventListener('click', load);
document.getElementById('refresh').addEventListener('click', async () => {
  try { await api('/refresh', { method: 'POST', body: '{}' }); setTimeout(load, 1500); }
  catch (err) { document.getElementById('error').textContent = String(err); }
});
keyInput.addEventListener('keydown', e => { if (e.key === 'Enter') load(); });
if (keyInput.value) load();
</script>
</body>
</html>`
