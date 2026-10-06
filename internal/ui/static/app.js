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
    document.querySelectorAll("table.matrix tbody.mgroup").forEach(function (g) {
      g.classList.toggle("filtered", !g.querySelector("tr[data-search]:not(.filtered)"));
    });
    const tbody = document.querySelector("table.matrix tbody:last-of-type");
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
  document.addEventListener("input", function (e) {
    if (e.target.id === "matrix-q") filterMatrix();
    if (e.target.matches("input[data-filter]")) filterCards(e.target);
  });

  // A filter box for cards (data-filter names their selector): words must all appear
  // in a card's data-search; groups without a visible card hide too.
  function filterCards(input) {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    document.querySelectorAll(input.getAttribute("data-filter")).forEach(function (el) {
      const hay = el.getAttribute("data-search") || "";
      el.hidden = !words.every(function (w) { return hay.indexOf(w) >= 0; });
    });
    document.querySelectorAll("[data-group]").forEach(function (g) {
      g.hidden = !g.querySelector("[data-search]:not([hidden])");
    });
  }
  document.addEventListener("change", function (e) { if (e.target.id === "matrix-drift") filterMatrix(); });

  // Header menus: one open at a time; outside clicks and Escape close them.
  function closeMenus(except) {
    document.querySelectorAll("details.menu[open]").forEach(function (d) { if (d !== except) d.removeAttribute("open"); });
  }
  document.addEventListener("click", function (e) { closeMenus(e.target.closest("details.menu")); });

  document.addEventListener("keydown", function (e) {
    const q = document.getElementById("matrix-q");
    const typing = e.target.closest && e.target.closest("input, textarea, select, [contenteditable]");
    // "/" jumps to the matrix filter, as on GitHub; Escape in the filter clears it.
    if (q && e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); q.focus(); q.select(); return; }
    if (e.key !== "Escape") return;
    if (q && e.target === q && q.value) { q.value = ""; filterMatrix(); return; }
    const open = document.querySelector("details.menu[open]");
    if (open) { open.removeAttribute("open"); open.querySelector("summary").focus(); return; }
    // A tile's details panel is open while its id is the URL's fragment.
    const panel = window.location.hash && document.querySelector(".tile-panel" + CSS.escape(window.location.hash));
    if (panel) {
      const key = window.location.hash.slice(1);
      window.location.hash = "";
      const cell = document.querySelector('[data-key="' + CSS.escape(key) + '"]');
      if (cell) cell.focus();
      return;
    }
    // Drilled into an application: Escape lays its card down, back to the board.
    const close = !typing && document.querySelector(".stack-close");
    if (close) close.click();
  });

  // Selects that act at once (role, workspace, collector) and the print button. The
  // Content-Security-Policy allows no inline handlers, so they live here.
  document.addEventListener("change", function (e) {
    if (e.target.matches("select[data-autosubmit]") && e.target.form) e.target.form.requestSubmit();
  });
  document.addEventListener("click", function (e) {
    if (e.target.closest("[data-print]")) window.print();
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

  // Chart hover, SigNoz-style: a crosshair at the nearest point and a tooltip with
  // every series' value there. The server puts what it needs in data-chart.
  const chartData = new WeakMap();
  let hoveredChart = null;
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function svgEl(tag, attrs) {
    const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const k in attrs) e.setAttribute(k, attrs[k]);
    return e;
  }
  function leaveChart() {
    if (!hoveredChart) return;
    hoveredChart.classList.remove("hovering");
    hoveredChart.querySelectorAll(".bar.active").forEach(function (b) { b.classList.remove("active"); });
    hoveredChart.querySelectorAll(".hover-dot").forEach(function (d) { d.remove(); });
    hoveredChart = null;
  }
  function hoverChart(fig, e) {
    let d = chartData.get(fig);
    if (!d) {
      try { d = JSON.parse(fig.getAttribute("data-chart")); } catch (err) { return; }
      chartData.set(fig, d);
    }
    if (!d.xs || !d.xs.length || !d.series || !d.series.length) return;
    const svg = fig.querySelector("svg");
    const ctm = svg.getScreenCTM();
    if (!ctm) return;
    const pt = svg.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    const x = pt.matrixTransform(ctm.inverse()).x;
    let i = 0;
    for (let k = 1; k < d.xs.length; k++) if (Math.abs(d.xs[k] - x) < Math.abs(d.xs[i] - x)) i = k;
    if (hoveredChart !== fig) { leaveChart(); hoveredChart = fig; }
    if (fig.dataset.hoverIndex === String(i) && fig.classList.contains("hovering")) { placeTip(fig, svg, d, i, e); return; }
    fig.dataset.hoverIndex = String(i);
    fig.classList.add("hovering");

    const cx = d.xs[i];
    if (d.bars) {
      const slot = d.xs.length > 1 ? d.xs[1] - d.xs[0] : 40;
      const band = svg.querySelector(".cross-band");
      if (band) { band.setAttribute("x", cx - slot / 2); band.setAttribute("width", slot); }
      svg.querySelectorAll(".bar").forEach(function (b) { b.classList.toggle("active", b.getAttribute("data-i") === String(i)); });
    } else {
      const cross = svg.querySelector(".cross");
      if (cross) { cross.setAttribute("x1", cx); cross.setAttribute("x2", cx); }
      svg.querySelectorAll(".hover-dot").forEach(function (h) { h.remove(); });
      d.series.forEach(function (s) {
        if (s.ys) svg.appendChild(svgEl("circle", { class: "hover-dot " + s.cls, cx: cx, cy: s.ys[i], r: 4.5 }));
      });
    }

    let tip = fig.querySelector(".chart-tip");
    if (!tip) { tip = el("div", "chart-tip"); tip.setAttribute("aria-hidden", "true"); fig.appendChild(tip); }
    tip.replaceChildren();
    tip.appendChild(el("div", "tip-head", (d.bars ? "Week of " : "") + d.labels[i]));
    const rows = d.series.map(function (s) { return { s: s, v: s.values[i] || 0 }; })
      .sort(function (a, b) { return b.v - a.v; });
    let total = 0;
    rows.forEach(function (r) {
      total += r.v;
      const row = el("div", "tip-row");
      row.appendChild(el("span", "swatch " + r.s.cls));
      row.appendChild(el("span", "tip-name", r.s.name));
      row.appendChild(el("span", "tip-val", String(r.v)));
      tip.appendChild(row);
    });
    if (d.bars && rows.length > 1) {
      const row = el("div", "tip-row tip-total");
      row.appendChild(el("span", "tip-name", "Total"));
      row.appendChild(el("span", "tip-val", String(total)));
      tip.appendChild(row);
    }
    placeTip(fig, svg, d, i, e);
  }
  function placeTip(fig, svg, d, i, e) {
    const tip = fig.querySelector(".chart-tip");
    if (!tip) return;
    const ctm = svg.getScreenCTM();
    const pt = svg.createSVGPoint();
    pt.x = d.xs[i]; pt.y = 0;
    const sx = pt.matrixTransform(ctm).x;
    const fr = fig.getBoundingClientRect();
    let left = sx - fr.left + fig.scrollLeft + 16;
    if (left + tip.offsetWidth > fig.clientWidth + fig.scrollLeft - 4) left = sx - fr.left + fig.scrollLeft - tip.offsetWidth - 16;
    let top = e.clientY - fr.top - tip.offsetHeight / 2;
    top = Math.max(4, Math.min(top, fig.clientHeight - tip.offsetHeight - 4));
    tip.style.left = left + "px";
    tip.style.top = top + "px";
  }
  document.addEventListener("pointermove", function (e) {
    const fig = e.target.closest && e.target.closest("figure[data-chart]");
    if (fig && e.target.closest("svg")) hoverChart(fig, e);
    else leaveChart();
  });
  document.addEventListener("pointerleave", leaveChart);

  // Tiles: after a live refresh, cells whose state changed pulse once, and the
  // favicon (the logo in the workspace's colours) is redrawn.
  let tileStates = null;
  document.body.addEventListener("htmx:beforeSwap", function (e) {
    if (!e.detail.target.classList || !e.detail.target.classList.contains("tiles-live")) return;
    tileStates = {};
    e.detail.target.querySelectorAll("[data-key]").forEach(function (c) { tileStates[c.getAttribute("data-key")] = c.getAttribute("data-state"); });
  });
  document.body.addEventListener("htmx:afterSettle", function () {
    if (!tileStates) return;
    document.querySelectorAll(".tiles-live [data-key]").forEach(function (c) {
      const was = tileStates[c.getAttribute("data-key")];
      if (was && was !== c.getAttribute("data-state")) c.classList.add("changed");
    });
    tileStates = null;
  });
  document.body.addEventListener("goliash:changed", function () {
    const icon = document.getElementById("favicon");
    if (icon && icon.getAttribute("href").indexOf("/ui/favicon.svg") === 0) icon.setAttribute("href", "/ui/favicon.svg?t=" + Date.now());
  });

  // Arrow keys move between the cells of a tiles board, to the nearest cell in that
  // direction (cells differ in size, so by geometry, not by index). Enter opens one.
  document.addEventListener("keydown", function (e) {
    const dirs = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
    const d = dirs[e.key];
    const from = d && e.target.closest && e.target.closest(".logo-board .board-cell");
    if (!from || e.altKey || e.metaKey || e.ctrlKey) return;
    const board = from.closest(".logo-board");
    const r = from.getBoundingClientRect();
    const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
    let best = null, bestScore = Infinity;
    board.querySelectorAll(".board-cell").forEach(function (c) {
      if (c === from) return;
      const b = c.getBoundingClientRect();
      const dx = b.left + b.width / 2 - cx, dy = b.top + b.height / 2 - cy;
      const along = dx * d[0] + dy * d[1];
      if (along <= 1) return; // not in that direction
      const across = Math.abs(dx * d[1]) + Math.abs(dy * d[0]);
      const score = along + across * 2;
      if (score < bestScore) { best = c; bestScore = score; }
    });
    if (best) { e.preventDefault(); best.focus(); }
  });
  // The command palette: Ctrl+K (⌘K) or the header's search button. Its entries come
  // from /ui/palette.json once, again after the workspace changed. Built from DOM
  // nodes, never markup: names are user input.
  let paletteData = null;
  let palette = null;
  function fuzzy(q, text) {
    // Every letter of q in order; consecutive letters and word starts score higher.
    text = text.toLowerCase();
    let score = 0, ti = 0, prev = -2;
    for (const ch of q) {
      const i = text.indexOf(ch, ti);
      if (i < 0) return -1;
      score += i === prev + 1 ? 3 : 1;
      if (i === 0 || " -_./".indexOf(text[i - 1]) >= 0) score += 2;
      prev = i; ti = i + 1;
    }
    return score - text.length / 100;
  }
  // The last few picks, per browser; storage may be off (private windows), then none.
  const recentKey = "goliash.palette.recent";
  function paletteRecent() {
    try { const r = JSON.parse(localStorage.getItem(recentKey) || "[]"); return Array.isArray(r) ? r.slice(0, 5) : []; } catch (_) { return []; }
  }
  function paletteRemember(url) {
    try { localStorage.setItem(recentKey, JSON.stringify([url].concat(paletteRecent().filter(function (u) { return u !== url; })).slice(0, 5))); } catch (_) { /* storage off */ }
  }
  function paletteRender() {
    const q = palette.input.value.trim().toLowerCase();
    let items = paletteData || [];
    const recent = paletteRecent();
    if (!q) {
      // Nothing typed: the places picked last come first.
      const first = recent.map(function (u) { return items.find(function (e) { return e.url === u; }); }).filter(Boolean);
      items = first.concat(items.filter(function (e) { return first.indexOf(e) < 0; }));
    } else {
      items = items.map(function (e) { return { e: e, s: Math.max(fuzzy(q, e.label), fuzzy(q, e.label + " " + (e.sub || "")) - 2) + (recent.indexOf(e.url) >= 0 ? 1 : 0) }; })
        .filter(function (x) { return x.s >= 0; })
        .sort(function (a, b) { return b.s - a.s; })
        .map(function (x) { return x.e; });
    }
    items = items.slice(0, 50);
    palette.list.replaceChildren();
    if (!paletteData) { palette.list.appendChild(el("li", "palette-empty", "Loading…")); return; }
    if (!items.length) { palette.list.appendChild(el("li", "palette-empty", "Nothing matches.")); return; }
    items.forEach(function (e, i) {
      const li = el("li", "palette-item");
      li.setAttribute("role", "option");
      li.dataset.url = e.url;
      li.id = "palette-opt-" + i;
      const mark = el("span", "palette-mark " + (e.state ? "tcell " + e.state : "kind-" + e.kind));
      li.appendChild(mark);
      const text = el("span", "palette-text");
      text.appendChild(el("span", "palette-label", e.label));
      if (e.sub) text.appendChild(el("span", "palette-sub", e.sub));
      li.appendChild(text);
      li.appendChild(el("span", "palette-kind", !q && recent.indexOf(e.url) >= 0 ? "recent" : e.kind));
      li.addEventListener("mousemove", function () { paletteSelect(i); });
      li.addEventListener("click", function () { paletteRemember(e.url); paletteGo(e.url); });
      palette.list.appendChild(li);
    });
    paletteSelect(0);
  }
  function paletteSelect(i) {
    const opts = palette.list.querySelectorAll(".palette-item");
    if (!opts.length) return;
    i = (i + opts.length) % opts.length;
    opts.forEach(function (o, j) { o.classList.toggle("on", j === i); o.setAttribute("aria-selected", j === i ? "true" : "false"); });
    palette.sel = i;
    palette.input.setAttribute("aria-activedescendant", opts[i].id);
    opts[i].scrollIntoView({ block: "nearest" });
  }
  function paletteGo(url) {
    paletteClose();
    if (url === "#shortcuts") { shortcutsOpen(); return; }
    // A link clicked like any other: hx-boost swaps the page, sets the title and history.
    const a = document.createElement("a");
    a.href = url;
    a.hidden = true;
    document.body.appendChild(a);
    if (window.htmx) htmx.process(a);
    a.click();
    a.remove();
  }
  function paletteOpen() {
    if (!palette) {
      const back = el("div", "palette-backdrop");
      const box = el("div", "palette");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-label", "Search");
      const input = el("input", "palette-input");
      input.type = "search";
      input.placeholder = "Jump to a service, an application, a target or a page…";
      input.setAttribute("role", "combobox");
      input.setAttribute("aria-controls", "palette-list");
      input.setAttribute("aria-expanded", "true");
      input.autocomplete = "off";
      const list = el("ul", "palette-list");
      list.id = "palette-list";
      list.setAttribute("role", "listbox");
      box.appendChild(input);
      box.appendChild(list);
      const hint = el("div", "palette-hint", "↑ ↓ to move · Enter to open · Esc to close");
      box.appendChild(hint);
      back.appendChild(box);
      back.addEventListener("mousedown", function (e) { if (e.target === back) paletteClose(); });
      input.addEventListener("input", paletteRender);
      input.addEventListener("keydown", function (e) {
        if (e.key === "ArrowDown") { e.preventDefault(); paletteSelect(palette.sel + 1); }
        else if (e.key === "ArrowUp") { e.preventDefault(); paletteSelect(palette.sel - 1); }
        else if (e.key === "Enter") {
          e.preventDefault();
          const on = list.querySelector(".palette-item.on");
          if (on) { paletteRemember(on.dataset.url); paletteGo(on.dataset.url); }
        } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); paletteClose(); }
      });
      palette = { back: back, input: input, list: list, sel: 0 };
    }
    document.body.appendChild(palette.back);
    palette.input.value = "";
    palette.input.focus();
    paletteRender();
    if (!paletteData) {
      fetch("/ui/palette.json", { credentials: "same-origin" }).then(function (r) { return r.ok ? r.json() : []; })
        .then(function (d) { paletteData = d || []; if (palette.back.isConnected) paletteRender(); });
    }
  }
  function paletteClose() { if (palette && palette.back.isConnected) palette.back.remove(); }
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
      if (!document.querySelector("[data-palette]")) return; // signed out
      e.preventDefault();
      if (palette && palette.back.isConnected) paletteClose(); else paletteOpen();
    }
  });
  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-palette]")) { e.preventDefault(); paletteOpen(); }
  });
  document.body.addEventListener("goliash:changed", function () { paletteData = null; });
  document.body.addEventListener("htmx:beforeSwap", function (e) { if (e.detail.target === document.body) paletteClose(); });

  // Keyboard shortcuts: "?" lists them; "g" then a letter goes to a page, as on GitHub.
  const goKeys = { m: ["/?view=table", "Matrix"], t: ["/tiles", "Tiles"], u: ["/updates", "Updates"], i: ["/inbox", "Inbox"], d: ["/delivery", "Delivery"], h: ["/events", "History"], n: ["/notifications", "Notifications"] };
  let shortcuts = null;
  let goPending = 0;
  function shortcutsOpen() {
    if (!shortcuts) {
      const back = el("div", "palette-backdrop");
      const box = el("div", "palette shortcuts");
      box.setAttribute("role", "dialog");
      box.setAttribute("aria-label", "Keyboard shortcuts");
      box.tabIndex = -1;
      box.appendChild(el("h2", "shortcuts-title", "Keyboard shortcuts"));
      const dl = el("dl", "shortcuts-list");
      const rows = [
        [[/Mac|iPhone|iPad/.test(navigator.platform) ? "⌘" : "Ctrl", "K"], "Jump to a service, an application, a target or a page"],
        [["/"], "Filter the matrix"],
        [["←", "↑", "→", "↓"], "Move between tiles; Enter opens one"],
        [["Esc"], "Close a panel, a card or a menu"],
        [["?"], "This list"],
      ];
      Object.keys(goKeys).forEach(function (k) { rows.push([["g", k], "Go to " + goKeys[k][1]]); });
      rows.forEach(function (r) {
        const dt = el("dt");
        r[0].forEach(function (k) { dt.appendChild(el("kbd", null, k)); });
        dl.appendChild(dt);
        dl.appendChild(el("dd", null, r[1]));
      });
      box.appendChild(dl);
      box.appendChild(el("div", "palette-hint", "Esc to close"));
      back.appendChild(box);
      back.addEventListener("mousedown", function (e) { if (e.target === back) shortcutsClose(); });
      shortcuts = { back: back, box: box };
    }
    document.body.appendChild(shortcuts.back);
    shortcuts.box.focus();
  }
  function shortcutsClose() { if (shortcuts && shortcuts.back.isConnected) { shortcuts.back.remove(); return true; } return false; }
  document.addEventListener("keydown", function (e) {
    const typing = e.target.closest && e.target.closest("input, textarea, select, [contenteditable]");
    if (e.key === "Escape" && shortcutsClose()) { e.preventDefault(); e.stopImmediatePropagation(); return; }
    if (typing || e.ctrlKey || e.metaKey || e.altKey || !document.querySelector("[data-palette]")) return;
    if (e.key === "?") { e.preventDefault(); if (!shortcutsClose()) shortcutsOpen(); return; }
    if (goPending && Date.now() - goPending < 1500 && goKeys[e.key]) { goPending = 0; e.preventDefault(); shortcutsClose(); paletteGo(goKeys[e.key][0]); return; }
    goPending = e.key === "g" ? Date.now() : 0;
  }, true);
  document.body.addEventListener("htmx:beforeSwap", function (e) { if (e.detail.target === document.body) shortcutsClose(); });

  // ---- every page load ----

  // TV mode cycling: after its seconds, follow the next environment's link.
  let cycleTimer = null;
  function cycle() {
    clearTimeout(cycleTimer);
    const next = document.querySelector("a[data-cycle]");
    if (!next) return;
    cycleTimer = setTimeout(function () { if (next.isConnected) next.click(); }, Number(next.getAttribute("data-cycle")) * 1000);
  }

  function init() {
    cycle();
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
