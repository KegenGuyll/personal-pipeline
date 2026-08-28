"use strict";

const LS_READ = "dash.readToken";
const LS_ADMIN = "dash.adminToken";

const $ = (sel) => document.querySelector(sel);

const banner = $("#banner");
const addError = $("#add-error");

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

loadServices();
