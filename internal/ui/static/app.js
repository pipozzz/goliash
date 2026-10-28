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
    es.addEventListener("version", function (e) { if (e.data && e.data !== pageVersion()) upgraded(); });
    es.onopen = function () { online = true; showLive(); };
    es.onerror = function () { online = false; showLive(); };
  }
  // After an upgrade the page still runs the old release's code. In the background (another tab, the installed
  // app out of sight) it reloads when it is looked at again; in front of someone, it offers to, without taking
  // the page away from them.
  function pageVersion() {
    const css = document.querySelector('link[rel="stylesheet"][href*="app.css"]');
    const m = css && /[?&]v=([^&]+)/.exec(css.getAttribute("href"));
    return m ? m[1] : "";
  }
  let upgradeShown = false;
  function upgraded() {
    if (document.visibilityState === "hidden") {
      document.addEventListener("visibilitychange", function () {
        if (document.visibilityState === "visible") location.reload();
      }, { once: true });
      return;
    }
    if (upgradeShown) return;
    upgradeShown = true;
    const bar = el("div", "update-bar");
    bar.setAttribute("role", "status");
    bar.appendChild(el("span", "", "Goliash was updated."));
    const btn = el("button", "", "Reload");
    btn.type = "button";
    btn.addEventListener("click", function () { location.reload(); });
    bar.appendChild(btn);
    document.body.appendChild(bar);
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
  // matchWords: every word must appear in hay, except kinds (type:lambda), of which one is enough:
  // "type:ecs type:docker" is what runs on ECS or Docker.
  // A word starting with a dash leaves out what has it: "-pr-" hides the preview copies.
  function matchWords(words, hay) {
    const kinds = words.filter(function (w) { return w.indexOf("type:") === 0; });
    return words.every(function (w) {
      if (w.indexOf("type:") === 0) return true;
      if (w.length > 1 && w[0] === "-") return hay.indexOf(w.slice(1)) < 0;
      return hay.indexOf(w) >= 0;
    }) && (kinds.length === 0 || kinds.some(function (k) { return hay.indexOf(k) >= 0; }));
  }

  // Rows with the same data-same (one upgrade of one image in one environment, say every Node.js function
  // 22 -> 24) fold into the first one shown, with "+N more like this" to unfold them.
  const sameOpen = new Set();
  function foldSame() {
    document.querySelectorAll("[data-same][data-same-hid]").forEach(function (el) { el.hidden = false; el.removeAttribute("data-same-hid"); });
    const groups = {};
    document.querySelectorAll("[data-same]").forEach(function (el) {
      if (el.hidden || el.classList.contains("filtered")) return;
      const k = el.getAttribute("data-same");
      (groups[k] = groups[k] || []).push(el);
    });
    document.querySelectorAll("[data-same-more]").forEach(function (b) { b.hidden = true; });
    Object.keys(groups).forEach(function (k) {
      const rows = groups[k];
      const more = rows[0].querySelector("[data-same-more]");
      if (rows.length < 2 || !more) return;
      more.hidden = false;
      more.setAttribute("data-same-key", k);
      if (sameOpen.has(k)) { more.textContent = "fold the " + (rows.length - 1) + " like this"; return; }
      more.textContent = "+" + (rows.length - 1) + " more like this";
      rows.slice(1).forEach(function (el) { el.hidden = true; el.setAttribute("data-same-hid", ""); });
    });
  }
  document.addEventListener("click", function (e) {
    const b = e.target.closest && e.target.closest("[data-same-more]");
    if (!b) return;
    const k = b.getAttribute("data-same-key");
    if (sameOpen.has(k)) sameOpen.delete(k); else sameOpen.add(k);
    foldSame();
  });
  document.body.addEventListener("htmx:afterSwap", foldSame);

  // "Tick all": ticks every box of its form, or unticks them when all are.
  document.addEventListener("click", function (e) {
    const b = e.target.closest && e.target.closest("[data-tick-all]");
    if (!b || !b.form) return;
    const boxes = Array.from(b.form.querySelectorAll('input[type="checkbox"][name="id"]'));
    const all = boxes.length > 0 && boxes.every(function (c) { return c.checked; });
    boxes.forEach(function (c) { c.checked = !all; });
  });

  function filterMatrix() {
    const q = document.getElementById("matrix-q");
    const drift = document.getElementById("matrix-drift");
    if (!q || !drift) return;
    const words = q.value.toLowerCase().split(/\s+/).filter(Boolean);
    const rows = document.querySelectorAll("table.matrix tbody tr[data-search]");
    let shown = 0;
    rows.forEach(function (tr) {
      const hay = tr.getAttribute("data-search");
      const ok = matchWords(words, hay) && (!drift.checked || tr.getAttribute("data-drift") === "1");
      tr.classList.toggle("filtered", !ok);
      if (ok) shown++;
    });
    document.querySelectorAll("table.matrix tbody.mgroup").forEach(function (g) {
      g.classList.toggle("filtered", !g.querySelector("tr[data-search]:not(.filtered)"));
    });
    foldSame();
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

  // Kind buttons (Kubernetes, Lambda, RDS …) add or remove type:<kind> in their filter box, so the kind
  // filter is part of the words: shared in ?q= like them, and combined with them.
  function syncKindChips() {
    document.querySelectorAll("[data-kind-token]").forEach(function (b) {
      const input = document.getElementById(b.getAttribute("data-kind-input"));
      const words = input ? input.value.toLowerCase().split(/\s+/) : [];
      b.setAttribute("aria-pressed", words.indexOf(b.getAttribute("data-kind-token")) >= 0 ? "true" : "false");
    });
  }
  document.addEventListener("click", function (e) {
    const b = e.target.closest("[data-kind-token]");
    if (!b) return;
    const input = document.getElementById(b.getAttribute("data-kind-input"));
    if (!input) return;
    const token = b.getAttribute("data-kind-token");
    let words = input.value.split(/\s+/).filter(Boolean);
    words = words.indexOf(token) >= 0 ? words.filter(function (w) { return w !== token; }) : words.concat(token);
    input.value = words.join(" ");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  document.addEventListener("input", syncKindChips);

  document.body.addEventListener("htmx:afterSwap", function (e) { refreshTimes(e.target); filterMatrix(); syncKindChips(); });
  document.addEventListener("input", function (e) {
    if (e.target.id === "matrix-q") filterMatrix();
    if (e.target.matches("input[data-filter]")) filterCards(e.target);
  });

  // A filter box for cards (data-filter names their selector): words must all appear
  // in a card's data-search; groups without a visible card hide too.
  // Without data-search an element matches on its text (a table row, say). data-filter-dim steps the others back
  // instead of hiding them (the tiles keep their places), data-filter-count names where "3 of 24" goes, and
  // data-filter-url keeps the words in ?q=, so a filtered page can be shared and survives its live refreshes.
  function filterCards(input) {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    const dim = input.hasAttribute("data-filter-dim");
    let shown = 0, total = 0;
    document.querySelectorAll(input.getAttribute("data-filter")).forEach(function (el) {
      const hay = (el.getAttribute("data-search") || el.textContent).toLowerCase();
      const ok = matchWords(words, hay);
      total++;
      if (ok) shown++;
      el.removeAttribute("data-same-hid");
      if (dim) el.classList.toggle("filter-out", !ok); else el.hidden = !ok;
    });
    foldSame();
    document.querySelectorAll("[data-group]").forEach(function (g) {
      g.hidden = !g.querySelector("[data-search]:not([hidden])");
    });
    if (dim) cellsReach();
    bulkCount();
    const count = input.getAttribute("data-filter-count") && document.getElementById(input.getAttribute("data-filter-count"));
    if (count) count.textContent = words.length && total ? shown + " of " + total : "";
    if (input.hasAttribute("data-filter-url")) {
      const url = new URL(window.location.href);
      if (input.value) url.searchParams.set("q", input.value); else url.searchParams.delete("q");
      if (url.href !== window.location.href) window.history.replaceState(window.history.state, "", url);
    }
    // Links marked data-filter-q (a CSV download, say) carry the words too, to give what the page shows.
    document.querySelectorAll("a[data-filter-q]").forEach(function (a) {
      const href = new URL(a.href);
      if (input.value) href.searchParams.set("q", input.value); else href.searchParams.delete("q");
      a.href = href.pathname + href.search;
    });
  }
  // Tiles: the arrow keys move between the cells of a board; up and down go to the nearest cell in the row above or
  // below, by where it is drawn (big cells span two rows). Cells out of reach (dimmed by a filter) are skipped.
  document.addEventListener("keydown", function (e) {
    const cell = e.target.closest && e.target.closest(".logo-board .board-cell");
    if (!cell || e.altKey || e.ctrlKey || e.metaKey || ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].indexOf(e.key) < 0) return;
    const cells = Array.from(cell.closest(".logo-board").querySelectorAll(".board-cell")).filter(function (c) { return !c.inert; });
    const here = cell.getBoundingClientRect();
    let next = null;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      next = cells[cells.indexOf(cell) + (e.key === "ArrowRight" ? 1 : -1)];
    } else {
      const down = e.key === "ArrowDown", cx = here.left + here.width / 2;
      let best = Infinity;
      cells.forEach(function (c) {
        if (c === cell) return;
        const r = c.getBoundingClientRect();
        const dy = down ? r.top - here.bottom : here.top - r.bottom;
        if (dy < -2) return; // not below (above)
        const score = dy * 4 + Math.abs(r.left + r.width / 2 - cx);
        if (score < best) { best = score; next = c; }
      });
    }
    if (next) { e.preventDefault(); next.focus(); }
  });
  // A cell either filter steps back (the legend's state, the filter box's words) is out of reach too, as a hidden
  // row is: Tab skips it and screen readers leave it out.
  function cellsReach() {
    const only = document.body.dataset.tileOnly;
    document.querySelectorAll(".logo-board .board-cell").forEach(function (c) {
      c.inert = c.classList.contains("filter-out") || Boolean(only && !c.classList.contains("s-" + only));
    });
  }
  // Bulk pull requests (Updates): "Tick all shown" ticks the upgrades the filter shows (again: unticks them), the
  // button counts what goes, and ticked rows a filter hides stay out of the form.
  function bulkShown(form) {
    return Array.from(document.querySelectorAll('input[type="checkbox"][form="' + form + '"]')).filter(function (c) {
      const row = c.closest("tr");
      return !row || shownRow(row);
    });
  }
  // shownRow: a row the filter keeps; one folded under "+N more like this" counts, it is only out of sight.
  function shownRow(r) { return (!r.hidden && !r.classList.contains("filtered")) || r.hasAttribute("data-same-hid"); }
  // The matrix's ticks: kept by service across live refreshes, which redraw the rows; the bar shows while any is ticked.
  const matrixTicks = new Set();
  function matrixBulk() {
    const bar = document.getElementById("matrix-bulk");
    if (!bar) return;
    document.querySelectorAll('input.row-tick[form="matrix-bulk"]').forEach(function (c) { c.checked = matrixTicks.has(c.value); });
    bar.hidden = matrixTicks.size === 0;
    bar.querySelector("[data-bulk-count]").textContent = matrixTicks.size + " ticked";
    bar.querySelector("[data-bulk-return]").value = "/" + window.location.search;
  }
  document.addEventListener("change", function (e) {
    if (!e.target.matches || !e.target.matches('input.row-tick[form="matrix-bulk"]')) return;
    if (e.target.checked) matrixTicks.add(e.target.value); else matrixTicks.delete(e.target.value);
    matrixBulk();
  });
  document.body.addEventListener("htmx:afterSwap", matrixBulk);
  // Sent: the ticks have done their job (the page that comes back starts with none).
  document.addEventListener("submit", function (e) { if (e.target.id === "matrix-bulk") setTimeout(function () { matrixTicks.clear(); matrixBulk(); }, 0); });

  function bulkCount() {
    if (document.getElementById("matrix-bulk")) {
      matrixTicks.clear();
      document.querySelectorAll('input.row-tick[form="matrix-bulk"]:checked').forEach(function (c) { matrixTicks.add(c.value); });
      matrixBulk();
    }
    const form = document.getElementById("bulk-prs");
    if (!form) return;
    const n = bulkShown("bulk-prs").filter(function (c) { return c.checked; }).length;
    const count = form.querySelector("[data-bulk-count]");
    if (count) count.textContent = n ? " (" + n + ")" : "";
    const limit = form.querySelector("[data-bulk-limit]");
    if (limit) limit.hidden = n <= 20;
  }
  document.addEventListener("click", function (e) {
    const b = e.target.closest && e.target.closest("[data-tick-shown]");
    if (!b) return;
    const shown = bulkShown(b.getAttribute("data-tick-shown"));
    const all = shown.length > 0 && shown.every(function (c) { return c.checked; });
    shown.forEach(function (c) { c.checked = !all; });
    bulkCount();
  });
  document.addEventListener("change", function (e) { if (e.target.matches && e.target.matches('input[form="bulk-prs"]')) bulkCount(); });
  document.addEventListener("submit", function (e) {
    if (e.target.id !== "bulk-prs") return;
    document.querySelectorAll('input[form="bulk-prs"][name="pr"]:checked').forEach(function (c) {
      const row = c.closest("tr");
      if (row && !shownRow(row)) c.checked = false; // not shown: not opened
    });
  }, true);
  // Filters with words in them apply again to what a live refresh brought.
  function refilter() {
    document.querySelectorAll("input[data-filter]").forEach(function (i) { if (i.value) filterCards(i); });
  }
  document.body.addEventListener("htmx:afterSwap", refilter);
  // Opening a card's edit form puts the cursor in its first field, its text selected.
  document.addEventListener("toggle", function (e) {
    if (!e.target.matches || !e.target.matches("details.card-edit") || !e.target.open) return;
    const f = e.target.querySelector("[data-focus-open]");
    if (f) { f.focus(); if (f.select) f.select(); }
  }, true);
  document.addEventListener("change", function (e) {
    if (e.target.id === "matrix-drift") filterMatrix();
    // "Select all shown": ticks (or unticks) the checkboxes its selector finds, e.g. those a filter left visible.
    const sel = e.target.getAttribute && e.target.getAttribute("data-check-shown");
    if (sel) document.querySelectorAll(sel).forEach(function (c) { c.checked = e.target.checked; });
  });
  // Saving a view keeps the address as it is now, the matrix's filters typed in this page included.
  document.addEventListener("submit", function (e) {
    if (!e.target.matches("form[data-save-view]")) return;
    const url = e.target.querySelector("input[name=url]");
    if (url) url.value = window.location.pathname + window.location.search;
  });
  // Opening "Save view" puts the cursor in its name.
  document.addEventListener("toggle", function (e) {
    if (!e.target.matches || !e.target.matches("details.view-save") || !e.target.open) return;
    const f = e.target.querySelector("[data-focus-open]");
    if (f) f.focus();
  }, true);
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
    const q = document.getElementById("matrix-q") || document.querySelector("input[data-filter]");
    const typing = e.target.closest && e.target.closest("input, textarea, select, [contenteditable]");
    // "/" jumps to the page's filter, as on GitHub; Escape in the filter clears it.
    if (q && e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); q.focus(); q.select(); return; }
    if (e.key !== "Escape") return;
    if (q && e.target === q && q.value) { q.value = ""; if (q.id === "matrix-q") filterMatrix(); else filterCards(q); return; }
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
    // data-copy-rows: the lines of the rows shown (a filter may hide some), else the element's text.
    const rows = btn.getAttribute("data-copy-rows");
    const text = rows ? Array.from(document.querySelectorAll(rows)).filter(shownRow)
      .map(function (r) { return r.getAttribute("data-md"); }).join("") : el.textContent;
    navigator.clipboard.writeText(text).then(
      function () { btn.textContent = "Copied"; },
      function () { window.getSelection().selectAllChildren(el); }
    );
  });

  // Download buttons: save an element's text as a file (the widget's script, with its token, never leaves the page).
  document.addEventListener("click", function (e) {
    const btn = e.target.closest("[data-download]");
    if (!btn) return;
    const src = document.getElementById(btn.getAttribute("data-download"));
    const url = URL.createObjectURL(new Blob([src.textContent], { type: "text/javascript" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = btn.getAttribute("data-filename") || "goliash.txt";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
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
    left = Math.max(fig.scrollLeft + 4, left); // a narrow chart has room on neither side: keep it inside
    let top = e.clientY - fr.top - tip.offsetHeight / 2;
    top = Math.max(4, Math.min(top, fig.clientHeight - tip.offsetHeight - 4));
    tip.style.left = left + "px";
    tip.style.top = top + "px";
  }
  function pointChart(e) {
    const fig = e.target.closest && e.target.closest("figure[data-chart]");
    if (fig && e.target.closest("svg")) hoverChart(fig, e);
    else leaveChart();
  }
  document.addEventListener("pointermove", pointChart);
  // A finger does not hover: a tap on a chart shows that day, a tap elsewhere hides it.
  document.addEventListener("pointerdown", function (e) { if (e.pointerType !== "mouse") pointChart(e); });
  // A finger leaves the page as it lifts: only a mouse leaving hides the tooltip.
  document.addEventListener("pointerleave", function (e) { if (e.pointerType === "mouse") leaveChart(); });

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
  // Where q's letters fall in text, for highlighting: a run of the whole q first, else the fuzzy letters.
  function fuzzyAt(q, text) {
    text = text.toLowerCase();
    const run = text.indexOf(q);
    if (run >= 0) return Array.from({ length: q.length }, function (_, k) { return run + k; });
    const at = [];
    let ti = 0;
    for (const ch of q) { const i = text.indexOf(ch, ti); if (i < 0) return []; at.push(i); ti = i + 1; }
    return at;
  }
  // The label as nodes, the letters that matched in <mark>.
  function highlighted(label, q) {
    const span = el("span", "palette-label");
    const at = q ? fuzzyAt(q, label) : [];
    if (!at.length) { span.textContent = label; return span; }
    let last = 0;
    at.forEach(function (i) {
      if (i > last) span.appendChild(document.createTextNode(label.slice(last, i)));
      span.appendChild(el("mark", "", label[i]));
      last = i + 1;
    });
    if (last < label.length) span.appendChild(document.createTextNode(label.slice(last)));
    return span;
  }
  // A word of the entry's keys holding q, when its name does not match: what found it.
  function keyHit(q, e) {
    if (!e.keys) return "";
    for (const k of e.keys) if (k.indexOf(q) >= 0) return k;
    return "";
  }
  // Ways to look further than the palette's places, from what was typed.
  function paletteSearches(raw) {
    const out = [];
    const id = raw.toUpperCase();
    if (/^(CVE-\d{4}-\d+|GHSA(-[0-9A-Z]{4}){3}|GO-\d{4}-\d+)$/.test(id)) {
      out.push({ label: "Where does " + id + " run?", sub: "vulnerabilities in running images", kind: "search", url: "/security/vulns?q=" + encodeURIComponent(id), top: true });
    } else if (raw.length >= 3) {
      out.push({ label: "Vulnerabilities matching “" + raw + "”", sub: "CVE, package or service", kind: "search", url: "/security/vulns?q=" + encodeURIComponent(raw) });
      out.push({ label: "Packages named “" + raw + "”", sub: "in running images, with their licenses", kind: "search", url: "/security/licenses?q=" + encodeURIComponent(raw) });
    }
    if (raw.length >= 2) {
      out.push({ label: "Search everywhere for “" + raw + "”", sub: "services, vulnerabilities, packages, pull requests, history, notifications", kind: "search", url: "/search?q=" + encodeURIComponent(raw) });
    }
    return out;
  }
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
      items = items.map(function (e) {
        let s = Math.max(fuzzy(q, e.label), fuzzy(q, e.label + " " + (e.sub || "")) - 2);
        let hit = "";
        if (s < 0 && q.length >= 2) { // an image, version, workload or GitOps application it runs
          hit = keyHit(q, e);
          if (hit) s = 1 + q.length / hit.length;
        }
        return { e: hit ? Object.assign({}, e, { hit: hit }) : e, s: s < 0 ? s : s + (recent.indexOf(e.url) >= 0 ? 1 : 0) };
      })
        .filter(function (x) { return x.s >= 0; })
        .sort(function (a, b) { return b.s - a.s; })
        .map(function (x) { return x.e; });
    }
    items = items.slice(0, 50);
    if (q && paletteData) {
      paletteSearches(palette.input.value.trim()).forEach(function (sr) { if (sr.top) items.unshift(sr); else items.push(sr); });
    }
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
      text.appendChild(e.kind === "search" || e.kind === "goli" ? el("span", "palette-label", e.label) : highlighted(e.label, q));
      if (e.hit) text.appendChild(el("span", "palette-sub palette-hit", "via " + e.hit));
      else if (e.sub) text.appendChild(el("span", "palette-sub", e.sub));
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
    delivery: "delivery", promotions: "promotions", release_notes: "release notes", posture: "the security posture", rollouts: "rollouts",
    images: "images", notifications: "notifications", licenses: "licenses"
  };
  const goliIdeas = [
    "What should we upgrade first, and why?",
    "Which running images have vulnerabilities exploited in the wild?",
    "What drifted between staging and production this week?",
    "Is any of the data stale right now?"
  ];
  // Questions that fit the page Goli is opened on; the general ones elsewhere.
  function goliPageIdeas() {
    const path = location.pathname, params = new URLSearchParams(location.search);
    const svc = /^\/services\/([^/]+)$/.exec(path);
    if (svc) {
      const name = decodeURIComponent(svc[1]);
      return [
        "What runs where for " + name + ", and is anything behind?",
        "What changed in " + name + " this week?",
        "Brief me on upgrading " + name + " to its newest release.",
        "Does " + name + " have known vulnerabilities in production?",
        "Why did the " + name + " image grow, and is its base current?"
      ];
    }
    if (params.get("at")) {
      return [
        "What ran in production at " + params.get("at").replace("T", " ") + "?",
        "What changed in production in the hour before that?",
        "What drift was open at that moment?"
      ];
    }
    if (path.indexOf("/security") === 0) {
      return [
        "Which vulnerabilities exploited in the wild run in production, and what fixes them?",
        "How long has production run behind an available fix?",
        "What runs past its end of life?",
        "Which images have no SBOM, so we cannot see inside them?"
      ];
    }
    if (path === "/updates") {
      return [
        "What should we upgrade first, and why?",
        "Which upgrades are major jumps that need care?",
        "Which updates fix security problems?",
        "What is already in staging and waiting for production?"
      ];
    }
    if (path === "/delivery" || path === "/promotions" || path === "/events") {
      return [
        "What was deployed to production this week?",
        "What waits in staging to be promoted?",
        "What changed in the last two hours?"
      ];
    }
    return goliIdeas;
  }
  function goliRender() {
    const log = goli.log;
    log.replaceChildren();
    const h = goliHistory();
    if (!h.length && !goli.busy) {
      const empty = el("div", "goli-empty");
      const art = goliArt("goli-big");
      if (art) empty.appendChild(art);
      empty.appendChild(el("p", "goli-hello", "Hi, I am Goli. Ask me about what runs where."));
      const ideas = el("div", "goli-ideas");
      goliPageIdeas().forEach(function (q) {
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
      if (t.role !== "user" && t.text) {
        // The answer as Goli wrote it, Markdown and links, for a ticket or a chat.
        const copy = el("button", "goli-copy link", "Copy");
        copy.type = "button";
        copy.addEventListener("click", function () {
          const text = t.text.replace(/\]\(\//g, "](" + location.origin + "/");
          navigator.clipboard.writeText(text).then(function () { copy.textContent = "Copied"; }, function () { copy.textContent = "Copy failed"; });
        });
        m.appendChild(copy);
      }
      if (Array.isArray(t.looked)) m.appendChild(el("p", "goli-looking", "Looked at " + t.looked.join(", ")));
      if (t.error) m.appendChild(el("p", "goli-error", t.error));
      log.appendChild(m);
    });
    if (goli.busy) {
      const m = el("div", "goli-msg goli-a");
      if (goli.answer) m.appendChild(goliMarkdown(goli.answer));
      if (goli.looking.length) m.appendChild(el("p", "goli-looking", "Looked at " + goli.looking.join(", ") + (goli.answer ? "" : "…")));
      else if (!goli.answer) {
        const think = el("p", "goli-looking goli-thinking");
        const art = goliArt("goli-face");
        if (art) think.appendChild(art);
        think.appendChild(document.createTextNode("Thinking…"));
        m.appendChild(think);
      }
      log.appendChild(m);
    }
    if (window.htmx) htmx.process(log);
    log.scrollTop = log.scrollHeight;
    goli.send.textContent = goli.busy ? "Stop" : "Ask";
    goli.clear.disabled = goli.busy || !h.length;
    const corner = goli.panel.querySelector(".goli-corner");
    if (corner) corner.textContent = goliCornerOn() ? "Hide Goli from the corner" : "Show Goli in the corner";
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
    document.documentElement.classList.add("goli-busy");
    goli.abort = new AbortController();
    goliRender();
    const turns = h.filter(function (t) { return t.text; }).map(function (t) { return { role: t.role, text: t.text }; });
    function finish() {
      const done = goliHistory();
      done.push({ role: "assistant", text: goli.answer, error: goli.error || undefined, looked: goli.looking.length ? goli.looking : undefined });
      goliSave(done);
      goli.busy = false; goli.abort = null;
      document.documentElement.classList.remove("goli-busy");
      // One ding: the answer is there.
      document.documentElement.classList.add("goli-done");
      setTimeout(function () { document.documentElement.classList.remove("goli-done"); }, 1400);
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
      const face = goliArt("goli-face");
      if (face) head.appendChild(face);
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
      const viaEl = document.querySelector("[data-goli-via]");
      const via = (viaEl && viaEl.getAttribute("data-goli-via")) || "its model";
      const note = el("p", "goli-note", "Goli reads what you may see here and sends it with your question to " + via + ". It changes nothing. Check what matters. ");
      const corner = el("button", "goli-corner link");
      corner.type = "button";
      corner.addEventListener("click", function () { goliCorner(!goliCornerOn()); });
      note.appendChild(corner);
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
  // Goli in the page's corner; it can be sent away, per browser, and the header button stays.
  const goliCornerKey = "goliash.goli.corner";
  function goliCornerOn() { try { return localStorage.getItem(goliCornerKey) !== "off"; } catch (_) { return true; } }
  function goliCorner(on) {
    try { if (on) localStorage.removeItem(goliCornerKey); else localStorage.setItem(goliCornerKey, "off"); } catch (_) { /* storage off */ }
    document.documentElement.classList.toggle("goli-nocorner", !on);
    if (goli) goliRender();
  }
  document.documentElement.classList.toggle("goli-nocorner", !goliCornerOn());
  // Goli's eyes follow the pointer, a little: CSS moves the pupils by --goli-x and --goli-y.
  let goliEyeFrame = 0;
  document.addEventListener("pointermove", function (e) {
    if (goliEyeFrame || e.pointerType === "touch") return;
    goliEyeFrame = requestAnimationFrame(function () {
      goliEyeFrame = 0;
      document.querySelectorAll(".goli-art").forEach(function (art) {
        const r = art.getBoundingClientRect();
        if (!r.width) return;
        const dx = e.clientX - (r.left + r.width / 2), dy = e.clientY - (r.top + r.height * 0.42);
        const d = Math.hypot(dx, dy) || 1, k = Math.min(1, d / 300) * 9 / d; // up to 9 units of 404
        art.style.setProperty("--goli-x", (dx * k).toFixed(1) + "px");
        art.style.setProperty("--goli-y", (dy * k).toFixed(1) + "px");
      });
    });
  }, { passive: true });
  // A copy of the mascot the page carries (in the corner or the header button).
  function goliArt(cls) {
    const src = document.querySelector("[data-goli] .goli-art");
    if (!src) return null;
    const art = src.cloneNode(true);
    art.setAttribute("class", "goli-art " + cls);
    return art;
  }
  // A link from an alert (?goli=question) opens Goli with the question ready; asking stays a click away, so a
  // link opened by a preview bot or by mistake costs nothing.
  function goliFromLink() {
    const params = new URLSearchParams(location.search);
    const q = params.get("goli");
    if (q === null) return;
    params.delete("goli");
    const rest = params.toString();
    history.replaceState(history.state, "", location.pathname + (rest ? "?" + rest : "") + location.hash);
    if (!document.querySelector("[data-goli]")) return;
    goliOpen();
    goli.input.value = q.slice(0, 2000);
    goli.input.focus();
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
  // Tiles: a legend item picks out the cells in its state; again, or Escape, shows all. The choice is kept on the
  // body, so the board's live refresh keeps it.
  function tileOnly(state) {
    if (state) document.body.dataset.tileOnly = state; else delete document.body.dataset.tileOnly;
    document.querySelectorAll("[data-tile-only]").forEach(function (b) {
      b.setAttribute("aria-pressed", String(b.dataset.tileOnly === document.body.dataset.tileOnly));
    });
    cellsReach();
  }
  document.addEventListener("click", function (e) {
    const b = e.target.closest && e.target.closest("button[data-tile-only]");
    if (b) tileOnly(document.body.dataset.tileOnly === b.dataset.tileOnly ? "" : b.dataset.tileOnly);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && document.body.dataset.tileOnly && !document.querySelector(".tile-panel:target")) tileOnly("");
  });
  document.body.addEventListener("htmx:afterSwap", function (e) {
    tileOnly(e.detail && e.detail.boosted ? "" : document.body.dataset.tileOnly || ""); // a new page starts with all
  });
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
    if (!goli.busy && !goliHistory().length) goliRender(); // suggestions for the new page
  }
  document.body.addEventListener("htmx:afterSwap", goliKeep);
  document.body.addEventListener("htmx:afterSettle", goliKeep);

  // The bell: what Goliash sent in this workspace, newest first, and how many the person has not seen. The
  // count is on the bell and, in the installed app, on its icon. Built from DOM nodes: texts are anyone's input.
  let bellPanel = null;
  function bellSet(n) {
    const c = document.querySelector("[data-unread]");
    if (c) {
      c.textContent = n > 98 ? "99+" : String(n);
      c.hidden = n === 0;
    }
    const b = document.querySelector("[data-bell]");
    if (b) b.setAttribute("aria-label", n ? "Notifications, " + n + " unread" : "Notifications");
    // Goli's swamped outfit follows the live count ("Always something!").
    const at = parseInt(document.documentElement.getAttribute("data-swamped-at") || "", 10);
    if (!isNaN(at)) document.documentElement.classList.toggle("goli-swamped", n >= at);
    try {
      if (n > 0 && navigator.setAppBadge) navigator.setAppBadge(n).catch(function () { /* not installed */ });
      else if (navigator.clearAppBadge) navigator.clearAppBadge().catch(function () { /* not installed */ });
    } catch (_) { /* no badging */ }
  }
  let bellTimer = 0;
  function bellRefresh() {
    clearTimeout(bellTimer);
    bellTimer = setTimeout(function () {
      if (!document.querySelector("[data-bell]")) return;
      fetch("/ui/notifications/unread", { credentials: "same-origin" }).then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) { if (d) bellSet(d.unread); }).catch(function () { /* offline */ });
    }, 800);
  }
  // A menu opens under its summary, aligned right; near the left edge of a phone that
  // would start it off the screen. Opened, it is nudged back inside.
  document.addEventListener("toggle", function (e) {
    const d = e.target;
    if (!(d instanceof HTMLDetailsElement) || !d.classList.contains("menu") || !d.open) return;
    const list = d.querySelector(":scope > .menu-list");
    if (!list) return;
    list.style.transform = "";
    const r = list.getBoundingClientRect(), vw = document.documentElement.clientWidth;
    let dx = 0;
    if (r.left < 8) dx = 8 - r.left;
    else if (r.right > vw - 8) dx = vw - 8 - r.right;
    if (dx) list.style.transform = "translateX(" + Math.round(dx) + "px)";
  }, true);
  // A panel's explanation closes on a click elsewhere or Escape, as a tooltip would.
  document.addEventListener("click", function (e) {
    document.querySelectorAll("details.panel-info[open]").forEach(function (d) { if (!d.contains(e.target)) d.open = false; });
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") document.querySelectorAll("details.panel-info[open]").forEach(function (d) { d.open = false; });
  });
  function bellClose() {
    if (!bellPanel || !bellPanel.isConnected) return;
    bellPanel.remove();
    const b = document.querySelector("[data-bell]");
    if (b) { b.setAttribute("aria-expanded", "false"); b.focus(); }
  }
  function bellOpen(btn) {
    if (bellPanel && bellPanel.isConnected) { bellClose(); return; }
    bellPanel = el("div", "bell-panel");
    bellPanel.setAttribute("role", "dialog");
    bellPanel.setAttribute("aria-label", "Notifications");
    const head = el("div", "bell-head");
    head.appendChild(el("strong", null, "Notifications"));
    const tools = el("span", "bell-tools");
    const all = el("button", "link bell-all", "Mark all read");
    all.type = "button";
    all.hidden = true;
    all.addEventListener("click", function () {
      fetch("/ui/notifications/seen", { method: "POST", credentials: "same-origin" }).then(function () {
        bellSet(0);
        bellPanel.querySelectorAll(".bell-item.unread").forEach(function (li) { li.classList.remove("unread"); });
        all.hidden = true;
      });
    });
    tools.appendChild(all);
    const settings = el("a", null, "Settings");
    settings.href = "/notifications";
    tools.appendChild(settings);
    head.appendChild(tools);
    const list = el("ul", "bell-list");
    list.appendChild(el("li", "bell-empty", "Loading…"));
    bellPanel.appendChild(head);
    bellPanel.appendChild(list);
    // Under the bell, its right edge with the bell's, but always on the screen: on a
    // phone the bell sits mid-bar and a panel that wide would start off its left edge.
    const r = btn.getBoundingClientRect();
    const vw = document.documentElement.clientWidth, vh = window.innerHeight;
    const width = Math.min(400, vw - 16);
    const top = r.bottom + 8;
    bellPanel.style.top = top + "px";
    bellPanel.style.width = width + "px";
    bellPanel.style.right = Math.min(Math.max(8, vw - r.right - 8), vw - width - 8) + "px";
    bellPanel.style.maxHeight = Math.min(560, vh - top - 8) + "px";
    document.body.appendChild(bellPanel);
    btn.setAttribute("aria-expanded", "true");
    if (window.htmx) htmx.process(bellPanel);
    fetch("/ui/notifications", { credentials: "same-origin" }).then(function (r) { return r.ok ? r.json() : { items: [] }; }).then(function (d) {
      list.replaceChildren();
      // This device: notifications pushed to it, turned on (or off) here rather than in the settings.
      if (d.push && pushSupported()) {
        const foot = el("div", "bell-device");
        foot.setAttribute("data-push-scope", "");
        const on = el("button", "link", "Notify me on this device");
        on.type = "button";
        on.setAttribute("data-push", "subscribe");
        on.setAttribute("data-channel", d.push.channel);
        on.hidden = true;
        const off = el("button", "link", "Stop on this device");
        off.type = "button";
        off.setAttribute("data-push", "unsubscribe");
        off.setAttribute("data-channel", d.push.channel);
        off.hidden = true;
        const state = el("span", "muted bell-device-state");
        const note = el("div", "push-note");
        note.setAttribute("data-push-key", d.push.key);
        note.hidden = true;
        foot.append(state, on, off, note);
        bellPanel.appendChild(foot);
        pushSetup();
      }
      if (!d.items.length) {
        const empty = el("li", "bell-empty");
        empty.appendChild(el("p", null, "Nothing yet."));
        const p = el("p", "muted", "Notifications show up here when a rule sends something. Add a rule to the channel In Goliash to get them here only. ");
        const a = el("a", null, "Set up notifications");
        a.href = "/notifications";
        p.appendChild(a);
        empty.appendChild(p);
        list.appendChild(empty);
      }
      d.items.forEach(function (it) {
        const li = el("li", "bell-item" + (it.unread ? " unread" : ""));
        const a = el("a", "bell-link");
        a.href = it.url;
        a.appendChild(el("span", "bell-mark kind-" + it.kind));
        const body = el("span", "bell-body");
        body.appendChild(el("span", "bell-title", it.title));
        (it.lines || []).forEach(function (l) { body.appendChild(el("span", "bell-line", l)); });
        if (it.more) body.appendChild(el("span", "bell-line", "and " + it.more + " more"));
        const t = el("time", "bell-time");
        t.setAttribute("datetime", it.at);
        t.setAttribute("data-ago", "");
        t.textContent = new Date(it.at).toLocaleString();
        body.appendChild(t);
        a.appendChild(body);
        // Opening one reads that one only, so nothing else slips by unseen.
        const read = function () {
          if (!li.classList.contains("unread")) return;
          li.classList.remove("unread");
          fetch("/ui/notifications/read", {
            method: "POST", credentials: "same-origin", keepalive: true,
            headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ id: it.id }),
          }).then(function (r) { return r.ok ? r.json() : null; }).then(function (d) {
            if (d) bellSet(d.unread);
            if (d && !d.unread) all.hidden = true;
          }).catch(function () { /* the next refresh corrects the count */ });
        };
        a.addEventListener("click", function () { read(); bellClose(); });
        li.appendChild(a);
        if (it.ask && document.querySelector("[data-goli]")) {
          // Ready to ask, not asked: the question waits in Goli's box for a click.
          const ask = el("button", "bell-ask", "Ask Goli");
          ask.type = "button";
          ask.addEventListener("click", function () {
            read();
            bellClose();
            goliOpen();
            goli.input.value = it.ask;
            goli.input.focus();
          });
          li.appendChild(ask);
          li.classList.add("has-ask");
        }
        list.appendChild(li);
      });
      refreshTimes(list);
      if (window.htmx) htmx.process(list);
      all.hidden = !(d.unread > 0);
      bellSet(d.unread || 0);
    }).catch(function () {
      list.replaceChildren(el("li", "bell-empty", "Notifications could not be loaded."));
    });
  }
  document.addEventListener("click", function (e) {
    const btn = e.target.closest && e.target.closest("[data-bell]");
    if (btn) { e.preventDefault(); bellOpen(btn); return; }
    if (bellPanel && bellPanel.isConnected && !bellPanel.contains(e.target)) bellClose();
  });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape" && bellPanel && bellPanel.isConnected) { e.stopPropagation(); bellClose(); } }, true);
  document.body.addEventListener("htmx:beforeSwap", function (e) { if (e.detail.target === document.body && bellPanel) bellPanel.remove(); });
  document.body.addEventListener("goliash:changed", bellRefresh);
  setInterval(function () { if (!document.hidden) bellRefresh(); }, 120000);
  document.addEventListener("visibilitychange", function () { if (!document.hidden) bellRefresh(); });
  // The installed app's icon follows the count the page came with.
  function bellInit() {
    const c = document.querySelector("[data-unread]");
    if (c) bellSet(c.hidden ? 0 : parseInt(c.textContent, 10) || 0);
  }

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
      if (document.querySelector("[data-goli]")) rows.splice(1, 0, [["."], "Ask Goli, or close its panel"]);
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
    if (e.key === "." && document.querySelector("[data-goli]")) { e.preventDefault(); shortcutsClose(); if (goli && goli.open) goliClose(); else goliOpen(); return; }
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
  function pushNote(text, bad, scope) {
    const note = (scope && scope.querySelector("[data-push-key]")) || document.querySelector("[data-push-key]");
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
    document.querySelectorAll(".bell-device").forEach(function (f) {
      const off = f.querySelector('[data-push="unsubscribe"]');
      f.querySelector(".bell-device-state").textContent = off && !off.hidden ? "On on this device." : "";
    });
  }
  async function pushToggle(b) {
    const channel = b.getAttribute("data-channel");
    const scope = b.closest("[data-push-scope]");
    const key = ((scope && scope.querySelector("[data-push-key]")) || document.querySelector("[data-push-key]")).getAttribute("data-push-key");
    const note = function (text, bad) { pushNote(text, bad, scope); };
    b.disabled = true;
    note("Asking this browser…", false);
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
        note((await pushPost("/notifications/channels/" + channel + "/push", sub.toJSON())).notice, false);
      } else if (sub) {
        note((await pushPost("/notifications/channels/" + channel + "/push/delete", { endpoint: sub.endpoint })).notice, false);
      }
    } catch (err) {
      note("This browser could not subscribe: " + (err.message || String(err)), true);
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

  // The page loader: only for navigations and forms (boosted requests), and only when they take a moment, so a
  // fast page does not flash it; live refreshes never show it.
  let loaderTimer = null;
  function loaderOn() {
    if (loaderTimer) return;
    loaderTimer = setTimeout(function () {
      const l = document.querySelector("[data-loader]");
      if (!l) return;
      l.hidden = false;
      requestAnimationFrame(function () { l.classList.add("on"); });
    }, 250);
  }
  function loaderOff() {
    clearTimeout(loaderTimer);
    loaderTimer = null;
    const l = document.querySelector("[data-loader]");
    if (l) { l.classList.remove("on"); l.hidden = true; }
  }

  // A boosted navigation swaps the body and keeps the head: after an upgrade, the new page would run on the old
  // stylesheet and script. When the page's assets changed, load it whole instead.
  document.addEventListener("htmx:beforeSwap", function (e) {
    if (!e.detail.boosted || !e.detail.xhr) return;
    const cur = document.querySelector('link[rel="stylesheet"][href*="app.css"]');
    const next = /href="([^"]*app\.css\?v=[^"]*)"/.exec(e.detail.xhr.responseText || "");
    if (cur && next && next[1] !== cur.getAttribute("href")) {
      e.detail.shouldSwap = false;
      location.assign(e.detail.xhr.responseURL || location.href);
    }
  });
  document.addEventListener("htmx:beforeRequest", function (e) { if (e.detail.boosted) loaderOn(); });
  ["htmx:afterSettle", "htmx:responseError", "htmx:sendError", "htmx:timeout", "htmx:afterRequest"].forEach(function (ev) {
    document.addEventListener(ev, function (e) { if (ev === "htmx:afterSettle" || e.detail.boosted || !e.detail.successful) loaderOff(); });
  });
  window.addEventListener("pageshow", loaderOff);

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

  // The service worker shows web push and fills the Windows 11 widget of the installed app; it caches nothing.
  function swRegister() {
    if (window.isSecureContext && "serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(function () {});
  }
  // A link to one integration (#widgets) opens it.
  function openFromHash() {
    const id = decodeURIComponent(location.hash.slice(1));
    const el = id && document.getElementById(id);
    if (el && el.tagName === "DETAILS") { el.open = true; el.scrollIntoView({ block: "start" }); }
  }
  window.addEventListener("hashchange", openFromHash);
  // The top navigation scrolls sideways when it does not fit (phones, tablets): the
  // current page is scrolled into view, and a fade at the edge says there is more.
  function navFit() {
    const nav = document.querySelector(".nav");
    if (!nav) return;
    const more = function () {
      nav.toggleAttribute("data-more-right", nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2);
      nav.toggleAttribute("data-more-left", nav.scrollLeft > 2);
    };
    const cur = nav.querySelector('[aria-current="page"]');
    if (cur && nav.scrollWidth > nav.clientWidth) {
      const r = cur.getBoundingClientRect(), n = nav.getBoundingClientRect();
      if (r.right > n.right || r.left < n.left) nav.scrollLeft += r.left - n.left - (n.width - r.width) / 2;
    }
    more();
    nav.addEventListener("scroll", more, { passive: true });
  }
  window.addEventListener("resize", function () { const nav = document.querySelector(".nav"); if (nav) nav.dispatchEvent(new Event("scroll")); });
  // On phones a table of three or more columns reads better as one card per row
  // (table.cards in app.css). Tables written without the cards class get it here,
  // each cell labelled with its column's heading.
  function cardTables(root) {
    (root || document).querySelectorAll("main table:not(.cards):not(.matrix):not(.keep-table)").forEach(function (t) {
      const heads = Array.from(t.querySelectorAll("thead th")).map(function (th) { return th.textContent.trim(); });
      if (heads.length < 3 || t.querySelector("thead th[colspan]")) return;
      t.querySelectorAll("tbody tr").forEach(function (tr) {
        let col = 0;
        Array.from(tr.children).forEach(function (td) {
          if (!td.hasAttribute("data-label")) td.setAttribute("data-label", heads[col] || "");
          if (!td.textContent.trim() && !td.querySelector("input, select, button, a, img, svg")) td.classList.add("card-empty");
          col += td.colSpan || 1;
        });
      });
      t.classList.add("cards");
    });
  }
  document.body.addEventListener("htmx:afterSwap", function (e) { cardTables(e.target); });
  function init() {
    swRegister();
    openFromHash();
    bellInit();
    goliFromLink();
    tlSetup();
    pushSetup();
    channelFields();
    cycle();
    refreshTimes();
    showLive();
    cardTables();
    navFit();
    document.querySelectorAll("input[data-filter][data-filter-url]").forEach(function (i) {
      const w = new URLSearchParams(window.location.search).get("q");
      if (w) { i.value = w; filterCards(i); }
    });
    const q = document.getElementById("matrix-q");
    const drift = document.getElementById("matrix-drift");
    if (q && drift) {
      const params = new URLSearchParams(window.location.search);
      q.value = params.get("q") || "";
      drift.checked = params.get("drift") === "1";
      filterMatrix();
    }
    syncKindChips();
    foldSame();
  }
  // A combobox for names (data-combo names the datalist of existing ones): as you type it lists the existing names
  // that match and, for a name not among them, offers to create it (data-combo-new: "Create team"). Arrow keys
  // move, Enter picks, Escape closes; screen readers follow aria-activedescendant.
  let comboSeq = 0;
  function comboList(input) {
    let list = document.getElementById(input.getAttribute("aria-controls") || "");
    if (!list) {
      const wrap = document.createElement("span");
      wrap.className = "combo";
      input.parentNode.insertBefore(wrap, input);
      wrap.appendChild(input);
      list = document.createElement("ul");
      list.id = "combo-" + (++comboSeq);
      list.className = "combo-list";
      list.setAttribute("role", "listbox");
      list.hidden = true;
      wrap.appendChild(list);
      input.setAttribute("role", "combobox");
      input.setAttribute("aria-autocomplete", "list");
      input.setAttribute("aria-controls", list.id);
      input.setAttribute("aria-expanded", "false");
      input.setAttribute("autocomplete", "off");
    }
    return list;
  }
  function comboOpen(input) {
    const list = comboList(input);
    const source = document.getElementById(input.getAttribute("data-combo"));
    const names = source ? Array.prototype.map.call(source.options, function (o) { return o.value; }) : [];
    const q = input.value.trim().toLowerCase();
    const shown = names.filter(function (n) { return !q || n.toLowerCase().indexOf(q) >= 0; }).slice(0, 50);
    list.textContent = "";
    shown.forEach(function (n) { comboOption(list, n, n, false); });
    const create = input.getAttribute("data-combo-new");
    if (create && q && !names.some(function (n) { return n.toLowerCase() === q; })) {
      comboOption(list, input.value.trim(), create + " \u201c" + input.value.trim() + "\u201d", true);
    }
    list.hidden = list.children.length === 0;
    input.setAttribute("aria-expanded", String(!list.hidden));
    input.removeAttribute("aria-activedescendant");
  }
  function comboOption(list, value, label, isNew) {
    const li = document.createElement("li");
    li.id = list.id + "-" + list.children.length;
    li.setAttribute("role", "option");
    li.setAttribute("data-value", value);
    li.textContent = label;
    if (isNew) li.className = "combo-new";
    list.appendChild(li);
  }
  function comboClose(input) {
    const list = document.getElementById(input.getAttribute("aria-controls") || "");
    if (list) list.hidden = true;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }
  function comboPick(input, li) {
    input.value = li.getAttribute("data-value");
    comboClose(input);
    input.focus();
  }
  document.addEventListener("focusin", function (e) {
    if (e.target.matches && e.target.matches("input[data-combo]")) comboOpen(e.target);
  });
  document.addEventListener("input", function (e) {
    if (e.target.matches && e.target.matches("input[data-combo]")) comboOpen(e.target);
  });
  document.addEventListener("focusout", function (e) {
    if (e.target.matches && e.target.matches("input[data-combo]")) setTimeout(function () { comboClose(e.target); }, 120);
  });
  document.addEventListener("mousedown", function (e) {
    const li = e.target.closest && e.target.closest(".combo-list [role=option]");
    if (!li) return;
    e.preventDefault();
    comboPick(li.closest(".combo").querySelector("input[data-combo]"), li);
  });
  document.addEventListener("keydown", function (e) {
    const input = e.target;
    if (!input.matches || !input.matches("input[data-combo]")) return;
    const list = document.getElementById(input.getAttribute("aria-controls") || "");
    if (!list || list.hidden) {
      if (e.key === "ArrowDown") { comboOpen(input); e.preventDefault(); }
      return;
    }
    const opts = Array.prototype.slice.call(list.children);
    const cur = opts.findIndex(function (o) { return o.id === input.getAttribute("aria-activedescendant"); });
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = e.key === "ArrowDown" ? Math.min(cur + 1, opts.length - 1) : Math.max(cur - 1, 0);
      opts.forEach(function (o, i) { o.setAttribute("aria-selected", String(i === next)); });
      input.setAttribute("aria-activedescendant", opts[next].id);
      opts[next].scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter" && cur >= 0) {
      e.preventDefault(); // pick, do not send the form yet
      comboPick(input, opts[cur]);
    } else if (e.key === "Escape") {
      comboClose(input);
    }
  });

  window.goliash = { init: init };
  init();
})();
