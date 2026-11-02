"use strict";

// Interface de sffs : tableau de bord, lectures et configuration (lecture seule).

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const nf = new Intl.NumberFormat("fr-FR");
const num = (n) => nf.format(n || 0);

const STATUS = {
  cache: ["En cache", "ok"],
  substitution: ["Remplacée", "accent"],
  community: ["Communautaire", "info"],
  uncached: ["Hors cache", "bad"],
  denied: ["Démentie", "warn"],
  list_error: ["Erreur StreamFusion", "bad"],
  no_imdb: ["Non vérifiable", "bad"],
  "": ["Sans verdict", ""],
};

let tab = "dashboard";
let timer = null;
let plays = [];
let configLoaded = false;
let serverNow = Date.now(); // heure de sffs (les lectures sont datées par lui)
const lastBytes = new Map(); // id -> {bytes, t} : débit entre deux actualisations
const rates = new Map();     // id -> octets/s

// Lecture en cours : des données lues dans la dernière minute.
const isLive = (p, now) => p.last_read && now - new Date(p.last_read) < 60e3;

function trackRates() {
  for (const p of plays) {
    if (!isLive(p, serverNow)) { lastBytes.delete(p.id); rates.delete(p.id); continue; }
    const prev = lastBytes.get(p.id);
    if (prev && serverNow > prev.t && p.bytes >= prev.bytes) rates.set(p.id, ((p.bytes - prev.bytes) * 1000) / (serverNow - prev.t));
    lastBytes.set(p.id, { bytes: p.bytes, t: serverNow });
  }
}

const METHOD = { direct: "lecture directe", directplay: "lecture directe", directstream: "flux direct", transcode: "transcodage", remux: "remux" };

// Qui lit : application et utilisateur selon Silo, puis processus qui ouvre le fichier.
function whoLine(p) {
  const parts = [];
  if (p.client) {
    if (p.client.app) parts.push(`<b>${esc(p.client.app)}</b>`);
    if (p.client.user) parts.push(esc(p.client.user));
    const m = p.client.play_method;
    if (m) parts.push(esc((METHOD[m.toLowerCase().replace(/[^a-z]/g, "")] || m) + (p.client.resolution ? " " + p.client.resolution : "")));
  }
  if (p.process) parts.push(`<span title="pid ${esc(p.pid)}${p.opens > 1 ? " · " + esc(p.opens) + " ouvertures" : ""}">FUSE · ${esc(p.process)}</span>`);
  return parts.length ? `<div class="who small">${parts.join(" · ")}</div>` : "";
}

function progress(p) {
  if (!p.size || !p.position) return "";
  const pct = Math.min(100, Math.round((p.position / p.size) * 100));
  return `<span class="pos"><progress max="100" value="${pct}"></progress> ${pct} %</span>`;
}

function stalls(p) {
  return p.stalls ? `<span class="warn-text">${num(p.stalls)} · ${ms(p.stall_ms)}</span>` : "0";
}

function renderLive() {
  const live = plays.filter((p) => isLive(p, serverNow));
  $("live").hidden = !live.length;
  $("live-list").innerHTML = live.map((p) => {
    const t = titleOf(p.path);
    const [label, cls] = STATUS[p.status || ""] || [p.status, ""];
    const rate = rates.get(p.id);
    const version = p.status === "substitution" && p.served ? p.served : t.file;
    return `<div class="live-row">
      <div class="live-main"><div class="title">${esc(t.title)}</div><div class="file">${esc(version)}</div>${whoLine(p)}</div>
      <div class="live-meta">${badge(label, cls)}
        ${progress(p)}
        <span><b>${rate !== undefined ? bytes(rate) + "/s" : "…"}</b> <span class="muted">débit</span></span>
        <span><b>${ms(p.first_byte_ms)}</b> <span class="muted">1er octet</span></span>
        <span><b>${stalls(p)}</b> <span class="muted">blocages</span></span>
        <span><b>${bytes(p.bytes)}</b> <span class="muted">lus</span></span>
        <span class="muted">depuis ${esc(timeFmt.format(new Date(p.at)))}</span></div></div>`;
  }).join("");
}

// ---------------------------------------------------------------- formats

function bytes(n) {
  if (!n) return "0 o";
  const u = ["o", "Ko", "Mo", "Go", "To"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)).toString().replace(".", ",") + " " + u[i];
}

function ms(n) {
  if (!n) return "–";
  if (n < 1000) return n + " ms";
  if (n < 60000) return (n / 1000).toFixed(1).replace(".", ",") + " s";
  const m = Math.floor(n / 60000), s = Math.round((n % 60000) / 1000);
  return m + " min" + (s ? " " + s + " s" : "");
}

