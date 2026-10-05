// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// hx-boost swaps the body on every navigation and runs this script again. Global
// things (the live stream, document listeners, timers) start once; page things run
// on every load through window.goliash.init.
(function () {
  if (window.goliash) {
    window.goliash.init();
    return;
  }

  // ---- once per browser tab ----

  // Live updates: the server sends "changed" over SSE when snapshots, releases or drift
  // change something; parts of the page listening for goliash:changed reload themselves.
  let online = null; // null until the first answer
  function showLive() {
    const live = document.querySelector("[data-live]");
    if (!live) return;
    live.classList.toggle("off", !online);
    live.textContent = online ? "Live" : online === null ? "Connecting…" : "Reconnecting…";
  }
  function connect() {
    if (!window.EventSource) return;
    const es = new EventSource("/ui/stream");
    es.addEventListener("changed", function () { htmx.trigger(document.body, "goliash:changed"); });
    es.onopen = function () { online = true; showLive(); };
    es.onerror = function () { online = false; showLive(); };
  }
  // Connect once the page has settled, so the long-lived stream does not delay loading.
  setTimeout(connect, 1500);

  // Relative times: <time datetime="…" data-ago> shows "5 min ago" and keeps it current.
  const units = [["year", 31536000], ["month", 2592000], ["day", 86400], ["hour", 3600], ["minute", 60]];
  const fmt = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "short" });
  function ago(el) {
    const t = Date.parse(el.getAttribute("datetime"));
    if (isNaN(t)) return;
    const secs = Math.round((t - Date.now()) / 1000);
    el.title = new Date(t).toLocaleString();
    for (const [unit, size] of units) {
      if (Math.abs(secs) >= size) { el.textContent = fmt.format(Math.round(secs / size), unit); return; }
    }
    el.textContent = "just now";
  }
  function refreshTimes(root) { (root || document).querySelectorAll("time[data-ago]").forEach(ago); }
  setInterval(refreshTimes, 30000);

  // Matrix filter: words must all appear in a row (service, owner, versions, targets);
  // "Only with drift" hides rows without drift. The state lives in the URL (?q=, ?drift=1),
  // so a filtered matrix can be shared, and it survives the live refreshes.
  function filterMatrix() {
    const q = document.getElementById("matrix-q");
    const drift = document.getElementById("matrix-drift");
    if (!q || !drift) return;
    const words = q.value.toLowerCase().split(/\s+/).filter(Boolean);
    const rows = document.querySelectorAll("table.matrix tbody tr[data-search]");
    let shown = 0;
    rows.forEach(function (tr) {
      const hay = tr.getAttribute("data-search");
      const ok = words.every(function (w) { return hay.indexOf(w) >= 0; }) && (!drift.checked || tr.getAttribute("data-drift") === "1");
      tr.classList.toggle("filtered", !ok);
      if (ok) shown++;
    });
    const tbody = document.querySelector("table.matrix tbody");
    let none = tbody && tbody.querySelector("tr.no-match");
    if (tbody && shown === 0 && rows.length > 0) {
      if (!none) {
        none = document.createElement("tr");
        none.className = "no-match";
        none.innerHTML = '<td colspan="99">No service matches the filter.</td>';
        tbody.appendChild(none);
      }
    } else if (none) {
      none.remove();
    }
    const count = document.getElementById("matrix-count");
    const filtering = words.length > 0 || drift.checked;
    if (count) count.textContent = filtering && rows.length ? shown + " of " + rows.length + " services" : "";
    const url = new URL(window.location.href);
    if (q.value) url.searchParams.set("q", q.value); else url.searchParams.delete("q");
    if (drift.checked) url.searchParams.set("drift", "1"); else url.searchParams.delete("drift");
    if (url.href !== window.location.href) window.history.replaceState(window.history.state, "", url);
  }

  document.body.addEventListener("htmx:afterSwap", function (e) { refreshTimes(e.target); filterMatrix(); });
  document.addEventListener("input", function (e) { if (e.target.id === "matrix-q") filterMatrix(); });
  document.addEventListener("change", function (e) { if (e.target.id === "matrix-drift") filterMatrix(); });

  // Header menus: one open at a time; outside clicks and Escape close them.
  function closeMenus(except) {
    document.querySelectorAll("details.menu[open]").forEach(function (d) { if (d !== except) d.removeAttribute("open"); });
  }
  document.addEventListener("click", function (e) { closeMenus(e.target.closest("details.menu")); });

  document.addEventListener("keydown", function (e) {
    const q = document.getElementById("matrix-q");
    const typing = e.target.closest("input, textarea, select, [contenteditable]");
    // "/" jumps to the matrix filter, as on GitHub; Escape in the filter clears it.
    if (q && e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); q.focus(); q.select(); return; }
    if (e.key !== "Escape") return;
    if (q && e.target === q && q.value) { q.value = ""; filterMatrix(); return; }
    const open = document.querySelector("details.menu[open]");
    if (open) { open.removeAttribute("open"); open.querySelector("summary").focus(); }
  });

  // Copy buttons for one-time secrets.
  document.addEventListener("click", function (e) {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const el = document.getElementById(btn.getAttribute("data-copy"));
    navigator.clipboard.writeText(el.textContent).then(
      function () { btn.textContent = "Copied"; },
      function () { window.getSelection().selectAllChildren(el); }
    );
  });

  // ---- every page load ----

  function init() {
    refreshTimes();
    showLive();
    const q = document.getElementById("matrix-q");
    const drift = document.getElementById("matrix-drift");
    if (q && drift) {
      const params = new URLSearchParams(window.location.search);
      q.value = params.get("q") || "";
      drift.checked = params.get("drift") === "1";
      filterMatrix();
    }
  }
  window.goliash = { init: init };
  init();
})();
