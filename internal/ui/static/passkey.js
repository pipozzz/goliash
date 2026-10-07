// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Passkeys: sign in (button, or the browser's suggestion in the e-mail field), use one
// as the second factor, and add one on the account page. Buttons stay hidden where
// the browser cannot use passkeys. Loaded on several pages and again after htmx
// swaps, so it binds once, through the document.
(function () {
  if (window.goliashPasskeys) {
    window.goliashPasskeys.show();
    return;
  }
  var supported = !!(window.PublicKeyCredential && navigator.credentials);

  function b64u(buf) {
    var bytes = new Uint8Array(buf), s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function unb64u(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s), out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out.buffer;
  }

  function post(url, body) {
    return fetch(url, {
      method: "POST", credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error(j.error || "Something went wrong (" + r.status + ").");
        return j;
      });
    });
  }

  function showError(scope, msg) {
    var el = (scope && scope.querySelector("[data-passkey-error]")) || document.querySelector("[data-passkey-error]");
    if (!el) return;
    el.textContent = msg;
    el.hidden = !msg;
  }

  function scopeOf(el) { return el.closest(".card, .panel") || document; }

  // A sign-in or second factor: options from the server, the browser asks for the
  // passkey, the server checks the answer and says where to go.
  var pending = null;
  function signIn(base, mediation) {
    if (pending) pending.abort();
    var ctl = new AbortController();
    pending = ctl;
    return post(base + "/options").then(function (o) {
      var pk = o.publicKey;
      pk.challenge = unb64u(pk.challenge);
      (pk.allowCredentials || []).forEach(function (c) { c.id = unb64u(c.id); });
      var req = { publicKey: pk, signal: ctl.signal };
      if (mediation) req.mediation = mediation;
      return navigator.credentials.get(req);
    }).then(function (cred) {
      if (!cred) return null;
      var r = cred.response;
      return post(base, {
        id: cred.id, rawId: b64u(cred.rawId), type: cred.type,
        response: {
          clientDataJSON: b64u(r.clientDataJSON), authenticatorData: b64u(r.authenticatorData),
          signature: b64u(r.signature), userHandle: r.userHandle ? b64u(r.userHandle) : null,
        },
      });
    }).then(function (j) {
      if (j && j.redirect) window.location.assign(j.redirect);
    });
  }

  function add(button) {
    var scope = scopeOf(button), nameEl = document.getElementById("passkey-name");
    var name = nameEl ? nameEl.value.trim() : "";
    showError(scope, "");
    button.disabled = true;
    post(button.getAttribute("data-passkey-add") + "/options").then(function (o) {
      var pk = o.publicKey;
      pk.challenge = unb64u(pk.challenge);
      pk.user.id = unb64u(pk.user.id);
      (pk.excludeCredentials || []).forEach(function (c) { c.id = unb64u(c.id); });
      return navigator.credentials.create({ publicKey: pk });
    }).then(function (cred) {
      var r = cred.response;
      return post(button.getAttribute("data-passkey-add") + "?name=" + encodeURIComponent(name), {
        id: cred.id, rawId: b64u(cred.rawId), type: cred.type,
        response: {
          clientDataJSON: b64u(r.clientDataJSON), attestationObject: b64u(r.attestationObject),
          transports: r.getTransports ? r.getTransports() : [],
        },
      });
    }).then(function (j) {
      if (j && j.redirect) window.location.assign(j.redirect);
    }).catch(function (e) {
      if (e && e.name === "InvalidStateError") e = new Error("This device has a passkey for your account here already.");
      if (e && e.name !== "NotAllowedError") showError(scope, e.message);
    }).finally(function () { button.disabled = false; });
  }

  document.addEventListener("click", function (ev) {
    var b = ev.target.closest("[data-passkey]");
    if (b) {
      ev.preventDefault();
      showError(scopeOf(b), "");
      signIn(b.getAttribute("data-passkey")).catch(function (e) {
        if (e && e.name !== "NotAllowedError" && e.name !== "AbortError") showError(scopeOf(b), e.message);
      });
      return;
    }
    var a = ev.target.closest("[data-passkey-add]");
    if (a) {
      ev.preventDefault();
      add(a);
    }
  });

  function show() {
    document.querySelectorAll("[data-passkey], [data-passkey-add]").forEach(function (b) { b.hidden = !supported; });
    document.querySelectorAll("[data-passkey-unsupported]").forEach(function (p) { p.hidden = supported; });
  }
  window.goliashPasskeys = { show: show };
  show();

  // The browser suggests passkeys in the e-mail field of the sign-in page.
  var field = document.querySelector('input[autocomplete~="webauthn"]');
  if (supported && field && PublicKeyCredential.isConditionalMediationAvailable) {
    PublicKeyCredential.isConditionalMediationAvailable().then(function (ok) {
      if (ok) signIn("/auth/passkey", "conditional").catch(function () {});
    });
  }
})();
