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
  // Forms that take a while (filling the Example workspace) say so on their button, and cannot be sent twice.
  document.addEventListener("submit", function (e) {
    const busy = e.target.getAttribute("data-busy");
    const button = e.target.querySelector("button[type=submit]");
    if (!busy || !button) return;
    setTimeout(function () { button.disabled = true; button.textContent = busy; }, 0);
  });

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

  // A click beside an application's card lays it down too, as on any modal: anywhere
  // on the page but the card, the small board of all applications and the controls.
  document.addEventListener("click", function (e) {
    const close = document.querySelector(".stack-close");
    if (!close || e.defaultPrevented || e.button !== 0) return;
    if (e.target.closest(".stack-front, .stack-back, a, button, input, select, textarea, label, summary, details, header, .tiles-toolbar, .tile-panel, dialog")) return;
    if (!e.target.closest("main")) return;
    close.click();
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
    if (url.indexOf("#goli:") === 0) return;
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
    // With Goli on, what was typed can be a question too: last, or first when nothing else matches.
    const raw = palette.input.value.trim();
    if (raw && paletteData && document.querySelector("[data-goli]")) {
      const ask = { label: "Ask Goli: " + raw, kind: "goli", url: "#goli:" + raw };
      if (items.length && raw.indexOf(" ") < 0) items.push(ask); else items.unshift(ask);
    }
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
    if (url.indexOf("#goli:") === 0) { goliOpen(url.slice(6)); return; }
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

  // Goli, the assistant: a panel beside the page. A question goes to /ui/goli, which streams
  // the answer as server-sent events. The conversation stays in this tab until cleared.
  // Answers are rendered from a little Markdown into DOM nodes, never markup: they quote
  // names, labels and release notes, which are anyone's input.
  const goliKey = "goliash.goli";
  let goli = null;
  function goliHistory() {
    try { const h = JSON.parse(sessionStorage.getItem(goliKey) || "[]"); return Array.isArray(h) ? h : []; } catch (_) { return []; }
  }
  function goliSave(h) { try { sessionStorage.setItem(goliKey, JSON.stringify(h.slice(-20))); } catch (_) { /* storage off */ } }
  function goliInline(parent, text) {
    const re = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      const t = m[0];
      if (t[0] === "*") parent.appendChild(el("strong", "", t.slice(2, -2)));
      else if (t[0] === "`") parent.appendChild(el("code", "", t.slice(1, -1)));
      else {
        const label = t.slice(1, t.indexOf("]("));
        const href = t.slice(t.indexOf("](") + 2, -1);
        // Pages of this Goliash, or https links; nothing else is a link.
        if ((href[0] === "/" && href[1] !== "/" && href[1] !== "\\") || /^https:\/\//.test(href)) {
          const a = el("a", "", label);
          a.href = href;
          if (href[0] !== "/") { a.target = "_blank"; a.rel = "noopener noreferrer"; }
          parent.appendChild(a);
        } else parent.appendChild(document.createTextNode(label));
      }
      last = m.index + t.length;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  }
  function goliMarkdown(text) {
    const out = document.createDocumentFragment();
    let list = null, para = null, code = null;
    text.split("\n").forEach(function (line) {
      if (code) {
        if (/^```/.test(line)) { code = null; return; }
        code.textContent += (code.textContent ? "\n" : "") + line;
        return;
      }
      if (/^```/.test(line)) { list = para = null; code = el("code"); const pre = el("pre"); pre.appendChild(code); out.appendChild(pre); return; }
      const item = /^\s*(?:[-*•]|(\d+)\.)\s+(.*)$/.exec(line);
      if (item) {
        para = null;
        const tag = item[1] ? "ol" : "ul";
        if (!list || list.tagName.toLowerCase() !== tag) { list = el(tag); out.appendChild(list); }
        const li = el("li");
        goliInline(li, item[2]);
        list.appendChild(li);
        return;
      }
      list = null;
      if (!line.trim()) { para = null; return; }
      const head = /^#{1,6}\s+(.*)$/.exec(line);
      if (head) { para = null; const h = el("p", "goli-head"); goliInline(h, head[1]); out.appendChild(h); return; }
      if (para) para.appendChild(document.createElement("br")); else { para = el("p"); out.appendChild(para); }
      goliInline(para, line);
    });
    return out;
  }
  const goliTools = {
    matrix: "the matrix", service: "a service", drifts: "drift", updates: "updates", changes: "history",
    vulnerabilities: "vulnerabilities", status: "data freshness", inventory: "the inventory", hygiene: "hygiene",
    delivery: "delivery", promotions: "promotions"
  };
  const goliIdeas = [
    "What should we upgrade first, and why?",
    "Which running images have vulnerabilities exploited in the wild?",
    "What drifted between staging and production this week?",
    "Is any of the data stale right now?"
  ];
  function goliRender() {
    const log = goli.log;
    log.replaceChildren();
    const h = goliHistory();
    if (!h.length && !goli.busy) {
      const empty = el("div", "goli-empty");
      empty.appendChild(el("p", "goli-hello", "Hi, I am Goli. Ask me about what runs where."));
      const ideas = el("div", "goli-ideas");
      goliIdeas.forEach(function (q) {
        const b = el("button", "goli-idea", q);
        b.type = "button";
        b.addEventListener("click", function () { goliAsk(q); });
        ideas.appendChild(b);
      });
      empty.appendChild(ideas);
      log.appendChild(empty);
    }
    h.forEach(function (t) {
      const m = el("div", "goli-msg " + (t.role === "user" ? "goli-q" : "goli-a"));
      if (t.role === "user") m.textContent = t.text; else m.appendChild(goliMarkdown(t.text));
      if (Array.isArray(t.looked)) m.appendChild(el("p", "goli-looking", "Looked at " + t.looked.join(", ")));
      if (t.error) m.appendChild(el("p", "goli-error", t.error));
      log.appendChild(m);
    });
    if (goli.busy) {
      const m = el("div", "goli-msg goli-a");
      if (goli.answer) m.appendChild(goliMarkdown(goli.answer));
      if (goli.looking.length) m.appendChild(el("p", "goli-looking", "Looked at " + goli.looking.join(", ") + (goli.answer ? "" : "…")));
      else if (!goli.answer) m.appendChild(el("p", "goli-looking", "Thinking…"));
      log.appendChild(m);
    }
    if (window.htmx) htmx.process(log);
    log.scrollTop = log.scrollHeight;
    goli.send.textContent = goli.busy ? "Stop" : "Ask";
    goli.clear.disabled = goli.busy || !h.length;
  }
  let goliFrame = 0;
  function goliLater() { if (!goliFrame) goliFrame = requestAnimationFrame(function () { goliFrame = 0; goliRender(); }); }
  function goliAsk(text) {
    text = (text || "").trim();
    if (!text || goli.busy) return;
    const h = goliHistory();
    h.push({ role: "user", text: text });
    goliSave(h);
    goli.input.value = "";
    goli.busy = true; goli.answer = ""; goli.looking = []; goli.error = "";
    goli.abort = new AbortController();
    goliRender();
    const turns = h.filter(function (t) { return t.text; }).map(function (t) { return { role: t.role, text: t.text }; });
    function finish() {
      const done = goliHistory();
      done.push({ role: "assistant", text: goli.answer, error: goli.error || undefined, looked: goli.looking.length ? goli.looking : undefined });
      goliSave(done);
      goli.busy = false; goli.abort = null;
      goliRender();
      goli.input.focus();
    }
    fetch("/ui/goli", {
      method: "POST", credentials: "same-origin", signal: goli.abort.signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ history: turns, page: location.pathname + location.search })
    }).then(function (r) {
      if (!r.ok || !r.body) return r.text().then(function (t) { goli.error = t.trim() || "Goli could not answer (" + r.status + ")."; });
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      function pump() {
        return reader.read().then(function (x) {
          if (x.done) return;
          buf += dec.decode(x.value, { stream: true });
          let i;
          while ((i = buf.indexOf("\n\n")) >= 0) {
            const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
            if (chunk.indexOf("data: ") !== 0) continue;
            let ev;
            try { ev = JSON.parse(chunk.slice(6)); } catch (_) { continue; }
            if (ev.type === "text") goli.answer += ev.text;
            else if (ev.type === "tool") { const n = goliTools[ev.tool] || ev.tool; if (goli.looking.indexOf(n) < 0) goli.looking.push(n); }
            else if (ev.type === "error") goli.error = ev.text;
          }
          goliLater();
          return pump();
        });
      }
      return pump();
    }).catch(function (e) {
      if (e && e.name === "AbortError") goli.error = "Stopped.";
      else goli.error = "Goli could not be reached. Check your connection.";
    }).then(finish);
  }
  function goliOpen(question) {
    if (!goli) {
      const panel = el("aside", "goli");
      panel.setAttribute("aria-label", "Ask Goli");
      const head = el("div", "goli-top");
      head.appendChild(el("strong", "goli-title", "Goli"));
      head.appendChild(el("span", "goli-sub", "reads Goliash for you"));
      const clear = el("button", "goli-clear", "New");
      clear.type = "button"; clear.title = "Start a new conversation";
      clear.addEventListener("click", function () { goliSave([]); goliRender(); goli.input.focus(); });
      const close = el("button", "goli-close", "×");
      close.type = "button"; close.setAttribute("aria-label", "Close Goli");
      close.addEventListener("click", goliClose);
      head.appendChild(clear); head.appendChild(close);
      const log = el("div", "goli-log");
      log.setAttribute("aria-live", "polite");
      const form = el("form", "goli-form");
      const input = el("textarea", "goli-input");
      input.rows = 2; input.maxLength = 2000;
      input.placeholder = "Ask about versions, drift, updates, vulnerabilities…";
      input.setAttribute("aria-label", "Your question");
      const send = el("button", "goli-send", "Ask");
      send.type = "submit";
      form.appendChild(input); form.appendChild(send);
      const note = el("p", "goli-note", "Goli reads what you may see here and sends it with your question to Claude, by Anthropic. It changes nothing. Check what matters.");
      panel.appendChild(head); panel.appendChild(log); panel.appendChild(form); panel.appendChild(note);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        if (goli.busy) { if (goli.abort) goli.abort.abort(); return; }
        goliAsk(input.value);
      });
      input.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); }
      });
      panel.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.stopPropagation(); goliClose(); } });
      goli = { panel: panel, log: log, input: input, send: send, clear: clear, busy: false, answer: "", looking: [], error: "", abort: null };
    }
    goli.open = true;
    goliKeep();
    goliRender();
    if (question) goliAsk(question); else goli.input.focus();
  }
  function goliClose() {
    if (!goli) return;
    goli.open = false;
    goli.panel.remove();
    document.body.classList.remove("goli-on");
    const btn = document.querySelector("[data-goli]");
    if (btn) btn.focus();
  }
  // A question ready to ask, e.g. the Brief button of an upgrade.
  document.addEventListener("click", function (e) {
    const ask = e.target.closest && e.target.closest("[data-goli-ask]");
    if (ask) { e.preventDefault(); goliOpen(ask.getAttribute("data-goli-ask")); }
  });
  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-goli]")) {
      e.preventDefault();
      if (goli && goli.open) goliClose(); else goliOpen();
    }
  });
  // A boosted navigation replaces the body; the panel and its conversation stay.
  function goliKeep() {
    if (!goli || !goli.open) return;
    if (!goli.panel.isConnected) document.body.appendChild(goli.panel);
    document.body.classList.add("goli-on");
  }
  document.body.addEventListener("htmx:afterSwap", goliKeep);
  document.body.addEventListener("htmx:afterSettle", goliKeep);

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

  // The add-channel form shows the fields of the chosen type only.
  function channelFields() {
    document.querySelectorAll("select[data-channel-type]").forEach(function (sel) {
      sel.form.querySelectorAll("[data-for]").forEach(function (f) {
        f.hidden = f.getAttribute("data-for").split(" ").indexOf(sel.value) < 0;
      });
    });
  }
  document.addEventListener("change", function (e) { if (e.target.matches("select[data-channel-type]")) channelFields(); });

  // Web push: "Notify this browser" next to a push channel. The browser holds one push
  // subscription (with the server's key), added to each push channel the person picks.
  function pushNote(text, bad) {
    const note = document.querySelector("[data-push-key]");
    if (!note) return;
    note.textContent = text;
    note.className = "push-note " + (bad ? "bad" : "good");
    note.hidden = !text;
  }
  function pushPost(url, body) {
    return fetch(url, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      .then(function (r) { return r.json().then(function (d) { if (!r.ok) throw new Error(d.error || "failed"); return d; }); });
  }
  function pushKeyBytes(b64) {
    const s = atob(b64.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((b64.length + 3) % 4));
    return Uint8Array.from(s, function (c) { return c.charCodeAt(0); });
  }
  function pushSupported() {
    return window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  }
  async function pushSetup() {
    const buttons = document.querySelectorAll("[data-push]");
    if (!buttons.length) return;
    if (window.Notification && Notification.permission === "denied") {
      pushNote("Notifications are blocked for this site; allow them in the browser's site settings to get push notifications here.", true);
    }
    if (!pushSupported()) {
      pushNote("This browser cannot get push notifications here: it needs HTTPS (or localhost), and on iPhone Goliash added to the home screen.", true);
      return;
    }
    let on = [];
    try {
      const reg = await navigator.serviceWorker.register("/sw.js");
      const sub = await reg.pushManager.getSubscription();
      if (sub) on = (await pushPost("/ui/push/status", { endpoint: sub.endpoint })).channels;
    } catch (_) { /* treat as not subscribed */ }
    buttons.forEach(function (b) {
      const subscribed = on.indexOf(b.getAttribute("data-channel")) >= 0;
      b.hidden = (b.getAttribute("data-push") === "subscribe") === subscribed;
    });
  }
  async function pushToggle(b) {
    const channel = b.getAttribute("data-channel");
    const key = document.querySelector("[data-push-key]").getAttribute("data-push-key");
    b.disabled = true;
    pushNote("Asking this browser…", false);
    try {
      const reg = await navigator.serviceWorker.register("/sw.js");
      await navigator.serviceWorker.ready;
      let sub = await reg.pushManager.getSubscription();
      if (b.getAttribute("data-push") === "subscribe") {
        if (await Notification.requestPermission() !== "granted") throw new Error("Notifications are blocked for this site; allow them in the browser's site settings.");
        const want = pushKeyBytes(key);
        const have = sub && sub.options.applicationServerKey && new Uint8Array(sub.options.applicationServerKey);
        if (sub && (!have || have.length !== want.length || have.some(function (x, i) { return x !== want[i]; }))) { await sub.unsubscribe(); sub = null; }
        if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: want });
        pushNote((await pushPost("/notifications/channels/" + channel + "/push", sub.toJSON())).notice, false);
      } else if (sub) {
        pushNote((await pushPost("/notifications/channels/" + channel + "/push/delete", { endpoint: sub.endpoint })).notice, false);
      }
    } catch (err) {
      pushNote("This browser could not subscribe: " + (err.message || String(err)), true);
    } finally {
      b.disabled = false;
      pushSetup();
    }
  }
  document.addEventListener("click", function (e) {
    const b = e.target.closest && e.target.closest("button[data-push]");
    if (b) { e.preventDefault(); pushToggle(b); }
  });

  // ---- time travel ----
  // The timeline scrubs through the past. Dragging redraws only the grid, versions that differ from the
  // moment shown before slide in, ← → step from deployment to deployment, and Play steps through them all.
  let tl = null; // this page's timeline
  const tlFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", timeZone: "UTC" });
  const tlDay = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
  function tlIso(sec) { return new Date(sec * 1000).toISOString().slice(0, 16); }
  function tlParse(iso) { return iso ? Math.floor(Date.parse(iso + ":00Z") / 1000) : null; }
  function tlAgo(sec) {
    const secs = sec - Date.now() / 1000;
    for (const [unit, size] of units) {
      if (Math.abs(secs) >= size) return fmt.format(Math.round(secs / size), unit);
    }
    return "just now";
  }

  document.addEventListener("htmx:beforeRequest", function (e) {
    // The live refresh shows now; while the timeline shows the past it waits.
    const elt = e.detail.elt;
    if (tl && tl.at !== null && elt && (elt.id === "matrix-panel" || elt.classList.contains("tiles-live"))) e.preventDefault();
  });

  function tlSetup() {
    if (tl && tl.playing) clearInterval(tl.playing);
    tl = null;
    const root = document.getElementById("timeline");
    if (!root) return;
    tl = {
      root: root, range: root.querySelector("[data-tl-range]"), group: root.getAttribute("data-group") || "",
      kind: root.getAttribute("data-kind") || "matrix", base: root.getAttribute("data-base") || "/",
      at: tlParse(root.getAttribute("data-at")), from: 0, to: 0, changes: [], seq: 0, timer: null, playing: null, loaded: false,
    };
    const box = root.querySelector("[data-tl-compare]");
    if (box) {
      try { box.checked = localStorage.getItem("goliash.compareNow") === "1"; } catch (_) { /* private mode */ }
    }
    if (!root.hidden) tlLoad().then(function () { tlCompare(); });
  }

  async function tlLoad() {
    if (!tl || tl.loaded) return;
    tl.loaded = true;
    let data;
    try {
      data = await (await fetch("/ui/timeline", { headers: { Accept: "application/json" } })).json();
    } catch (_) { return; } // the form below the track still works
    if (!tl) return;
    tl.from = data.from; tl.to = data.to; tl.changes = data.changes || [];
    const r = tl.range;
    r.min = String(tl.from); r.max = String(tl.to);
    r.value = String(tl.at === null ? tl.to : Math.max(tl.from, tl.at));
    tl.root.querySelector("[data-tl-from]").textContent = tlDay.format(new Date(tl.from * 1000));
    tlTicks();
    ["[data-tl-track]", "[data-tl-buttons]", "[data-tl-presets]"].forEach(function (s) { tl.root.querySelector(s).hidden = false; });
    tlLabel(Number(r.value));
  }

  // tlTicks draws the deployments on the track: one bar per slice of time, taller where more changed.
  function tlTicks() {
    const svg = tl.root.querySelector("[data-tl-ticks]");
    svg.replaceChildren();
    const n = 200, span = Math.max(1, tl.to - tl.from), counts = new Array(n).fill(0);
    tl.changes.forEach(function (c) { counts[Math.min(n - 1, Math.floor((c - tl.from) / span * n))]++; });
    const most = Math.max(1, ...counts);
    counts.forEach(function (c, i) {
      if (!c) return;
      const h = 6 + 22 * Math.sqrt(c / most);
      svg.appendChild(svgEl("rect", { x: i * 1000 / n + 1, y: 28 - h, width: 1000 / n - 2, height: h, rx: 1.5 }));
    });
  }

  function tlLabel(sec) {
    const label = tl.root.querySelector("[data-tl-label]"), note = tl.root.querySelector("[data-tl-note]");
    const now = sec >= tl.to;
    label.textContent = now ? "Now" : tlFmt.format(new Date(sec * 1000)) + " UTC";
    tl.root.querySelector("[data-tl-now]").hidden = now;
    const since = tl.changes.filter(function (c) { return c > sec; }).length;
    note.textContent = now ? "drag back to see what ran before" : tlAgo(sec) + (since ? " · " + since + (since === 1 ? " deployment" : " deployments") + " since" : "");
  }

  // tlSnap pulls a dragged moment onto a deployment close by, so the marks are easy to hit.
  function tlSnap(sec) {
    const near = (tl.to - tl.from) * 0.005;
    let best = sec, dist = near;
    tl.changes.forEach(function (c) { if (Math.abs(c - sec) <= dist) { best = c; dist = Math.abs(c - sec); } });
    return best;
  }

  function tlStep(dir) {
    const cur = Number(tl.range.value);
    let target = null;
    if (dir > 0) {
      target = tl.changes.find(function (c) { return c > cur + 30; });
      if (target === undefined) target = tl.to;
    } else {
      for (let i = tl.changes.length - 1; i >= 0; i--) if (tl.changes[i] < cur - 30) { target = tl.changes[i]; break; }
      if (target === null) target = tl.from;
    }
    tlGo(target);
    return target < tl.to;
  }

  function tlGo(sec, later) {
    sec = Math.min(tl.to, Math.max(tl.from, sec));
    tl.range.value = String(sec);
    tlLabel(sec);
    clearTimeout(tl.timer);
    if (later) tl.timer = setTimeout(function () { tlShow(sec); }, later);
    else tlShow(sec);
  }

  // tlURL is the page (or, for the matrix, its grid) as of sec; now leaves the moment out.
  function tlURL(sec, now, grid) {
    const u = new URL(tl.base, location.origin);
    if (now) u.searchParams.delete("at"); else u.searchParams.set("at", tlIso(sec));
    if (grid && tl.kind === "matrix") return "/ui/matrix?" + u.searchParams.toString();
    return u.pathname + "?" + u.searchParams.toString();
  }

  async function tlShow(sec) {
    const seq = ++tl.seq;
    const now = sec >= tl.to;
    const tiles = tl.kind === "tiles";
    const panel = tiles ? document.querySelector(".tiles-live") : document.getElementById("matrix-panel");
    if (!panel) return;
    const keyed = tiles ? ".board-cell[data-key]" : "td[data-key]";
    const what = tiles ? function (el) { return el.textContent.replace(/\s+/g, " ").trim(); } : tlVersions;
    panel.classList.add("tl-loading");
    let html;
    try {
      const res = await fetch(tlURL(sec, now, true), { headers: { "HX-Request": "true" } });
      if (!res.ok) throw new Error(res.statusText);
      html = await res.text();
    } catch (_) { panel.classList.remove("tl-loading"); return; }
    if (!tl || seq !== tl.seq) return; // a later moment was asked for meanwhile
    tl.at = now ? null : sec;
    const before = new Map();
    panel.querySelectorAll(keyed).forEach(function (el) { before.set(el.getAttribute("data-key"), what(el)); });
    let target = panel;
    if (tiles) {
      // The whole page came back; its board replaces this one.
      const fresh = new DOMParser().parseFromString(html, "text/html").querySelector(".tiles-live");
      if (!fresh) { panel.classList.remove("tl-loading"); return; }
      panel.replaceWith(fresh);
      target = fresh;
    } else {
      panel.innerHTML = html;
      panel.classList.remove("tl-loading");
    }
    htmx.process(target);
    refreshTimes(target);
    if (!tiles) filterMatrix();
    let changed = 0;
    target.querySelectorAll(keyed).forEach(function (el) {
      const old = before.get(el.getAttribute("data-key"));
      if (old === undefined) { if (before.size) el.classList.add("tl-in"); return; }
      if (old !== what(el)) { el.classList.add("tl-changed"); changed++; }
    });
    // The address, the heading and the links follow, so the moment can be shared and reloaded.
    const url = tiles ? tlURL(sec, now, false) : "/?" + new URL(tlURL(sec, now, false), location.origin).searchParams.toString();
    history.replaceState(history.state, "", url);
    if (tiles) {
      document.querySelectorAll("a[href^='/tiles']").forEach(function (a) {
        const u = new URL(a.getAttribute("href"), location.origin);
        if (now) u.searchParams.delete("at"); else u.searchParams.set("at", tlIso(sec));
        a.setAttribute("href", u.pathname + "?" + u.searchParams.toString() + u.hash);
      });
    }
    document.querySelectorAll("[data-tl-asof]").forEach(function (p) { p.hidden = now; });
    document.querySelectorAll("[data-tl-live]").forEach(function (p) { p.hidden = !now; });
    const asof = document.querySelector("[data-tl-asof-date]");
    if (asof && !now) asof.textContent = tlIso(sec).replace("T", " ") + " UTC";
    const input = tl.root.querySelector("[data-tl-input]");
    if (input && !now) input.value = tlIso(sec);
    document.querySelectorAll(".toolbar .segmented a[href^='/?']").forEach(function (a) {
      const u = new URL(a.href, location.origin);
      if (now) u.searchParams.delete("at"); else u.searchParams.set("at", tlIso(sec));
      a.setAttribute("href", u.pathname + "?" + u.searchParams.toString());
    });
    const label = tl.root.querySelector("[data-tl-label]");
    label.classList.remove("tl-pop"); void label.offsetWidth; label.classList.add("tl-pop");
    if (!tiles) tlCompare();
    if (changed) tl.root.querySelector("[data-tl-note]").textContent += " · " + changed + (tiles ? (changed === 1 ? " cell changed" : " cells changed") : (changed === 1 ? " version changed" : " versions changed"));
  }

  // Compare with now: the matrix of today, fetched once a minute at most, marks every cell of the past whose
  // versions differ from today's with what runs now. The choice is remembered in this browser.
  let tlNowGrid = null, tlNowAt = 0;
  async function tlCompare() {
    const box = tl && tl.root.querySelector("[data-tl-compare]");
    const panel = document.getElementById("matrix-panel");
    if (!box || !panel) return;
    panel.querySelectorAll(".tl-now-note").forEach(function (n) { n.remove(); });
    panel.querySelectorAll("td.tl-differs").forEach(function (td) { td.classList.remove("tl-differs"); });
    if (!box.checked || tl.at === null) return;
    if (!tlNowGrid || Date.now() - tlNowAt > 60000) {
      try {
        const res = await fetch("/ui/matrix?" + new URLSearchParams({ group: tl.group }).toString(), { headers: { "HX-Request": "true" } });
        if (!res.ok) return;
        const doc = new DOMParser().parseFromString(await res.text(), "text/html");
        tlNowGrid = new Map();
        doc.querySelectorAll("td[data-key]").forEach(function (td) { tlNowGrid.set(td.getAttribute("data-key"), tlVersions(td)); });
        tlNowAt = Date.now();
      } catch (_) { return; }
    }
    if (!tl || tl.at === null) return;
    let differ = 0;
    panel.querySelectorAll("td[data-key]").forEach(function (td) {
      const then = tlVersions(td), now = tlNowGrid.get(td.getAttribute("data-key")) || "";
      if (then === now) return;
      td.classList.add("tl-differs");
      const note = document.createElement("div");
      note.className = "tl-now-note";
      note.textContent = now ? "now " + now.split(",").join(", ") : "not running now";
      td.appendChild(note);
      differ++;
    });
    const label = tl.root.querySelector(".tl-compare span");
    if (label) label.textContent = "Compare with now" + (differ ? " · " + differ + (differ === 1 ? " differs" : " differ") : " · nothing differs");
  }
  document.addEventListener("change", function (e) {
    if (!tl || !e.target.matches("[data-tl-compare]")) return;
    try { localStorage.setItem("goliash.compareNow", e.target.checked ? "1" : ""); } catch (_) { /* private mode */ }
    if (!e.target.checked) { const l = tl.root.querySelector(".tl-compare span"); if (l) l.textContent = "Compare with now"; }
    tlCompare();
  });

  // tlVersions is what a cell runs, the versions only: badges for drift and releases exist for now alone.
  function tlVersions(td) {
    return Array.from(td.querySelectorAll(".ver .tag")).map(function (t) { return t.textContent.trim(); }).join(",");
  }

  function tlPlay() {
    const btn = tl.root.querySelector("[data-tl-play]");
    if (tl.playing) {
      clearInterval(tl.playing); tl.playing = null; btn.textContent = "Play"; btn.classList.remove("playing"); return;
    }
    if (Number(tl.range.value) >= tl.to && tl.changes.length) tlGo(tl.changes[Math.max(0, tl.changes.length - 30)]);
    btn.textContent = "Pause";
    btn.classList.add("playing");
    tl.playing = setInterval(function () {
      if (!tl || !tlStep(1)) { if (tl) { clearInterval(tl.playing); tl.playing = null; btn.textContent = "Play"; btn.classList.remove("playing"); } }
    }, 1200);
  }

  document.addEventListener("click", function (e) {
    if (!tl) return;
    const t = e.target.closest("[data-tl-open], [data-tl-prev], [data-tl-next], [data-tl-play], [data-tl-jump]");
    if (!t) return;
    if (t.hasAttribute("data-tl-open")) {
      e.preventDefault();
      t.hidden = true;
      tl.root.hidden = false;
      tlLoad().then(function () { if (tl) tl.range.focus(); });
    } else if (t.hasAttribute("data-tl-prev")) tlStep(-1);
    else if (t.hasAttribute("data-tl-next")) tlStep(1);
    else if (t.hasAttribute("data-tl-play")) tlPlay();
    else tlGo(tl.to + Number(t.getAttribute("data-tl-jump")));
  });
  // Scrolled down to the grid, the timeline stays at the top, compact, so moving it shows what changes.
  window.addEventListener("scroll", function () {
    if (!tl || tl.root.hidden) return;
    const top = document.querySelector(".top");
    const h = top ? top.offsetHeight : 0;
    document.documentElement.style.setProperty("--top-h", h + "px");
    tl.root.classList.toggle("tl-stuck", tl.root.getBoundingClientRect().top <= h + 1 && window.scrollY > 0);
  }, { passive: true });
  document.addEventListener("input", function (e) {
    if (!tl || e.target !== tl.range) return;
    const sec = tlSnap(Number(tl.range.value));
    tlGo(sec, 180);
  });
  document.addEventListener("keydown", function (e) {
    if (!tl || e.target !== tl.range) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      const dir = e.key === "ArrowRight" ? 1 : -1;
      if (e.shiftKey) tlGo(Number(tl.range.value) + dir * 86400, 120);
      else tlStep(dir);
    } else if (e.key === " ") {
      e.preventDefault();
      tlPlay();
    }
  });

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
    tlSetup();
    pushSetup();
    channelFields();
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
