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
  // Use elements.namedItem: direct `form.name` etc. collides with the form's
  // own properties (and is unsupported in some engines).
  const body = {
    name: form.elements.namedItem("name").value.trim(),
    image: form.elements.namedItem("image").value.trim(),
    port: parseInt(form.elements.namedItem("port").value, 10) || 0,
    hostname: form.elements.namedItem("hostname").value.trim() || undefined,
  };

  const { status, data } = await api("/services", {
    method: "POST", token: adminToken, body,
  });

  if (status === 201) {
    form.reset();
    form.elements.namedItem("port").value = "3000";
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
  // Canonical form-control access (see submitAdd).
  const el = (name) => form.elements.namedItem(name);
  const val = (name) => (el(name) ? el(name).value.trim() : "");

  const repo = val("repo");
  if (!repo) {
    showOnboardError("Pick or type a repository (owner/repo).");
    return;
  }

  const body = {
    repo,
    service: val("service") || undefined,
    image: val("image") || undefined,
    port: parseInt(val("port"), 10) || 0,
    hostname: val("hostname") || undefined,
    context: val("context") || undefined,
    dockerfile: val("dockerfile") || undefined,
    env: buildEnv(),
    overwrite_workflow: el("overwrite") ? el("overwrite").checked : false,
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
    const el = (name) => form.elements.namedItem(name);
    if (el("port")) el("port").value = "3000";
    if (el("context")) el("context").value = ".";
    if (el("dockerfile")) el("dockerfile").value = "Dockerfile";
    if (el("overwrite")) el("overwrite").checked = false;
    $("#image-hint").textContent = "";
    $("#compose-hint").textContent = "";
    populateEnv([]);
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
  const secrets = "SERVICE_ENV secret: set" +
    (r.webhook_secrets ? " · webhook secrets: set" : "") +
    (r.ts_authkey ? " · TS_AUTHKEY: " + esc(r.ts_authkey) : "");
  showOnboardResult(
    `<span class="title">Onboarded ${esc(r.repo)}</span> as service <strong>${esc(r.service)}</strong> (${esc(r.image)}).` +
      ` Compose file: ${esc(r.compose)} · ${secrets}` +
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
  // Use elements.namedItem — unambiguous form-control access.
  const set = (name, val) => {
    const el = form.elements.namedItem(name);
    if (el) el.value = val;
  };
  set("service", service);
  set("hostname", service);
  set("image", image);
  const hint = $("#image-hint");
  if (hint) hint.textContent = "Default image the workflow will build: " + image;
}

// ---- onboarding: searchable repository combobox ----

let repoRepos = []; // cached [{full_name}] from GET /onboard/repos
let repoActive = -1; // active row index in the visible list

function renderRepoList() {
  const input = $("#repo");
  const list = $("#repo-list");
  const q = input.value.trim().toLowerCase();
  const matches = q
    ? repoRepos.filter((r) => r.full_name.toLowerCase().includes(q)).slice(0, 100)
    : repoRepos.slice(0, 30);
  if (!matches.length) {
    repoActive = -1;
    list.innerHTML = `<li class="muted">${q ? "No matching repositories" : "No repositories loaded — type owner/repo manually"}</li>`;
  } else {
    repoActive = 0; // top match highlighted by default: type + Enter just works
    list.innerHTML = matches
      .map((r, i) => `<li data-index="${i}" data-full="${esc(r.full_name)}">${esc(r.full_name)}</li>`)
      .join("");
    list.querySelectorAll("li:not(.muted)")[0].classList.add("active");
  }
  list.hidden = false;
}

function openRepoList() { renderRepoList(); }
function closeRepoList() { $("#repo-list").hidden = true; }

function setRepoActive(items) {
  items.forEach((li, i) => li.classList.toggle("active", i === repoActive));
  const active = items[repoActive];
  if (active) active.scrollIntoView({ block: "nearest" });
}

function selectRepo(fullName) {
  const input = $("#repo");
  input.value = fullName;
  setRepoDefaults(fullName);
  loadEnvKeys(fullName);
  closeRepoList();
}

async function loadRepos() {
  const input = $("#repo");
  repoRepos = [];
  const token = getToken(LS_READ) || getToken(LS_ADMIN);
  const { status, data } = await api("/onboard/repos", { token });

  if (status === 404) {
    input.placeholder = "Onboarding disabled — type owner/repo manually";
    closeRepoList();
    return;
  }
  if (status === 401) {
    input.placeholder = "Enter a token in Settings to load repositories";
    closeRepoList();
    return;
  }
  if (status !== 200) {
    input.placeholder = "Failed to load repositories — type owner/repo manually";
    closeRepoList();
    return;
  }
  repoRepos = data.repos || [];
  input.placeholder = "Search or type owner/repo…";
}

const repoInput = $("#repo");
const repoList = $("#repo-list");

repoInput.addEventListener("focus", openRepoList);
let repoDebounce = null;
repoInput.addEventListener("input", () => {
  openRepoList();
  // Prefill as soon as the value looks like owner/repo (also covers typing).
  const v = repoInput.value.trim();
  if (/^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/.test(v)) {
    setRepoDefaults(v);
    clearTimeout(repoDebounce);
    repoDebounce = setTimeout(() => loadEnvKeys(v), 400);
  }
});
repoInput.addEventListener("keydown", (e) => {
  const items = [...repoList.querySelectorAll("li:not(.muted)")];
  if (e.key === "ArrowDown") {
    e.preventDefault();
    repoActive = Math.min(repoActive + 1, items.length - 1);
    setRepoActive(items);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    repoActive = Math.max(repoActive - 1, 0);
    setRepoActive(items);
  } else if (e.key === "Enter") {
    if (!repoList.hidden && items[repoActive]) {
      e.preventDefault();
      selectRepo(items[repoActive].dataset.full);
    } else if (!repoList.hidden) {
      // Enter with the list open but nothing highlighted: keep the typed
      // value, prefill defaults, close the list (submit still works after).
      e.preventDefault();
      const v = repoInput.value.trim();
      if (v) setRepoDefaults(v);
      closeRepoList();
    }
    // list hidden -> let the form submit normally
  } else if (e.key === "Escape") {
    closeRepoList();
  }
});
repoList.addEventListener("mousedown", (e) => {
  const li = e.target.closest("li:not(.muted)");
  if (li) {
    e.preventDefault(); // beat input blur
    selectRepo(li.dataset.full);
  }
});
repoList.addEventListener("mouseover", (e) => {
  const li = e.target.closest("li:not(.muted)");
  if (!li) return;
  repoActive = parseInt(li.dataset.index, 10);
  setRepoActive([...repoList.querySelectorAll("li:not(.muted)")]);
});
document.addEventListener("click", (e) => {
  if (!e.target.closest(".repo-combobox")) closeRepoList();
});

// ---- onboarding: env key-value rows ----

function addEnvRow(key, value, valuePlaceholder) {
  const row = document.createElement("div");
  row.className = "env-row";
  const k = document.createElement("input");
  k.name = "env_key";
  k.placeholder = "KEY (e.g. API_KEY)";
  k.value = key || "";
  k.autocomplete = "off";
  const v = document.createElement("input");
  v.name = "env_value";
  v.placeholder = valuePlaceholder || "value";
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

// populateEnv rebuilds the env rows from the repo's env-example keys. Values
// are left blank (sample values are placeholders). TS_AUTHKEY always gets a
// row: blank means "use the server's shared key" (the agent injects it).
function populateEnv(keys) {
  const list = keys || [];
  $("#env-rows").innerHTML = "";
  if (!list.includes("TS_AUTHKEY")) addEnvRow("TS_AUTHKEY", "", "blank = server's shared key");
  for (const k of list) {
    if (k !== "TS_AUTHKEY") addEnvRow(k, "");
  }
}

// loadEnvKeys fetches the repo's env-example keys and pre-fills the env form.
async function loadEnvKeys(fullName) {
  if (!fullName) return;
  const token = getToken(LS_READ) || getToken(LS_ADMIN);
  const form = $("#onboard-form");
  const service = form.elements.namedItem("service") ? form.elements.namedItem("service").value.trim() : "";
  const { status, data } = await api(
    "/onboard/env-keys?repo=" + encodeURIComponent(fullName) + (service ? "&service=" + encodeURIComponent(service) : ""),
    { token }
  );
  if (status !== 200) return;
  populateEnv(data.keys || []);
  setComposeHint(data);
}

// setComposeHint tells the user whether the current service name maps to an
// existing (custom) compose or will get the standard template — making a name
// mismatch like "dsh-server" vs the committed "dsh" visible before submit.
function setComposeHint(data) {
  const hint = $("#compose-hint");
  if (!hint) return;
  if (data.compose === "existing") {
    hint.textContent = "✓ Custom compose exists for this name — it will be used as-is.";
    hint.className = "hint ok-hint";
  } else if (data.compose === "missing") {
    const existing = (data.existing_services || []).filter((s) => s && s !== "_template");
    hint.textContent = "⚠ No compose for this name — the standard template will be generated." +
      (existing.length ? " Existing services: " + existing.join(", ") : "");
    hint.className = "hint warn-hint";
  } else {
    hint.textContent = "";
    hint.className = "hint";
  }
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
$("#repo-refresh").addEventListener("click", loadRepos);
$("#env-add").addEventListener("click", () => addEnvRow());

// Changing the service name re-checks whether a custom compose exists for it.
const serviceField = $("#onboard-form").elements.namedItem("service");
if (serviceField) {
  let serviceDebounce = null;
  serviceField.addEventListener("input", () => {
    clearTimeout(serviceDebounce);
    serviceDebounce = setTimeout(() => {
      const repo = $("#repo").value.trim();
      if (repo) loadEnvKeys(repo);
    }, 400);
  });
}

loadServices();
loadRepos();
populateEnv([]);
