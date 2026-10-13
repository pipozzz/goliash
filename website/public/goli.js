// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Goli on goliash.dev: the plushie in the corner answers questions about Goliash from its documentation,
// through Claude on the demo server. Answers are rendered from a little Markdown into DOM nodes, never markup.
(function () {
  if (window.__goli) return;
  window.__goli = true;
  let endpoint = "https://demo.goliash.dev/goli/docs";
  try { endpoint = localStorage.getItem("goli.endpoint") || endpoint; } catch (_) { /* storage off */ }
  const SVG = "<svg class=\"goli-art\" viewBox=\"128 64 404 534\" aria-hidden=\"true\" focusable=\"false\"> <g class=\"goli-bell\"> <path d=\"M320 178 Q312 145 320 118\" stroke=\"#4a55b8\" stroke-width=\"8\" fill=\"none\" stroke-linecap=\"round\"></path> <path d=\"M300 112 Q300 78 320 76 Q340 78 340 112 L346 120 L294 120 Z\" fill=\"#f2b84b\" stroke=\"#c98f27\" stroke-width=\"3\" stroke-linejoin=\"round\"></path> <circle cx=\"320\" cy=\"126\" r=\"6\" fill=\"#c98f27\"></circle> <path class=\"goli-ding\" d=\"M352 86 q8 6 4 16 M360 78 q14 10 6 28\" stroke=\"#f2b84b\" stroke-width=\"3\" fill=\"none\" stroke-linecap=\"round\"></path> </g> <path d=\"M232 222 Q218 172 250 168 Q262 190 268 206 Z\" fill=\"#4a55b8\"></path> <path d=\"M408 222 Q422 172 390 168 Q378 190 372 206 Z\" fill=\"#4a55b8\"></path> <g class=\"goli-arm\"> <ellipse cx=\"172\" cy=\"420\" rx=\"34\" ry=\"62\" transform=\"rotate(25 172 420)\" fill=\"#5f6cd6\"></ellipse> </g> <path d=\"M320 172 C445 172 488 285 488 405 C488 525 425 568 320 568 C215 568 152 525 152 405 C152 285 195 172 320 172 Z\" fill=\"#6c7ae0\"></path> <path d=\"M320 182 L320 238\" stroke=\"#4a55b8\" stroke-width=\"2.5\" stroke-dasharray=\"6 5\" stroke-linecap=\"round\"></path> <ellipse cx=\"320\" cy=\"462\" rx=\"104\" ry=\"86\" fill=\"#f6ecd9\"></ellipse> <ellipse cx=\"320\" cy=\"462\" rx=\"94\" ry=\"76\" fill=\"none\" stroke=\"#d9c8a8\" stroke-width=\"2\" stroke-dasharray=\"5 5\"></ellipse> <rect x=\"266\" y=\"428\" width=\"108\" height=\"50\" rx=\"10\" fill=\"#fff\" stroke=\"#2b2d42\" stroke-width=\"3\"></rect> <path d=\"M284 453 h72\" stroke=\"#2b2d42\" stroke-width=\"6\" stroke-linecap=\"round\" opacity=\"0.8\"></path> <circle cx=\"372\" cy=\"428\" r=\"12\" fill=\"#3dbb7c\" stroke=\"#fff\" stroke-width=\"3\"></circle> <path d=\"M366 428 l4 4 l8 -8\" stroke=\"#fff\" stroke-width=\"3\" fill=\"none\" stroke-linecap=\"round\" stroke-linejoin=\"round\"></path> <ellipse cx=\"258\" cy=\"566\" rx=\"58\" ry=\"26\" fill=\"#4a55b8\"></ellipse> <ellipse cx=\"382\" cy=\"566\" rx=\"58\" ry=\"26\" fill=\"#4a55b8\"></ellipse> <path d=\"M236 562 v-8 M258 560 v-10 M280 562 v-8 M360 562 v-8 M382 560 v-10 M404 562 v-8\" stroke=\"#3a4396\" stroke-width=\"3\" stroke-linecap=\"round\"></path> <g class=\"goli-eyes\"> <circle cx=\"275\" cy=\"290\" r=\"34\" fill=\"#fff\"></circle> <circle cx=\"365\" cy=\"290\" r=\"34\" fill=\"#fff\"></circle> <g class=\"goli-pupils\"> <circle cx=\"281\" cy=\"286\" r=\"16\" fill=\"#2b2d42\"></circle> <circle cx=\"371\" cy=\"286\" r=\"16\" fill=\"#2b2d42\"></circle> <circle cx=\"287\" cy=\"279\" r=\"5\" fill=\"#fff\"></circle> <circle cx=\"377\" cy=\"279\" r=\"5\" fill=\"#fff\"></circle> </g> </g> <circle cx=\"275\" cy=\"290\" r=\"41\" fill=\"none\" stroke=\"#2b2d42\" stroke-width=\"6\"></circle> <circle cx=\"365\" cy=\"290\" r=\"41\" fill=\"none\" stroke=\"#2b2d42\" stroke-width=\"6\"></circle> <path d=\"M312 284 Q320 276 328 284\" stroke=\"#2b2d42\" stroke-width=\"6\" fill=\"none\"></path> <path d=\"M250 236 Q272 228 294 236\" stroke=\"#2b2d42\" stroke-width=\"5\" fill=\"none\" stroke-linecap=\"round\"></path> <path class=\"goli-brow\" d=\"M344 226 Q368 208 390 222\" stroke=\"#2b2d42\" stroke-width=\"5\" fill=\"none\" stroke-linecap=\"round\"></path> <ellipse cx=\"236\" cy=\"346\" rx=\"18\" ry=\"10\" fill=\"#f28fa6\" opacity=\"0.7\"></ellipse> <ellipse cx=\"404\" cy=\"346\" rx=\"18\" ry=\"10\" fill=\"#f28fa6\" opacity=\"0.7\"></ellipse> <path d=\"M302 348 Q320 368 338 348\" stroke=\"#2b2d42\" stroke-width=\"5\" fill=\"none\" stroke-linecap=\"round\"></path> <path d=\"M314 357 l3 9 l3 -9\" fill=\"#fff\" stroke=\"#2b2d42\" stroke-width=\"1.5\" stroke-linejoin=\"round\"></path> <g stroke=\"#2b2d42\" stroke-width=\"3\" stroke-linejoin=\"round\"> <rect x=\"452\" y=\"452\" width=\"68\" height=\"40\" rx=\"6\" fill=\"#ef8a5b\"></rect> <rect x=\"458\" y=\"412\" width=\"62\" height=\"38\" rx=\"6\" fill=\"#3dbb7c\"></rect> <rect x=\"464\" y=\"374\" width=\"54\" height=\"36\" rx=\"6\" fill=\"#f2b84b\"></rect> </g> <path d=\"M466 460 v24 M478 460 v24 M490 460 v24 M502 460 v24 M472 420 v22 M484 420 v22 M496 420 v22 M508 420 v22 M476 382 v20 M488 382 v20 M500 382 v20\" stroke=\"#2b2d42\" stroke-width=\"2\" opacity=\"0.45\"></path> <ellipse cx=\"462\" cy=\"440\" rx=\"30\" ry=\"58\" transform=\"rotate(-30 462 440)\" fill=\"#5f6cd6\"></ellipse> </svg>";
  const key = "goli.docs.history";
  const ideas = [
    "What does Goliash do, in one minute?",
    "Can I try it without installing anything?",
    "How do I connect an EKS cluster?",
    "How does it find vulnerabilities in running images?"
  ];

  const css = `
.goli-fab { position: fixed; right: 22px; bottom: 18px; z-index: 60; width: 66px; height: 86px; padding: 0; border: 0; background: transparent; cursor: pointer; filter: drop-shadow(0 8px 12px rgb(10 14 30 / .25)); transition: transform .18s ease-out; }
.goli-fab:hover { transform: translateY(-4px); }
.goli-fab:focus-visible { outline: 2px solid var(--sl-color-accent); outline-offset: 6px; border-radius: 14px; }
.goli-fab .goli-art { width: 100%; height: 100%; transform-origin: 50% 100%; animation: goli-sway 5s ease-in-out infinite; }
.goli-fab:hover .goli-arm { animation: goli-wave .7s ease-in-out 2; }
.goli-art { display: block; overflow: visible; }
.goli-bubble { position: fixed; right: 96px; bottom: 74px; z-index: 60; background: var(--sl-color-bg-nav, #fff); color: var(--sl-color-white); border: 1px solid var(--sl-color-hairline); border-radius: 14px 14px 4px 14px; padding: 8px 12px; font-size: .85rem; box-shadow: 0 10px 30px rgb(10 14 30 / .15); animation: goli-pop .3s ease-out; cursor: pointer; max-width: 220px; }
@keyframes goli-pop { from { transform: translateY(6px) scale(.96); opacity: 0; } to { transform: none; opacity: 1; } }
@keyframes goli-sway { 0%, 100% { transform: rotate(0); } 30% { transform: rotate(-2.5deg) translateY(-1px); } 70% { transform: rotate(2.5deg) translateY(-1px); } }
.goli-arm { transform-box: fill-box; transform-origin: 80% 15%; }
@keyframes goli-wave { 0%, 100% { transform: rotate(0); } 50% { transform: rotate(38deg); } }
.goli-eyes { transform-box: fill-box; transform-origin: center; animation: goli-blink 7s infinite; }
@keyframes goli-blink { 0%, 94%, 100% { transform: none; } 96% { transform: scaleY(.12); } }
.goli-pupils { transform: translate(var(--goli-x, 0), var(--goli-y, 0)); transition: transform .12s ease-out; }
.goli-brow { transition: transform .25s ease-out; }
.goli-busy .goli-brow { transform: translateY(-10px); }
.goli-bell { transform-box: fill-box; transform-origin: 36% 100%; }
.goli-ding { opacity: 0; transition: opacity .2s; }
.goli-busy .goli-bell { animation: goli-ring .9s ease-in-out infinite; }
.goli-busy .goli-ding, .goli-done .goli-ding { opacity: 1; }
.goli-done .goli-bell { animation: goli-ring .45s ease-in-out 3; }
@keyframes goli-ring { 0%, 100% { transform: rotate(0); } 25% { transform: rotate(14deg); } 75% { transform: rotate(-14deg); } }
.goli-panel { position: fixed; right: 18px; bottom: 18px; z-index: 61; width: min(420px, calc(100vw - 24px)); height: min(620px, calc(100vh - 36px)); display: flex; flex-direction: column; background: var(--sl-color-bg, #fff); color: var(--sl-color-white); border: 1px solid var(--sl-color-hairline); border-radius: 18px; box-shadow: 0 30px 80px rgb(10 14 30 / .3); overflow: hidden; animation: goli-pop .18s ease-out; font-size: .95rem; }
.goli-top { display: flex; align-items: center; gap: 10px; padding: 12px 14px; border-bottom: 1px solid var(--sl-color-hairline); }
.goli-top .goli-art { width: 28px; height: 37px; }
.goli-top strong { font-size: 1.05rem; }
.goli-top span { color: var(--sl-color-gray-3); font-size: .8rem; flex: 1; }
.goli-top button { background: none; border: 1px solid var(--sl-color-hairline); color: var(--sl-color-gray-2); border-radius: 8px; padding: 2px 10px; cursor: pointer; font: inherit; font-size: .8rem; }
.goli-log { flex: 1; overflow-y: auto; overflow-x: hidden; padding: 14px; display: flex; flex-direction: column; gap: 12px; }
.goli-msg { line-height: 1.5; overflow-wrap: anywhere; }
.goli-msg p { margin: 0 0 8px; } .goli-msg p:last-child { margin-bottom: 0; }
.goli-msg ul, .goli-msg ol { margin: 0 0 8px; padding-left: 20px; }
.goli-msg code { font-size: .85em; background: var(--sl-color-gray-6); padding: 1px 5px; border-radius: 5px; }
.goli-msg pre { background: var(--sl-color-gray-6); padding: 10px; border-radius: 8px; overflow-x: auto; margin: 0 0 8px; }
.goli-msg pre code { background: none; padding: 0; }
.goli-q { align-self: flex-end; max-width: 85%; background: var(--sl-color-accent-low); padding: 8px 12px; border-radius: 14px 14px 4px 14px; white-space: pre-wrap; }
.goli-note { color: var(--sl-color-gray-3); font-size: .8rem; font-style: italic; }
.goli-error { color: var(--sl-color-red, #c0392b); font-size: .85rem; }
.goli-hello { display: flex; flex-direction: column; gap: 8px; margin: auto 0; }
.goli-hello .goli-art { width: 90px; height: 118px; align-self: center; }
.goli-hello button { text-align: left; background: var(--sl-color-gray-6); border: 1px solid var(--sl-color-hairline); color: var(--sl-color-white); border-radius: 10px; padding: 8px 12px; font: inherit; font-size: .88rem; cursor: pointer; }
.goli-hello button:hover { border-color: var(--sl-color-accent); }
.goli-form { display: flex; gap: 8px; padding: 10px 14px 6px; border-top: 1px solid var(--sl-color-hairline); }
.goli-form textarea { flex: 1; resize: none; font: inherit; font-size: .9rem; padding: 8px 10px; border-radius: 10px; border: 1px solid var(--sl-color-gray-5); background: var(--sl-color-bg); color: var(--sl-color-white); }
.goli-form button { background: var(--sl-color-accent); color: var(--sl-color-text-invert, #fff); border: 0; border-radius: 10px; padding: 0 14px; font: inherit; font-weight: 600; cursor: pointer; }
.goli-foot { margin: 0; padding: 0 14px 10px; color: var(--sl-color-gray-3); font-size: .72rem; }
@media (prefers-reduced-motion: reduce) { .goli-fab .goli-art, .goli-fab:hover .goli-arm, .goli-eyes, .goli-busy .goli-bell, .goli-done .goli-bell, .goli-bubble, .goli-panel { animation: none; } .goli-pupils { transform: none; } }
@media (max-width: 50rem) { .goli-fab { width: 50px; height: 66px; right: 12px; bottom: 12px; } .goli-bubble { right: 70px; bottom: 56px; } .goli-panel { right: 0; bottom: 0; width: 100vw; height: 100dvh; border-radius: 0; } }
@media print { .goli-fab, .goli-panel, .goli-bubble { display: none; } }
`;

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function art() {
    const t = document.createElement("template");
    t.innerHTML = SVG; // a constant of this file, never user input
    return t.content.firstChild;
  }
  function inline(parent, text) {
    const re = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      const t = m[0];
      if (t[0] === "*") parent.appendChild(el("strong", "", t.slice(2, -2)));
      else if (t[0] === "`") parent.appendChild(el("code", "", t.slice(1, -1)));
      else {
        const label = t.slice(1, t.indexOf("](")), href = t.slice(t.indexOf("](") + 2, -1);
        if ((href[0] === "/" && href[1] !== "/") || /^https:\/\//.test(href)) {
          const a = el("a", "", label);
          a.href = href;
          if (href[0] !== "/") { a.target = "_blank"; a.rel = "noopener"; }
          parent.appendChild(a);
        } else parent.appendChild(document.createTextNode(label));
      }
      last = m.index + t.length;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  }
  function markdown(text) {
    const out = document.createDocumentFragment();
    let list = null, para = null, code = null;
    text.split("\n").forEach(function (line) {
      if (code) { if (/^```/.test(line)) { code = null; return; } code.textContent += (code.textContent ? "\n" : "") + line; return; }
      if (/^```/.test(line)) { list = para = null; code = el("code"); const pre = el("pre"); pre.appendChild(code); out.appendChild(pre); return; }
      const item = /^\s*(?:[-*•]|(\d+)\.)\s+(.*)$/.exec(line);
      if (item) {
        para = null;
        const tag = item[1] ? "ol" : "ul";
        if (!list || list.tagName.toLowerCase() !== tag) { list = el(tag); out.appendChild(list); }
        const li = el("li"); inline(li, item[2]); list.appendChild(li); return;
      }
      list = null;
      if (!line.trim()) { para = null; return; }
      const head = /^#{1,6}\s+(.*)$/.exec(line);
      if (head) { para = null; const h = el("p"); const s = el("strong"); inline(s, head[1]); h.appendChild(s); out.appendChild(h); return; }
      if (para) para.appendChild(document.createElement("br")); else { para = el("p"); out.appendChild(para); }
      inline(para, line);
    });
    return out;
  }

  function history() { try { const h = JSON.parse(sessionStorage.getItem(key) || "[]"); return Array.isArray(h) ? h : []; } catch (_) { return []; } }
  function save(h) { try { sessionStorage.setItem(key, JSON.stringify(h.slice(-10))); } catch (_) { /* storage off */ } }

  // Goli shows up only when the server answers: until then, and wherever it is off, the site stays as it is.
  function reachable() {
    try { const c = sessionStorage.getItem("goli.up"); if (c) return Promise.resolve(c === "1"); } catch (_) { /* storage off */ }
    return fetch(endpoint, { method: "OPTIONS" }).then(function (r) { return r.status === 204; }, function () { return false; })
      .then(function (up) { try { sessionStorage.setItem("goli.up", up ? "1" : "0"); } catch (_) { /* storage off */ } return up; });
  }
  reachable().then(function (up) { if (up) start(); });

  function start() {
  const style = el("style"); style.textContent = css; document.head.appendChild(style);
  const fab = el("button", "goli-fab");
  fab.type = "button";
  fab.setAttribute("aria-label", "Ask Goli about Goliash");
  fab.title = "Ask Goli about Goliash";
  fab.appendChild(art());
  document.body.appendChild(fab);

  // Once per visit, a small hello beside it; a click on it opens the chat, and it leaves by itself.
  let bubble = null;
  try {
    if (!sessionStorage.getItem("goli.hello")) {
      sessionStorage.setItem("goli.hello", "1");
      setTimeout(function () {
        if (state.open) return;
        bubble = el("div", "goli-bubble", "Hi! I'm Goli. Ask me anything about Goliash.");
        bubble.addEventListener("click", function () { open(); });
        document.body.appendChild(bubble);
        setTimeout(function () { if (bubble) { bubble.remove(); bubble = null; } }, 7000);
      }, 2500);
    }
  } catch (_) { /* storage off: no hello */ }

  const state = { open: false, busy: false, answer: "", error: "", panel: null, log: null, input: null, send: null };

  document.addEventListener("pointermove", function (e) {
    if (e.pointerType === "touch") return;
    document.querySelectorAll(".goli-art").forEach(function (a) {
      const r = a.getBoundingClientRect();
      if (!r.width) return;
      const dx = e.clientX - (r.left + r.width / 2), dy = e.clientY - (r.top + r.height * 0.42);
      const d = Math.hypot(dx, dy) || 1, k = Math.min(1, d / 300) * 9 / d;
      a.style.setProperty("--goli-x", (dx * k).toFixed(1) + "px");
      a.style.setProperty("--goli-y", (dy * k).toFixed(1) + "px");
    });
  }, { passive: true });

  function render() {
    const log = state.log;
    log.replaceChildren();
    const h = history();
    if (!h.length && !state.busy) {
      const hello = el("div", "goli-hello");
      hello.appendChild(art());
      hello.appendChild(el("p", "", "Hi, I'm Goli. Ask me about Goliash: what it does, whether it fits your setup, how to install and use it."));
      ideas.forEach(function (q) {
        const b = el("button", "", q); b.type = "button";
        b.addEventListener("click", function () { ask(q); });
        hello.appendChild(b);
      });
      log.appendChild(hello);
    }
    h.forEach(function (t) {
      const m = el("div", "goli-msg " + (t.role === "user" ? "goli-q" : ""));
      if (t.role === "user") m.textContent = t.text; else m.appendChild(markdown(t.text));
      if (t.error) m.appendChild(el("p", "goli-error", t.error));
      log.appendChild(m);
    });
    if (state.busy) {
      const m = el("div", "goli-msg");
      if (state.answer) m.appendChild(markdown(state.answer)); else m.appendChild(el("p", "goli-note", "Goli is looking it up…"));
      log.appendChild(m);
    }
    log.scrollTop = log.scrollHeight;
    state.send.textContent = state.busy ? "Stop" : "Ask";
  }
  let frame = 0;
  function later() { if (!frame) frame = requestAnimationFrame(function () { frame = 0; render(); }); }

  function ask(text) {
    text = (text || "").trim();
    if (!text || state.busy) return;
    const h = history();
    h.push({ role: "user", text: text.slice(0, 1000) });
    save(h);
    state.input.value = "";
    state.busy = true; state.answer = ""; state.error = "";
    state.abort = new AbortController();
    document.documentElement.classList.add("goli-busy");
    render();
    fetch(endpoint, {
      method: "POST", signal: state.abort.signal, headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ history: h.map(function (t) { return { role: t.role, text: t.text }; }).filter(function (t) { return t.text; }) })
    }).then(function (r) {
      if (!r.ok || !r.body) {
        return r.json().then(function (j) { state.error = j.error || ("Goli could not answer (" + r.status + ")."); },
          function () { state.error = "Goli could not answer (" + r.status + ")."; });
      }
      const reader = r.body.getReader(), dec = new TextDecoder();
      let buf = "";
      function pump() {
        return reader.read().then(function (x) {
          if (x.done) return;
          buf += dec.decode(x.value, { stream: true });
          let i;
          while ((i = buf.indexOf("\n\n")) >= 0) {
            const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
            if (chunk.indexOf("data: ") !== 0) continue;
            let ev; try { ev = JSON.parse(chunk.slice(6)); } catch (_) { continue; }
            if (ev.type === "text") state.answer += ev.text; else if (ev.type === "error") state.error = ev.text;
          }
          later();
          return pump();
        });
      }
      return pump();
    }).catch(function (e) {
      state.error = e && e.name === "AbortError" ? "Stopped." : "Goli is resting right now. The search at the top knows the docs too.";
    }).then(function () {
      const done = history();
      done.push({ role: "assistant", text: state.answer, error: state.error || undefined });
      save(done);
      state.busy = false; state.abort = null;
      document.documentElement.classList.remove("goli-busy");
      document.documentElement.classList.add("goli-done");
      setTimeout(function () { document.documentElement.classList.remove("goli-done"); }, 1400);
      render();
      state.input.focus();
    });
  }

  function open() {
    if (bubble) { bubble.remove(); bubble = null; }
    if (!state.panel) {
      const p = el("div", "goli-panel");
      p.setAttribute("role", "dialog");
      p.setAttribute("aria-label", "Ask Goli about Goliash");
      const top = el("div", "goli-top");
      top.appendChild(art());
      top.appendChild(el("strong", "", "Goli"));
      top.appendChild(el("span", "", "knows the Goliash docs"));
      const clear = el("button", "", "New"); clear.type = "button";
      clear.addEventListener("click", function () { save([]); render(); state.input.focus(); });
      const close = el("button", "", "×"); close.type = "button"; close.setAttribute("aria-label", "Close");
      close.addEventListener("click", shut);
      top.appendChild(clear); top.appendChild(close);
      const log = el("div", "goli-log"); log.setAttribute("aria-live", "polite");
      const form = el("form", "goli-form");
      const input = el("textarea"); input.rows = 2; input.maxLength = 1000;
      input.placeholder = "Ask about Goliash…"; input.setAttribute("aria-label", "Your question");
      const send = el("button", "", "Ask"); send.type = "submit";
      form.appendChild(input); form.appendChild(send);
      form.addEventListener("submit", function (e) { e.preventDefault(); if (state.busy) { if (state.abort) state.abort.abort(); return; } ask(input.value); });
      input.addEventListener("keydown", function (e) { if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); } });
      const foot = el("p", "goli-foot", "Answers come from the documentation through Claude, by Anthropic, and can be wrong. Questions are not stored.");
      p.appendChild(top); p.appendChild(log); p.appendChild(form); p.appendChild(foot);
      p.addEventListener("keydown", function (e) { if (e.key === "Escape") shut(); });
      Object.assign(state, { panel: p, log: log, input: input, send: send });
    }
    document.body.appendChild(state.panel);
    fab.hidden = true;
    state.open = true;
    render();
    state.input.focus();
  }
  function shut() {
    state.panel.remove();
    state.open = false;
    fab.hidden = false;
    fab.focus();
  }
  fab.addEventListener("click", open);
  }
})();
