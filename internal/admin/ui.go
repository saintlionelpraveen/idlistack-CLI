package admin

import (
	"fmt"
)

// RenderAdminHTML produces the complete, self-contained single-page Admin Dashboard HTML
func RenderAdminHTML(nodeHost string, port int) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>IdliStack Admin — %s</title>
  <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%%2338bdf8' stroke-width='2'><path d='M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5'/></svg>">
  <style>
    :root {
      --bg: #090d16;
      --card-bg: #0f172a;
      --card-border: #1e293b;
      --card-hover: #162036;
      --text: #f8fafc;
      --text-muted: #94a3b8;
      --accent: #38bdf8;
      --accent-hover: #0284c7;
      --success: #10b981;
      --warning: #f59e0b;
      --danger: #ef4444;
      --indigo: #6366f1;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
    body { background-color: var(--bg); color: var(--text); min-height: 100vh; display: flex; flex-direction: column; }
    
    /* Top Header */
    header { background: rgba(15, 23, 42, 0.85); backdrop-filter: blur(12px); border-bottom: 1px solid var(--card-border); padding: 0.85rem 2rem; display: flex; justify-content: space-between; align-items: center; position: sticky; top: 0; z-index: 50; }
    .brand { display: flex; align-items: center; gap: 0.85rem; }
    .brand-logo { width: 36px; height: 36px; background: linear-gradient(135deg, #0284c7, #6366f1); border-radius: 9px; display: flex; align-items: center; justify-content: center; box-shadow: 0 0 20px rgba(56, 189, 248, 0.3); font-weight: bold; font-size: 1.2rem; }
    .brand-title { font-size: 1.15rem; font-weight: 700; letter-spacing: -0.02em; display: flex; align-items: center; gap: 0.5rem; }
    .badge { font-size: 0.72rem; padding: 0.2rem 0.55rem; border-radius: 9999px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
    .badge-primary { background: rgba(56, 189, 248, 0.15); color: var(--accent); border: 1px solid rgba(56, 189, 248, 0.3); }
    .badge-success { background: rgba(16, 185, 129, 0.15); color: var(--success); border: 1px solid rgba(16, 185, 129, 0.3); }
    .badge-warning { background: rgba(245, 158, 11, 0.15); color: var(--warning); border: 1px solid rgba(245, 158, 11, 0.3); }
    .badge-danger { background: rgba(239, 68, 68, 0.15); color: var(--danger); border: 1px solid rgba(239, 68, 68, 0.3); }

    .header-actions { display: flex; align-items: center; gap: 0.75rem; }
    button { background: var(--card-bg); color: var(--text); border: 1px solid var(--card-border); padding: 0.5rem 0.9rem; border-radius: 7px; font-size: 0.82rem; font-weight: 600; cursor: pointer; transition: all 0.15s ease; display: inline-flex; align-items: center; gap: 0.45rem; }
    button:hover { background: var(--card-hover); border-color: #334155; }
    button.primary { background: #0284c7; border-color: #38bdf8; color: #fff; }
    button.primary:hover { background: #0369a1; }
    button.danger { background: rgba(239, 68, 68, 0.15); border-color: rgba(239, 68, 68, 0.4); color: var(--danger); }
    button.danger:hover { background: rgba(239, 68, 68, 0.25); }

    /* Main Container */
    main { max-width: 1400px; width: 100%%; margin: 0 auto; padding: 1.75rem 2rem; flex: 1; display: flex; flex-direction: column; gap: 1.5rem; }

    /* Stat Cards Grid */
    .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 1rem; }
    .stat-card { background: var(--card-bg); border: 1px solid var(--card-border); border-radius: 12px; padding: 1.1rem 1.3rem; display: flex; flex-direction: column; gap: 0.4rem; box-shadow: 0 4px 15px rgba(0, 0, 0, 0.2); transition: border-color 0.2s; }
    .stat-card:hover { border-color: #334155; }
    .stat-label { font-size: 0.78rem; color: var(--text-muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.03em; display: flex; align-items: center; justify-content: space-between; }
    .stat-value { font-size: 1.6rem; font-weight: 800; color: #fff; }
    .stat-sub { font-size: 0.78rem; color: var(--text-muted); }

    /* Tabs */
    .tabs { display: flex; border-bottom: 1px solid var(--card-border); gap: 1.5rem; margin-top: 0.5rem; }
    .tab-btn { background: none; border: none; border-bottom: 2px solid transparent; border-radius: 0; padding: 0.65rem 0.25rem; font-size: 0.9rem; font-weight: 600; color: var(--text-muted); cursor: pointer; }
    .tab-btn:hover { color: var(--text); }
    .tab-btn.active { color: var(--accent); border-bottom-color: var(--accent); }

    /* Content Cards */
    .panel { background: var(--card-bg); border: 1px solid var(--card-border); border-radius: 12px; overflow: hidden; box-shadow: 0 6px 20px rgba(0,0,0,0.25); }
    .panel-header { padding: 1rem 1.4rem; border-bottom: 1px solid var(--card-border); display: flex; justify-content: space-between; align-items: center; background: rgba(15, 23, 42, 0.6); }
    .panel-title { font-size: 0.95rem; font-weight: 700; display: flex; align-items: center; gap: 0.5rem; }

    /* Tables */
    table { width: 100%%; border-collapse: collapse; text-align: left; font-size: 0.85rem; }
    th { background: rgba(15, 23, 42, 0.9); padding: 0.85rem 1.2rem; font-weight: 600; color: var(--text-muted); border-bottom: 1px solid var(--card-border); }
    td { padding: 0.95rem 1.2rem; border-bottom: 1px solid rgba(30, 41, 59, 0.7); vertical-align: middle; }
    tr:last-child td { border-bottom: none; }
    tr:hover td { background: rgba(30, 41, 59, 0.35); }

    /* App details */
    .app-title { font-weight: 700; color: #fff; display: flex; align-items: center; gap: 0.5rem; }
    .app-sub { font-size: 0.75rem; color: var(--text-muted); font-family: monospace; }
    .url-link { color: var(--accent); text-decoration: none; font-family: monospace; font-size: 0.8rem; background: rgba(56, 189, 248, 0.08); padding: 0.2rem 0.5rem; border-radius: 5px; border: 1px solid rgba(56, 189, 248, 0.2); display: inline-flex; align-items: center; gap: 0.3rem; }
    .url-link:hover { text-decoration: underline; background: rgba(56, 189, 248, 0.15); }

    /* Modals */
    .modal-overlay { position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.75); backdrop-filter: blur(4px); display: none; align-items: center; justify-content: center; z-index: 100; }
    .modal { background: #0f172a; border: 1px solid var(--card-border); border-radius: 14px; width: 90%%; max-width: 720px; box-shadow: 0 20px 40px rgba(0,0,0,0.5); overflow: hidden; display: flex; flex-direction: column; max-height: 85vh; }
    .modal-header { padding: 1.1rem 1.5rem; border-bottom: 1px solid var(--card-border); display: flex; justify-content: space-between; align-items: center; background: rgba(30, 41, 59, 0.4); }
    .modal-body { padding: 1.5rem; overflow-y: auto; display: flex; flex-direction: column; gap: 1rem; }
    .modal-footer { padding: 1rem 1.5rem; border-top: 1px solid var(--card-border); display: flex; justify-content: flex-end; gap: 0.75rem; background: rgba(30, 41, 59, 0.3); }

    /* Terminal Logs */
    .term-window { background: #040711; border: 1px solid #1e293b; border-radius: 8px; padding: 1rem; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 0.78rem; line-height: 1.5; color: #e2e8f0; height: 420px; overflow-y: auto; white-space: pre-wrap; word-break: break-all; }

    /* Inputs */
    .form-group { display: flex; flex-direction: column; gap: 0.4rem; }
    .form-label { font-size: 0.8rem; font-weight: 600; color: var(--text-muted); }
    .form-control { background: #090d16; border: 1px solid var(--card-border); border-radius: 7px; padding: 0.6rem 0.85rem; color: #fff; font-size: 0.85rem; outline: none; }
    .form-control:focus { border-color: var(--accent); box-shadow: 0 0 0 2px rgba(56, 189, 248, 0.2); }

    /* Toast */
    #toast { position: fixed; bottom: 2rem; right: 2rem; background: #0f172a; border: 1px solid var(--accent); padding: 0.85rem 1.25rem; border-radius: 9px; box-shadow: 0 10px 25px rgba(0,0,0,0.5); font-size: 0.85rem; font-weight: 600; display: none; z-index: 200; }
  </style>
</head>
<body>

  <header>
    <div class="brand">
      <div class="brand-logo">🍲</div>
      <div>
        <div class="brand-title">IdliStack <span class="badge badge-primary">Admin</span></div>
        <div style="font-size: 0.72rem; color: var(--text-muted);">Cluster Host: <strong>%s</strong></div>
      </div>
    </div>
    <div class="header-actions">
      <span id="connStatus" class="badge badge-success">● Connected</span>
      <button onclick="openCreateUserModal()">+ Create Tenant User</button>
      <button onclick="cleanupZombies()">🧹 Clean Zombie Releases</button>
      <button onclick="loadAllData()" class="primary">↻ Refresh</button>
    </div>
  </header>

  <main>
    <!-- Top Stat Cards -->
    <div class="stats-grid">
      <div class="stat-card">
        <div class="stat-label">Total Applications <span>📦</span></div>
        <div class="stat-value" id="statApps">-</div>
        <div class="stat-sub">Across all namespaces</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Pods Running <span>🚀</span></div>
        <div class="stat-value" id="statPods">-</div>
        <div class="stat-sub" id="statPodCapacity">Capacity: 110 pods</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">CPU Capacity <span>⚡</span></div>
        <div class="stat-value" id="statCPU">-</div>
        <div class="stat-sub" id="statNodeVersion">K3s v1.36.5</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Allocatable Memory <span>💾</span></div>
        <div class="stat-value" id="statMem">-</div>
        <div class="stat-sub">Local K3s Cluster</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Frappe Tenants <span>👥</span></div>
        <div class="stat-value" id="statUsers">-</div>
        <div class="stat-sub">Teleport RBAC Scoped</div>
      </div>
    </div>

    <!-- Navigation Tabs -->
    <div class="tabs">
      <button class="tab-btn active" onclick="switchTab('apps')">📦 Applications & Deployments</button>
      <button class="tab-btn" onclick="switchTab('node')">🖥️ K3s Node & Infrastructure</button>
      <button class="tab-btn" onclick="switchTab('users')">👥 Tenants & IAM</button>
      <button class="tab-btn" onclick="switchTab('events')">⚠️ Warning Events & Audit</button>
    </div>

    <!-- Tab 1: Applications -->
    <div id="tab-apps" class="panel">
      <div class="panel-header">
        <div class="panel-title">Deployed Applications & Workspaces</div>
        <input type="text" id="appSearch" placeholder="Filter by name or owner..." class="form-control" style="width: 250px; font-size: 0.78rem;" oninput="filterApps()">
      </div>
      <div style="overflow-x: auto;">
        <table>
          <thead>
            <tr>
              <th>Application</th>
              <th>Namespace</th>
              <th>Owner</th>
              <th>Status</th>
              <th>Replicas</th>
              <th>Public URL</th>
              <th>Age</th>
              <th>Control Actions</th>
            </tr>
          </thead>
          <tbody id="appsTableBody">
            <tr><td colspan="8" style="text-align: center; color: var(--text-muted);">Loading deployments...</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Tab 2: Node & Infrastructure -->
    <div id="tab-node" class="panel" style="display: none;">
      <div class="panel-header">
        <div class="panel-title">Local K3s Node Overview</div>
      </div>
      <div style="padding: 1.5rem; display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1.5rem;">
        <div class="panel" style="padding: 1.25rem;">
          <h4 style="margin-bottom: 1rem; color: var(--accent);">Node Properties</h4>
          <div style="display: flex; flex-direction: column; gap: 0.6rem; font-size: 0.85rem;">
            <div><strong>Node Name:</strong> <span id="nodeName">-</span></div>
            <div><strong>External IPv4:</strong> <span id="nodeExtIP" style="color: var(--accent); font-weight: bold;">-</span></div>
            <div><strong>Internal IPv4:</strong> <span id="nodeIntIP">-</span></div>
            <div><strong>OS & Image:</strong> <span id="nodeOS">-</span></div>
            <div><strong>Kernel Version:</strong> <span id="nodeKernel">-</span></div>
            <div><strong>Container Runtime:</strong> <span id="nodeRuntime">-</span></div>
          </div>
        </div>
        <div class="panel" style="padding: 1.25rem;">
          <h4 style="margin-bottom: 1rem; color: var(--success);">Node Conditions</h4>
          <div id="nodeConditions" style="display: flex; flex-direction: column; gap: 0.6rem; font-size: 0.85rem;">
            Loading conditions...
          </div>
        </div>
      </div>
    </div>

    <!-- Tab 3: Users & Tenants -->
    <div id="tab-users" class="panel" style="display: none;">
      <div class="panel-header">
        <div class="panel-title">Frappe Identity & Developer Tenants</div>
        <a id="kcAdminLink" href="http://%s:8000/app/user" target="_blank" class="url-link">↗ Open Frappe Desk</a>
      </div>
      <table>
        <thead>
          <tr>
            <th>Username</th>
            <th>Email</th>
            <th>RBAC Scope / Role</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody id="usersTableBody">
          <tr><td colspan="4" style="text-align: center; color: var(--text-muted);">Loading users...</td></tr>
        </tbody>
      </table>
    </div>

    <!-- Tab 4: Warning Events -->
    <div id="tab-events" class="panel" style="display: none;">
      <div class="panel-header">
        <div class="panel-title">Cluster Warning & Diagnostic Events</div>
      </div>
      <table>
        <thead>
          <tr>
            <th>Namespace</th>
            <th>Object</th>
            <th>Reason</th>
            <th>Message</th>
            <th>Time</th>
          </tr>
        </thead>
        <tbody id="eventsTableBody">
          <tr><td colspan="5" style="text-align: center; color: var(--text-muted);">Loading cluster events...</td></tr>
        </tbody>
      </table>
    </div>

  </main>

  <!-- Log Viewer Modal -->
  <div id="logsModal" class="modal-overlay">
    <div class="modal" style="max-width: 900px;">
      <div class="modal-header">
        <div class="panel-title">📄 Container Logs — <span id="logAppTitle">app</span></div>
        <button onclick="closeModal('logsModal')">✕</button>
      </div>
      <div class="modal-body">
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <input type="text" id="logSearch" placeholder="Filter logs..." class="form-control" style="width: 250px;" oninput="filterLogs()">
          <div>
            <button onclick="refreshLogs()">↻ Refresh</button>
            <button onclick="copyLogs()">📋 Copy Logs</button>
          </div>
        </div>
        <div class="term-window" id="logContent">Loading container logs...</div>
      </div>
    </div>
  </div>

  <!-- Scale Modal -->
  <div id="scaleModal" class="modal-overlay">
    <div class="modal" style="max-width: 450px;">
      <div class="modal-header">
        <div class="panel-title">⚙️ Scale Replicas — <span id="scaleAppTitle">app</span></div>
        <button onclick="closeModal('scaleModal')">✕</button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label class="form-label">Desired Replicas (0 to 10):</label>
          <input type="number" id="scaleInput" min="0" max="10" class="form-control" value="1">
          <small style="color: var(--text-muted);">Setting replicas to 0 stops the application without deleting its configuration.</small>
        </div>
      </div>
      <div class="modal-footer">
        <button onclick="closeModal('scaleModal')">Cancel</button>
        <button class="primary" onclick="submitScale()">Apply Scale</button>
      </div>
    </div>
  </div>

  <!-- Create User Modal -->
  <div id="createUserModal" class="modal-overlay">
    <div class="modal" style="max-width: 480px;">
      <div class="modal-header">
        <div class="panel-title">👤 Create Tenant User</div>
        <button onclick="closeModal('createUserModal')">✕</button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label class="form-label">Username:</label>
          <input type="text" id="newUsername" placeholder="e.g. alice" class="form-control">
        </div>
        <div class="form-group">
          <label class="form-label">Password:</label>
          <input type="password" id="newPassword" placeholder="Strong password" class="form-control">
        </div>
        <div class="form-group">
          <label class="form-label">Email (optional):</label>
          <input type="email" id="newEmail" placeholder="alice@example.com" class="form-control">
        </div>
        <div class="form-group">
          <label class="form-label">Role:</label>
          <select id="newRole" class="form-control">
            <option value="developer">Developer (Namespace-Scoped)</option>
            <option value="admin">Cluster Administrator</option>
          </select>
        </div>
        <div class="form-group">
          <label class="form-label">Project Namespace (optional):</label>
          <input type="text" id="newNamespace" placeholder="e.g. idlistack-myapp" class="form-control">
        </div>
      </div>
      <div class="modal-footer">
        <button onclick="closeModal('createUserModal')">Cancel</button>
        <button class="primary" onclick="submitCreateUser()">Create User</button>
      </div>
    </div>
  </div>

  <div id="toast">Action performed successfully</div>

  <script>
    let currentLogsNs = '';
    let currentLogsApp = '';
    let rawLogLines = '';
    let appsData = [];

    function showToast(msg, isErr = false) {
      const t = document.getElementById('toast');
      t.innerText = msg;
      t.style.borderColor = isErr ? 'var(--danger)' : 'var(--accent)';
      t.style.display = 'block';
      setTimeout(() => { t.style.display = 'none'; }, 3500);
    }

    function switchTab(tab) {
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('[id^="tab-"]').forEach(p => p.style.display = 'none');
      document.getElementById('tab-' + tab).style.display = 'block';
      event.target.classList.add('active');
    }

    function closeModal(id) {
      document.getElementById(id).style.display = 'none';
    }

    async function loadCluster() {
      try {
        const res = await fetch('/api/cluster');
        const data = await res.json();
        if (data.node) {
          const n = data.node;
          document.getElementById('statCPU').innerText = n.cpuCapacity || '1 Core';
          document.getElementById('statMem').innerText = n.memCapacity || '1 GiB';
          document.getElementById('statPods').innerText = data.totalPods;
          document.getElementById('statPodCapacity').innerText = 'Capacity: ' + n.podsCapacity + ' pods';
          document.getElementById('statNodeVersion').innerText = n.version;

          document.getElementById('nodeName').innerText = n.name;
          document.getElementById('nodeExtIP').innerText = n.externalIp || '%s';
          document.getElementById('nodeIntIP').innerText = n.internalIp;
          document.getElementById('nodeOS').innerText = n.osImage;
          document.getElementById('nodeKernel').innerText = n.kernelVersion;
          document.getElementById('nodeRuntime').innerText = n.containerRuntime;

          let condHtml = '';
          for (let k in n.conditions) {
            const val = n.conditions[k];
            const isGood = (k === 'Ready' && val === 'True') || (k !== 'Ready' && val === 'False');
            const badgeClass = isGood ? 'badge-success' : 'badge-danger';
            condHtml += '<div style="display:flex; justify-content:space-between; align-items:center;"><span>' + k + '</span><span class="badge ' + badgeClass + '">' + val + '</span></div>';
          }
          document.getElementById('nodeConditions').innerHTML = condHtml;
        }
      } catch (e) {
        console.error(e);
      }
    }

    async function loadApps() {
      try {
        const res = await fetch('/api/apps');
        const data = await res.json();
        appsData = data.apps || [];
        document.getElementById('statApps').innerText = appsData.length;
        renderApps(appsData);
      } catch (e) {
        console.error(e);
      }
    }

    function renderApps(apps) {
      const tbody = document.getElementById('appsTableBody');
      if (!apps || apps.length === 0) {
        tbody.innerHTML = '<tr><td colspan="8" style="text-align: center; color: var(--text-muted); padding: 2rem;">No tenant applications found yet. Deploy with "idlistack up" to see them here!</td></tr>';
        return;
      }
      let html = '';
      for (let app of apps) {
        let badgeClass = 'badge-primary';
        if (app.status === 'Running') badgeClass = 'badge-success';
        else if (app.status === 'Degraded') badgeClass = 'badge-warning';
        else if (app.status === 'Stopped') badgeClass = 'badge-danger';

        const urlDisplay = app.url ? '<a href="' + app.url + '" target="_blank" class="url-link">↗ ' + app.url + '</a>' : '<span style="color:var(--text-muted);">-</span>';

        html += '<tr>' +
          '<td><div class="app-title">' + app.name + '</div><div class="app-sub">' + (app.image || 'default') + '</div></td>' +
          '<td><code>' + app.namespace + '</code></td>' +
          '<td><span class="badge badge-primary">👤 ' + app.owner + '</span></td>' +
          '<td><span class="badge ' + badgeClass + '">' + app.status + '</span></td>' +
          '<td><strong>' + app.replicasReady + '</strong> / ' + app.replicasDesired + '</td>' +
          '<td>' + urlDisplay + '</td>' +
          '<td>' + (app.created ? new Date(app.created).toLocaleDateString() : '-') + '</td>' +
          '<td><div style="display:flex; gap:0.4rem;">' +
            '<button onclick="restartApp(\'' + app.namespace + '\', \'' + app.name + '\')" title="Restart">🔄</button>' +
            '<button onclick="openScaleModal(\'' + app.namespace + '\', \'' + app.name + '\', ' + app.replicasDesired + ')" title="Scale">⚙️ Scale</button>' +
            '<button onclick="openLogsModal(\'' + app.namespace + '\', \'' + app.name + '\')" title="Logs">📄 Logs</button>' +
            '<button class="danger" onclick="deleteApp(\'' + app.namespace + '\', \'' + app.name + '\')" title="Delete">🗑️</button>' +
          '</div></td>' +
        '</tr>';
      }
      tbody.innerHTML = html;
    }

    function filterApps() {
      const q = document.getElementById('appSearch').value.toLowerCase();
      const filtered = appsData.filter(a => a.name.toLowerCase().includes(q) || a.owner.toLowerCase().includes(q) || a.namespace.toLowerCase().includes(q));
      renderApps(filtered);
    }

    async function restartApp(ns, app) {
      if (!confirm('Restart deployment "' + app + '" in ' + ns + '?')) return;
      try {
        const res = await fetch('/api/apps/restart', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ namespace: ns, app: app })
        });
        const d = await res.json();
        if (d.success) {
          showToast('Restart initiated for ' + app);
          setTimeout(loadApps, 1500);
        } else {
          showToast(d.error || 'Restart failed', true);
        }
      } catch (e) {
        showToast('Error: ' + e, true);
      }
    }

    let scaleTarget = { ns: '', app: '' };
    function openScaleModal(ns, app, curr) {
      scaleTarget = { ns, app };
      document.getElementById('scaleAppTitle').innerText = app;
      document.getElementById('scaleInput').value = curr;
      document.getElementById('scaleModal').style.display = 'flex';
    }

    async function submitScale() {
      const count = parseInt(document.getElementById('scaleInput').value);
      closeModal('scaleModal');
      try {
        const res = await fetch('/api/apps/scale', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ namespace: scaleTarget.ns, app: scaleTarget.app, replicas: count })
        });
        const d = await res.json();
        if (d.success) {
          showToast('Scaled ' + scaleTarget.app + ' to ' + count + ' replicas');
          setTimeout(loadApps, 1500);
        } else {
          showToast(d.error || 'Scale failed', true);
        }
      } catch (e) {
        showToast('Error: ' + e, true);
      }
    }

    async function deleteApp(ns, app) {
      if (!confirm('WARNING: Are you sure you want to completely delete application "' + app + '" and namespace "' + ns + '"?')) return;
      try {
        const res = await fetch('/api/apps/delete', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ namespace: ns, app: app })
        });
        const d = await res.json();
        if (d.success) {
          showToast('Deleted ' + app);
          setTimeout(loadApps, 1500);
        } else {
          showToast(d.error || 'Delete failed', true);
        }
      } catch (e) {
        showToast('Error: ' + e, true);
      }
    }

    function openLogsModal(ns, app) {
      currentLogsNs = ns;
      currentLogsApp = app;
      document.getElementById('logAppTitle').innerText = app + ' (' + ns + ')';
      document.getElementById('logsModal').style.display = 'flex';
      refreshLogs();
    }

    async function refreshLogs() {
      const term = document.getElementById('logContent');
      term.innerText = 'Fetching live logs...';
      try {
        const res = await fetch('/api/apps/logs?namespace=' + encodeURIComponent(currentLogsNs) + '&app=' + encodeURIComponent(currentLogsApp) + '&tail=150');
        const d = await res.json();
        rawLogLines = d.logs || 'No logs recorded for this container.';
        term.innerText = rawLogLines;
        term.scrollTop = term.scrollHeight;
      } catch (e) {
        term.innerText = 'Failed to fetch logs: ' + e;
      }
    }

    function filterLogs() {
      const q = document.getElementById('logSearch').value.toLowerCase();
      if (!q) {
        document.getElementById('logContent').innerText = rawLogLines;
        return;
      }
      const lines = rawLogLines.split('\n');
      const filtered = lines.filter(l => l.toLowerCase().includes(q));
      document.getElementById('logContent').innerText = filtered.join('\n');
    }

    function copyLogs() {
      navigator.clipboard.writeText(rawLogLines);
      showToast('Logs copied to clipboard');
    }

    async function loadUsers() {
      try {
        const res = await fetch('/api/users');
        const data = await res.json();
        const users = data.users || [];
        document.getElementById('statUsers').innerText = users.length;
        const tbody = document.getElementById('usersTableBody');
        let html = '';
        for (let u of users) {
          html += '<tr>' +
            '<td><strong>' + u.username + '</strong></td>' +
            '<td>' + (u.email || '-') + '</td>' +
            '<td><span class="badge ' + (u.role === 'cluster-admin' ? 'badge-primary' : 'badge-success') + '">' + u.role + '</span></td>' +
            '<td><span class="badge badge-success">Active</span></td>' +
          '</tr>';
        }
        tbody.innerHTML = html;
      } catch (e) {
        console.error(e);
      }
    }

    function openCreateUserModal() {
      document.getElementById('createUserModal').style.display = 'flex';
    }

    async function submitCreateUser() {
      const username = document.getElementById('newUsername').value.trim();
      const password = document.getElementById('newPassword').value.trim();
      const email = document.getElementById('newEmail').value.trim();
      const role = document.getElementById('newRole').value;
      const namespace = document.getElementById('newNamespace').value.trim();

      if (!username || !password) {
        alert('Username and password are required');
        return;
      }

      closeModal('createUserModal');
      showToast('Creating tenant user...');

      try {
        const res = await fetch('/api/users/create', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username, password, email, role, namespace })
        });
        const d = await res.json();
        if (d.success) {
          showToast('User ' + username + ' created successfully!');
          loadUsers();
        } else {
          showToast(d.error || 'Failed to create user', true);
        }
      } catch (e) {
        showToast('Error: ' + e, true);
      }
    }

    async function loadEvents() {
      try {
        const res = await fetch('/api/events');
        const data = await res.json();
        const events = data.events || [];
        const tbody = document.getElementById('eventsTableBody');
        if (events.length === 0) {
          tbody.innerHTML = '<tr><td colspan="5" style="text-align: center; color: var(--success); padding: 1.5rem;">✓ No warning events in the cluster. Everything is healthy!</td></tr>';
          return;
        }
        let html = '';
        for (let ev of events) {
          html += '<tr>' +
            '<td><code>' + ev.namespace + '</code></td>' +
            '<td>' + ev.object + '</td>' +
            '<td><span class="badge badge-warning">' + ev.reason + '</span></td>' +
            '<td style="color: #f1f5f9;">' + ev.message + '</td>' +
            '<td style="color: var(--text-muted);">' + (ev.age ? new Date(ev.age).toLocaleTimeString() : '-') + '</td>' +
          '</tr>';
        }
        tbody.innerHTML = html;
      } catch (e) {
        console.error(e);
      }
    }

    async function cleanupZombies() {
      if (!confirm('Purge any stuck or pending Helm releases across all namespaces?')) return;
      try {
        const res = await fetch('/api/system/cleanup', { method: 'POST' });
        const d = await res.json();
        if (d.success) {
          showToast('Cleaned up: ' + (d.cleaned.length ? d.cleaned.join(', ') : 'none found'));
          loadApps();
        }
      } catch (e) {
        showToast('Error: ' + e, true);
      }
    }

    function loadAllData() {
      loadCluster();
      loadApps();
      loadUsers();
      loadEvents();
    }

    loadAllData();
    setInterval(loadAllData, 10000);
  </script>
</body>
</html>`, nodeHost, nodeHost, nodeHost, nodeHost)
}
