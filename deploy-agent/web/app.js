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
  let env = {};
  const envText = form.env.value.trim();
  if (envText) {
    try {
      env = JSON.parse(envText);
    } catch (_) {
      showOnboardError('Env must be valid JSON, e.g. {"API_KEY":"..."}.');
      return;
    }
    if (typeof env !== "object" || env === null || Array.isArray(env)) {
      showOnboardError('Env must be a JSON object, e.g. {"API_KEY":"..."}.');
      return;
    }
  }

  const body = {
    repo: form.repo.value.trim(),
    service: form.service.value.trim() || undefined,
    image: form.image.value.trim() || undefined,
    port: parseInt(form.port.value, 10) || 0,
    hostname: form.hostname.value.trim() || undefined,
    context: form.context.value.trim() || undefined,
    dockerfile: form.dockerfile.value.trim() || undefined,
    env,
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
    renderOnboardResult(data);
    loadServices();
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
});

// Pre-fill token fields on load so the user can see whether a token is set.
$("#read-token").value = getToken(LS_READ);
$("#admin-token").value = getToken(LS_ADMIN);

$("#refresh").addEventListener("click", loadServices);
$("#add-form").addEventListener("submit", submitAdd);
$("#onboard-form").addEventListener("submit", submitOnboard);

loadServices();