function since(t, now) {
  if (!t) return "jamais";
  const d = Math.max(0, (now - new Date(t)) / 1000);
  if (d < 60) return "à l'instant";
  if (d < 3600) return "il y a " + Math.floor(d / 60) + " min";
  if (d < 86400) return "il y a " + Math.floor(d / 3600) + " h " + String(Math.floor((d % 3600) / 60)).padStart(2, "0");
  return "il y a " + Math.floor(d / 86400) + " j";
}

function uptime(t, now) {
  const d = Math.max(0, (now - new Date(t)) / 1000);
  const j = Math.floor(d / 86400), h = Math.floor((d % 86400) / 3600), m = Math.floor((d % 3600) / 60);
  return (j ? j + " j " : "") + (j || h ? h + " h " : "") + m + " min";
}

const timeFmt = new Intl.DateTimeFormat("fr-FR", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("fr-FR", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
function when(t) {
  const d = new Date(t);
  return new Date().toDateString() === d.toDateString() ? timeFmt.format(d) : dayFmt.format(d);
}

function kv(el, rows) {
  el.innerHTML = rows.filter(Boolean).map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join("");
}

function badge(text, cls) {
  return `<span class="badge ${cls || ""}">${esc(text)}</span>`;
}

function tile(label, value, sub) {
  return `<div class="tile"><div class="label">${esc(label)}</div><div class="value">${value}</div>${sub ? `<div class="sub">${sub}</div>` : ""}</div>`;
}

// Titre lisible tiré du chemin : dossier du film ou de la série, avec l'épisode.
function titleOf(path) {
  const parts = (path || "").split("/");
  const dir = (parts[1] || path || "").replace(/\s*\{tmdb-\d+\}$/, "");
  const file = parts[parts.length - 1] || "";
  const ep = parts[0] === "series" ? (file.match(/S\d{2}E\d{2,3}/i) || [""])[0] : "";
  return { title: dir + (ep ? " · " + ep.toUpperCase() : ""), file, series: parts[0] === "series" };
}

// ---------------------------------------------------------------- API

async function api(path, opts) {
  const r = await fetch(path, { credentials: "same-origin", ...opts });
  if (r.status === 401) { showLogin(); throw new Error("auth"); }
  if (!r.ok) throw new Error(await r.text());
  return r.status === 204 ? null : r.json();
}

// ---------------------------------------------------------------- tableau de bord

function renderStatus(s) {
  const now = new Date(s.now);
  renderLive();
  const c = s.counters, rt = s.runtime, cat = s.catalog;
  const ph = $("phase");
  ph.textContent = s.phase === "monté" ? "En service" : "Chargement du catalogue…";
  ph.className = "badge " + (s.phase === "monté" ? "ok" : "warn");

  const day = plays.filter((p) => now - new Date(p.at) < 86400e3);
  const ok = day.filter((p) => ["cache", "substitution", "community"].includes(p.status)).length;
  const bytesDay = day.reduce((a, p) => a + (p.bytes || 0), 0);

  const liveN = plays.filter((p) => isLive(p, now)).length;
  $("tiles").innerHTML = [
    tile("En cours", num(liveN), liveN ? "lecture" + (liveN > 1 ? "s" : "") + " en cours" : "aucune lecture"),
    tile("Fichiers exposés", num(cat.files), `${num(cat.movie_dirs)} films · ${num(cat.show_dirs)} séries`),
    tile("Lectures (24 h)", num(day.length), day.length ? `${Math.round((ok / day.length) * 100)} % lancées sans téléchargement` : "aucune"),
    tile("Données lues (24 h)", bytes(bytesDay), `${bytes(c.bytes_read)} depuis le démarrage`),
    tile("Scans Silo en attente", num(s.silo.pending), s.silo.enabled ? `${num(s.silo.requested)} demandés` : "Silo non configuré"),
    tile("Mémoire", bytes(rt.rss), rt.gomemlimit ? `plafond Go ${esc(rt.gomemlimit)}` : "sans plafond Go"),
    tile("Processeur", (rt.cpu_percent || 0).toFixed(0) + " %", `${rt.gomaxprocs} cœurs autorisés`),
  ].join("");

  const L = cat.load, R = cat.reload;
  kv($("catalog"), [
    ["Films", `${num(L.movies)} <span class="muted">· ${num(L.movie_versions)} versions</span>`],
    L.shows ? ["Séries", `${num(L.shows)} <span class="muted">· ${num(L.episodes)} épisodes</span>`] : null,
    L.shows ? ["Versions d'épisodes", num(L.episode_versions)] : null,
    ["Chargement complet", L.at ? `${since(L.at, now)} <span class="muted">· ${ms(L.ms)}</span>` : "en cours…"],
    ["Rechargement automatique", cat.reload_every === "désactivé" ? "désactivé" : "toutes les " + esc(cat.reload_every)],
    R.at ? ["Dernier rechargement", R.error ? badge("échec", "bad") + " " + esc(R.error) : `+${num(R.added)} / −${num(R.removed)} fichiers <span class="muted">· ${since(R.at, now)}</span>`] : null,
    ["Montage", `<span class="cfg-value">${esc(s.mount)}</span>`],
  ]);

  const avgRes = c.resolves ? Math.round(c.resolve_ms_sum / c.resolves) : 0;
  const avgList = c.list_fetches ? Math.round(c.list_ms_sum / c.list_fetches) : 0;
  const cm = s.community;
  kv($("cache"), [
    ["Lectures depuis le démarrage", num(c.plays)],
    ["En cache telles quelles", num(c.cached)],
    ["Remplacées par une version en cache", num(c.substituted)],
    ["Lues grâce au cache communautaire", num(c.community)],
    ["Hors cache (non lancées)", c.uncached ? `<span class="badge bad">${num(c.uncached)}</span>` : "0"],
    ["Démenties à la lecture", num(c.denied)],
    ["Listes StreamFusion consultées", `${num(c.list_fetches)} <span class="muted">· ${ms(avgList)} en moyenne${c.list_errors ? " · " + num(c.list_errors) + " erreurs" : ""}</span>`],
    ["Liens AllDebrid obtenus", `${num(c.resolves)} <span class="muted">· ${ms(avgRes)} en moyenne, ${ms(c.resolve_ms_max)} au pire</span>`],
    ["Erreurs de lecture", c.read_errors ? badge(num(c.read_errors), "bad") : "0"],
    ["Titres déjà vus en cache", num(s.known_titles)],
    ["Cache communautaire", !cm.enabled ? "désactivé" : cm.failed ? badge("indisponible", "bad") :
      `${num(cm.size)} torrents <span class="muted">· ${since(cm.loaded, now)}</span>`],
    ["Fichiers armés en ce moment", num(s.armed)],
  ]);

  const w = s.watch;
  $("watch-summary").textContent = w.every === "désactivé" ? "désactivé" :
    `toutes les ${w.every} · dernier passage ${since(w.last, now)} · ${num(w.runs)} passages`;
  const passes = [...w.passes].reverse();
  $("passes").innerHTML = passes.length ? passes.slice(0, 15).map((p) => `<tr>
      <td class="when">${esc(when(p.at))}</td><td>${esc(p.mode || "ciblé")}</td>
      <td class="num">${num(p.changed)}<span class="muted"> / ${num(p.touched)}</span></td>
      <td class="num">${num(p.movies)}</td><td class="num">${num(p.shows)}</td>
      <td class="num">${p.added ? "+" + num(p.added) : "–"}</td><td class="num">${p.removed ? "−" + num(p.removed) : "–"}</td>
      <td class="num">${ms(p.ms)}</td></tr>`).join("")
    : `<tr><td colspan="8" class="empty">Aucun changement depuis le démarrage</td></tr>`;
  if (w.error) $("passes").insertAdjacentHTML("afterbegin", `<tr><td colspan="8">${badge("erreur", "bad")} ${esc(w.error)}</td></tr>`);
  drawChart(w.passes);

  const si = s.silo;
  kv($("silo"), si.enabled ? [
    ["Scans ciblés demandés", num(si.requested)],
    ["En attente", num(si.pending)],
    ["Limite de requêtes atteinte", num(si.rate_limited) + " fois"],
    ["Dossiers refusés par Silo", si.refused ? badge(num(si.refused), "warn") : "0"],
    ["Demandes en échec", si.failed ? badge(num(si.failed), "bad") : "0"],
    ["Dernière demande", since(si.last_at, now)],
    si.last_error ? ["Dernière erreur", `<span class="error">${esc(si.last_error)}</span>`] : null,
  ] : [["État", "Silo non configuré (SFFS_SILO_URL / SFFS_SILO_KEY)"]]);

  kv($("runtime"), [
    ["Mémoire résidente", bytes(rt.rss)],
    ["Tas Go utilisé / réservé", `${bytes(rt.heap_inuse)} / ${bytes(rt.heap_sys)}`],
    ["Plafond du ramasse-miettes", esc(rt.gomemlimit || "aucun")],
    ["Processeur", `${(rt.cpu_percent || 0).toFixed(1).replace(".", ",")} % <span class="muted">· ${num(Math.round(rt.cpu_seconds))} s au total</span>`],
    ["Cœurs autorisés (GOMAXPROCS)", num(rt.gomaxprocs)],
    ["Threads · goroutines", `${num(rt.threads)} · ${num(rt.goroutines)}`],
    ["Ramasse-miettes", num(rt.gc_count) + " passages"],
    ["Démarré", `${uptime(s.started, now)} <span class="muted">· ${esc(dayFmt.format(new Date(s.started)))}</span>`],
  ]);

  const ref = [...s.refused].reverse();
  $("refused").innerHTML = ref.length ? ref.slice(0, 20).map((r) => `<tr><td class="when">${esc(when(r.at))}</td>
      <td class="file">${esc(r.what)}</td><td>${esc(r.reason)}</td></tr>`).join("")
    : `<tr><td colspan="3" class="empty">Aucun refus depuis le démarrage</td></tr>`;

  $("foot").textContent = `sffs ${s.version} · ${rt.go_version || ""} · actualisé à ${timeFmt.format(now)}`;
}

// Barres des fichiers ajoutés (haut) et retirés (bas) par passage de surveillance.
function drawChart(passes) {
  const el = $("watch-chart");
  const ps = passes.filter((p) => p.added || p.removed).slice(-60);
  if (!ps.length) { el.innerHTML = ""; el.hidden = true; return; }
  el.hidden = false;
  const W = 600, H = 70, mid = 46, n = 60, bw = W / n;
  const maxA = Math.max(1, ...ps.map((p) => p.added)), maxR = Math.max(1, ...ps.map((p) => p.removed));
  const off = n - ps.length;
  let bars = "";
  ps.forEach((p, i) => {
    const x = (off + i) * bw + 1, w = Math.max(1, bw - 2);
    const ha = Math.max(p.added ? 1.5 : 0, (p.added / maxA) * (mid - 2));
    const hr = Math.max(p.removed ? 1.5 : 0, (p.removed / maxR) * (H - mid - 4));
    const label = `${when(p.at)} : +${p.added} / −${p.removed}`;
    bars += `<g><title>${esc(label)}</title><rect class="add" x="${x}" y="${mid - ha}" width="${w}" height="${ha}" rx="1"/>` +
      `<rect class="rem" x="${x}" y="${mid + 1}" width="${w}" height="${hr}" rx="1"/></g>`;
  });
  el.innerHTML = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img"><line class="axis" x1="0" x2="${W}" y1="${mid + 0.5}" y2="${mid + 0.5}"/>${bars}</svg>`;
}

// ---------------------------------------------------------------- lectures

function renderPlays() {
  const q = $("play-search").value.trim().toLowerCase();
  const f = $("play-filter").value;
  const days = +$("play-period").value;
  const now = serverNow;
  const rows = plays.filter((p) => now - new Date(p.at) < days * 86400e3 &&
    (!f || p.status === f) && (!q || p.path.toLowerCase().includes(q)));

  const ok = rows.filter((p) => ["cache", "substitution", "community"].includes(p.status)).length;
  const sub = rows.filter((p) => p.status === "substitution").length;
  const res = rows.filter((p) => p.resolve_ms).map((p) => p.resolve_ms).sort((a, b) => a - b);
  const med = res.length ? res[Math.floor(res.length / 2)] : 0;
  $("play-tiles").innerHTML = [
    tile("Lectures", num(rows.length), days === 1 ? "sur 24 heures" : `sur ${days} jours`),
    tile("Lancées sans téléchargement", rows.length ? Math.round((ok / rows.length) * 100) + " %" : "–", `${num(ok)} sur ${num(rows.length)}`),
    tile("Remplacées", num(sub), "par une version en cache"),
    tile("Délai médian du lien", ms(med), "réponse d'AllDebrid"),
    tile("Données lues", bytes(rows.reduce((a, p) => a + (p.bytes || 0), 0)), ""),
  ].join("");

  $("plays").innerHTML = rows.length ? rows.slice(0, 500).map((p) => {
    const t = titleOf(p.path);
    const [label, cls] = STATUS[p.status || ""] || [p.status, ""];
    const served = p.status === "substitution" && p.served
      ? `<div class="file">${esc(p.served)}</div>${p.bucket ? `<span class="muted small">${esc(p.bucket === "unknown" ? "qualité inconnue" : p.bucket + "p")}</span>` : ""}`
      : `<span class="muted">${["uncached", "no_imdb", "", "list_error"].includes(p.status || "") ? "–" : "originale"}</span>`;
    const err = p.errors ? `<div class="error small">${num(p.errors)} erreur(s) : ${esc(p.last_error)}</div>` : "";
    return `<tr><td class="when">${esc(when(p.at))}</td>
      <td><div class="title">${esc(t.title)}</div><div class="file">${esc(t.file)}</div>${whoLine(p)}${err}</td>
      <td>${badge(label, cls)}${isLive(p, serverNow) ? " " + badge("En cours", "ok") : ""}</td><td>${served}</td>
      <td class="num">${progress(p) || "–"}</td><td class="num">${ms(p.resolve_ms)}</td><td class="num">${ms(p.first_byte_ms)}</td>
      <td class="num">${p.process ? stalls(p) : "–"}</td><td class="num">${p.bytes ? bytes(p.bytes) : "–"}</td></tr>`;
  }).join("") : `<tr><td colspan="9" class="empty">Aucune lecture sur cette période</td></tr>`;
}

// ---------------------------------------------------------------- configuration

async function loadConfig() {
  const entries = await api("/api/config");
  const groups = new Map();
  for (const e of entries) {
    if (!groups.has(e.group)) groups.set(e.group, []);
    groups.get(e.group).push(e);
  }
  $("config").innerHTML = [...groups].map(([g, es]) => `<article class="card cfg-group"><h2>${esc(g)}</h2>
    <div class="table-wrap"><table><thead><tr><th>Réglage</th><th>Valeur</th><th>Description</th></tr></thead><tbody>
    ${es.map((e) => `<tr><td class="cfg-name">${esc(e.name)}</td>
      <td class="cfg-value ${e.set ? "" : "default"}">${e.value === "" ? "<span class=\"muted\">(vide)</span>" : esc(e.value)}${e.set ? "" : ` <span class="muted small">par défaut</span>`}</td>
      <td class="muted">${esc(e.desc)}</td></tr>`).join("")}
    </tbody></table></div></article>`).join("");
  configLoaded = true;
}

// ---------------------------------------------------------------- boucle

async function refresh() {
  clearTimeout(timer);
  try {
    const [s, p] = await Promise.all([api("/api/status"), api("/api/plays?limit=2000")]);
    plays = p;
    serverNow = new Date(s.now).getTime();
    trackRates();
    $("stale").hidden = true;
    renderStatus(s);
    if (tab === "plays") renderPlays();
  } catch (e) {
    if (e.message === "auth") return;
    $("stale").hidden = false;
  }
  timer = setTimeout(refresh, document.hidden ? 30000 : 5000);
}

function selectTab(name) {
  tab = name;
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", b.dataset.tab === name);
  for (const s of document.querySelectorAll(".tab")) s.hidden = s.id !== "tab-" + name;
  try { localStorage.setItem("sffs-tab", name); } catch (_) { /* stockage indisponible */ }
  if (name === "plays") renderPlays();
  if (name === "config" && !configLoaded) loadConfig().catch(() => {});
}

function showLogin() {
  clearTimeout(timer);
  $("app").hidden = true;
  $("login").hidden = false;
  $("password").focus();
}

function showApp() {
  $("login").hidden = true;
  $("app").hidden = false;
  let saved = "dashboard";
  try { saved = localStorage.getItem("sffs-tab") || saved; } catch (_) { /* stockage indisponible */ }
  selectTab(["dashboard", "plays", "config"].includes(saved) ? saved : "dashboard");
  refresh();
}

document.addEventListener("DOMContentLoaded", async () => {
  $("login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    $("login-error").textContent = "";
    const r = await fetch("/api/login", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: $("password").value }),
    });
    if (r.ok) { $("password").value = ""; showApp(); }
    else $("login-error").textContent = (await r.text()).trim() || "Connexion impossible";
  });
  $("logout").addEventListener("click", async () => {
    await fetch("/api/logout", { method: "POST" });
    configLoaded = false;
    showLogin();
  });
  for (const b of document.querySelectorAll(".tabs button")) b.addEventListener("click", () => selectTab(b.dataset.tab));
  for (const id of ["play-search", "play-filter", "play-period"]) $(id).addEventListener("input", renderPlays);
  document.addEventListener("visibilitychange", () => { if (!document.hidden && !$("app").hidden) refresh(); });

  try {
    const s = await (await fetch("/api/session")).json();
    s.authenticated ? showApp() : showLogin();
  } catch (_) {
    showLogin();
  }
});
