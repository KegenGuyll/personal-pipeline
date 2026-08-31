"use strict";

const LS_READ = "dash.readToken";
const LS_ADMIN = "dash.adminToken";

const $ = (sel) => document.querySelector(sel);

const banner = $("#banner");
const addError = $("#add-error");
const onboardError = $("#onboard-error");
const onboardResult = $("#onboard-result");

function getToken(key) {
  return localStorage.getItem(key) || "";
}
function setToken(key, val) {
  if (val) localStorage.setItem(key, val);
  else localStorage.removeItem(key);
}

function showBanner(msg) {
  banner.textContent = msg;
  banner.hidden = false;
}
function hideBanner() {
  banner.hidden = true;
  banner.textContent = "";
}

function showAddError(msg) {
  addError.textContent = msg;
  addError.hidden = false;
}
function hideAddError() {
  addError.hidden = true;
  addError.textContent = "";
}

function showOnboardError(msg) {
  onboardError.textContent = msg;
  onboardError.hidden = false;
  onboardResult.hidden = true;
}
function hideOnboardError() {
  onboardError.hidden = true;
  onboardError.textContent = "";
}

function showOnboardResult(html) {
  onboardResult.innerHTML = html;
  onboardResult.hidden = false;
}

async function api(path, { method = "GET", token = "", body } = {}) {
  const headers = {};
  if (token) headers["Authorization"] = "Bearer " + token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try { data = await res.json(); } catch (_) { /* non-JSON */ }
  return { status: res.status, data };
}

function openSettings() {
  $("#settings").hidden = false;
}

