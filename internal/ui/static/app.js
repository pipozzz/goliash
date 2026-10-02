// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Live updates: the server sends "changed" over SSE when snapshots, releases or drift
// change something; parts of the page listening for goliash:changed reload themselves.
(function () {
  const live = document.querySelector("[data-live]");
  function connect() {
    if (!window.EventSource) return;
    const es = new EventSource("/ui/stream");
    es.addEventListener("changed", function () {
      htmx.trigger(document.body, "goliash:changed");
    });
    es.onopen = function () { if (live) { live.classList.remove("off"); live.textContent = "Live"; } };
    es.onerror = function () { if (live) { live.classList.add("off"); live.textContent = "Reconnecting…"; } };
  }
  connect();

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
  refreshTimes();
  setInterval(refreshTimes, 30000);
  document.body.addEventListener("htmx:afterSwap", function (e) { refreshTimes(e.target); });

  // Copy buttons for one-time secrets.
  document.addEventListener("click", function (e) {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const text = document.getElementById(btn.getAttribute("data-copy")).textContent;
    navigator.clipboard.writeText(text).then(
      function () { btn.textContent = "Copied"; },
      function () { window.getSelection().selectAllChildren(document.getElementById(btn.getAttribute("data-copy"))); }
    );
  });
})();
