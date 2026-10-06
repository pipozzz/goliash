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
    const typing = e.target.closest("input, textarea, select, [contenteditable]");
    // "/" jumps to the matrix filter, as on GitHub; Escape in the filter clears it.
    if (q && e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); q.focus(); q.select(); return; }
    if (e.key !== "Escape") return;
    if (q && e.target === q && q.value) { q.value = ""; filterMatrix(); return; }
    const open = document.querySelector("details.menu[open]");
    if (open) { open.removeAttribute("open"); open.querySelector("summary").focus(); }
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