function formatTime(ts) {
  if (!ts) return "";
  const d = new Date(ts);
  return isNaN(d) ? "" : d.toLocaleString();
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function statusBadge(status) {
  const s = (status || "started").toLowerCase();
  return `<span class="badge ${esc(s)}">${esc(status || "started")}</span>`;
}

function render(services) {
  const body = $("#services-body");
  body.innerHTML = "";
  $("#services-empty").hidden = services.length > 0;

  for (const svc of services) {
    const tr = document.createElement("tr");
    const version = svc.tag
      ? `<code>${esc(svc.tag)}</code>`
      : '<span class="muted">never deployed</span>';
    const access = svc.access
      ? `<span class="badge ${esc(svc.access)}">${esc(svc.access)}</span>`
      : '<span class="muted">—</span>';

    let last = '<span class="muted">—</span>';
    if (svc.last_deploy) {
      const ld = svc.last_deploy;
      last = `${statusBadge(ld.status)} <span class="muted">${esc(formatTime(ld.ts))}</span>`;
    }

    tr.innerHTML = `
      <td><strong>${esc(svc.name)}</strong></td>
      <td>${version}</td>
      <td class="mono muted">${esc(svc.image || "—")}</td>
      <td>${access}</td>
      <td>${last}</td>`;
    body.appendChild(tr);
  }
}

async function loadServices() {
  hideBanner();
  const token = getToken(LS_READ);
  const { status, data } = await api("/services", { token });

  if (status === 401) {
    showBanner("Unauthorized — open Settings and enter the correct read token.");
    openSettings();
    return;
  }
  if (status === 404) {
    showBanner("Service list is disabled — set READ_TOKEN on the server.");
    return;
  }
  if (status !== 200) {
    showBanner("Failed to load services: " + (data && data.error ? data.error : status));
    return;
  }
  render(data.services || []);
}

async function submitAdd(e) {
  e.preventDefault();
  hideAddError();
  hideBanner();

  const adminToken = getToken(LS_ADMIN);
  if (!adminToken) {
    showBanner("Enter the admin token in Settings before adding a service.");
    openSettings();
    return;
  }

  const form = e.target;
  const body = {
    name: form.name.value.trim(),
    image: form.image.value.trim(),
    port: parseInt(form.port.value, 10) || 0,
    hostname: form.hostname.value.trim() || undefined,
  };

  const { status, data } = await api("/services", {
    method: "POST", token: adminToken, body,
  });

  if (status === 201) {
    form.reset();
    form.port.value = "3000";
    await loadServices();
    return;
  }
  if (status === 401) {
    showBanner("Unauthorized — open Settings and enter the correct admin token.");
    openSettings();
    return;
  }
  if (status === 404) {
    showBanner("Adding services is disabled — set ADMIN_TOKEN on the server.");
    return;
  }
  showAddError(data && data.error ? data.error : "Failed to add service (status " + status + ")");
}

async function submitOnboard(e) {
  e.preventDefault();
  hideOnboardError();
  hideBanner();

  const adminToken = getToken(LS_ADMIN);
  if (!adminToken) {
    showBanner("Enter the admin token in Settings before onboarding a project.");
    openSettings();
    return;
  }

  const form = e.target;
  const repoSelect = $("#repo");
  const repo = repoSelect.value === "__custom__"
    ? $("#repo-custom").value.trim()
    : repoSelect.value;
  if (!repo) {
    showOnboardError("Pick a repository (or choose 'Type a repository manually…').");
    return;
  }

  const body = {
    repo,
    service: form.service.value.trim() || undefined,
    image: form.image.value.trim() || undefined,
    port: parseInt(form.port.value, 10) || 0,
    hostname: form.hostname.value.trim() || undefined,
    context: form.context.value.trim() || undefined,
    dockerfile: form.dockerfile.value.trim() || undefined,
    env: buildEnv(),
    overwrite_workflow: form.overwrite.checked,
  };

  const { status, data } = await api("/onboard", { method: "POST", token: adminToken, body });

  if (status === 401) {
    showBanner("Unauthorized — open Settings and enter the correct admin token.");
    openSettings();
    return;
  }
  if (status === 404) {
    showBanner("Onboarding is disabled — set GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_B64 on the server.");
    return;
  }
  if (status === 201) {
    form.reset();
    form.port.value = "3000";
    form.context.value = ".";
    form.dockerfile.value = "Dockerfile";
    form.overwrite.checked = false;
    $("#repo-custom").hidden = true;
    $("#image-hint").textContent = "";
    $("#env-rows").innerHTML = "";
    addEnvRow();
    renderOnboardResult(data);
    loadServices();
    loadRepos();
    return;
  }
  const msg = data && data.error ? data.error : "Onboarding failed (status " + status + ")";
  if (data && data.results) {
    const res = data.results;
    const done = [
      res.compose ? "compose " + res.compose : null,
      res.secret ? "secret set" : null,
    ].filter(Boolean).join(", ");
    showOnboardError(msg + (done ? " — already done: " + done + ". Re-run to continue." : ""));
  } else {
    showOnboardError(msg);
  }
}

function renderOnboardResult(r) {
  const warns = (r.warnings || [])
    .map((w) => `<li class="warn">${esc(w)}</li>`)
    .join("");
  const pr = r.pr
    ? `<br><a href="${esc(r.pr.url)}" target="_blank" rel="noopener">Pull request #${esc(r.pr.number)}</a> (${esc(r.pr.state)}) — review and merge it to activate deploys.`
    : "";
  showOnboardResult(
    `<span class="title">Onboarded ${esc(r.repo)}</span> as service <strong>${esc(r.service)}</strong> (${esc(r.image)}).` +
      ` Compose file: ${esc(r.compose)} · SERVICE_ENV secret: set` +
      (warns ? `<ul>${warns}</ul>` : "") + pr
  );
}

// ---- onboarding: repository picker ----

// Mirrors the Go defaultServiceFromRepo: lowercase, map invalid chars to '-',
// collapse repeats, trim, cap at 63 chars.
function defaultServiceFromRepo(repo) {
  const base = (repo.split("/").pop() || "").toLowerCase();
  let out = "";
  let prevDash = false;
  for (const ch of base) {
    let c = ch;
    const ok = (c >= "a" && c <= "z") || (c >= "0" && c <= "9");
    if (!ok) c = "-";
    if (c === "-" && prevDash) continue;
    prevDash = c === "-";
    out += c;
  }
  out = out.replace(/^-+|-+$/g, "");
  return out.slice(0, 63);
}

function setRepoDefaults(fullName) {
  if (!fullName) return;
  const owner = fullName.split("/")[0].toLowerCase();
  const service = defaultServiceFromRepo(fullName);
  const image = "ghcr.io/" + owner + "/" + service;
  const form = $("#onboard-form");
  form.service.value = service;
  form.hostname.value = service;
  form.image.value = image;
  $("#image-hint").textContent = "Default image the workflow will build: " + image;
}

async function loadRepos() {
  const select = $("#repo");
  select.innerHTML = '<option value="">Loading repositories…</option>';
  const token = getToken(LS_READ) || getToken(LS_ADMIN);
  const { status, data } = await api("/onboard/repos", { token });

  if (status === 404) {
    select.innerHTML = '<option value="">Onboarding disabled (set GITHUB_APP_ID on the server)</option>';
    select.disabled = true;
    return;
  }
  if (status === 401) {
    select.innerHTML = '<option value="">Enter a token in Settings to load repositories</option>';
    select.disabled = true;
    return;
  }
  if (status !== 200) {
    select.innerHTML = '<option value="">Failed to load repositories</option>';
    select.disabled = true;
    return;
  }

  const repos = (data.repos || [])
    .map((r) => `<option value="${esc(r.full_name)}">${esc(r.full_name)}</option>`)
    .join("");
  select.innerHTML =
    '<option value="">Select a repository…</option>' +
    repos +
    '<option value="__custom__">Type a repository manually…</option>';
  select.disabled = false;
}

function onRepoChange() {
  const custom = $("#repo-custom");
  if ($("#repo").value === "__custom__") {
    custom.hidden = false;
    custom.focus();
    return;
  }
  custom.hidden = true;
  setRepoDefaults($("#repo").value);
}

// ---- onboarding: env key-value rows ----

function addEnvRow(key, value) {
  const row = document.createElement("div");
  row.className = "env-row";
  const k = document.createElement("input");
  k.name = "env_key";
  k.placeholder = "KEY (e.g. API_KEY)";
  k.value = key || "";
  k.autocomplete = "off";
  const v = document.createElement("input");
  v.name = "env_value";
  v.placeholder = "value";
  v.value = value || "";
  v.autocomplete = "off";
  const rm = document.createElement("button");
  rm.type = "button";
  rm.className = "ghost";
  rm.textContent = "✕";
  rm.title = "Remove";
  rm.addEventListener("click", () => row.remove());
  row.append(k, v, rm);
  $("#env-rows").appendChild(row);
}

function buildEnv() {
  const env = {};
  for (const row of $("#env-rows").children) {
    const key = row.querySelector('[name="env_key"]').value.trim();
    const val = row.querySelector('[name="env_value"]').value;
    if (key) env[key] = val;
  }
  return env;
}

// Settings wiring
$("#settings-toggle").addEventListener("click", () => {
  const s = $("#settings");
  s.hidden = !s.hidden;
});

$("#save-tokens").addEventListener("click", () => {
  setToken(LS_READ, $("#read-token").value.trim());
  setToken(LS_ADMIN, $("#admin-token").value.trim());
  $("#settings").hidden = true;
  hideBanner();
  loadServices();
  loadRepos();
});

// Pre-fill token fields on load so the user can see whether a token is set.
$("#read-token").value = getToken(LS_READ);
$("#admin-token").value = getToken(LS_ADMIN);

$("#refresh").addEventListener("click", loadServices);
$("#add-form").addEventListener("submit", submitAdd);
$("#onboard-form").addEventListener("submit", submitOnboard);
$("#repo").addEventListener("change", onRepoChange);
$("#repo-refresh").addEventListener("click", loadRepos);
$("#env-add").addEventListener("click", () => addEnvRow());
$("#repo-custom").addEventListener("input", () => {
  const repo = $("#repo-custom").value.trim();
  if (repo) setRepoDefaults(repo);
});

loadServices();
loadRepos();
addEnvRow();
