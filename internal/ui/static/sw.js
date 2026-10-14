// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Goliash's service worker: it shows web push notifications, keeps the installed app's icon badge at the
// number of unread notifications, opens Goliash when one is clicked, and fills the Windows 11 widget. It caches
// nothing.
"use strict";

self.addEventListener("install", function () { self.skipWaiting(); });
self.addEventListener("activate", function (e) { e.waitUntil(self.clients.claim()); });

self.addEventListener("push", function (e) {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (_) { d = { body: e.data.text() }; }
  e.waitUntil(Promise.all([
    self.registration.showNotification(d.title || "Goliash", {
      body: d.body || "",
      tag: d.tag || undefined,
      renotify: Boolean(d.tag),
      icon: "/static/icon-192.png",
      badge: "/static/icon-192.png",
      data: { url: d.url || "/" },
    }),
    updateBadge(),
  ]));
});

// The installed app's icon shows how many notifications wait under the bell.
function updateBadge() {
  if (!self.navigator.setAppBadge) return Promise.resolve();
  return fetch("/ui/notifications/unread", { credentials: "same-origin" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (d) { if (d) return d.unread > 0 ? self.navigator.setAppBadge(d.unread) : self.navigator.clearAppBadge(); })
    .catch(function () { /* offline or signed out */ });
}

self.addEventListener("notificationclick", function (e) {
  e.notification.close();
  const target = new URL(e.notification.data && e.notification.data.url || "/", self.location.origin);
  e.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (wins) {
    for (const w of wins) {
      if (new URL(w.url).origin === self.location.origin && "focus" in w) {
        return w.focus().then(function (f) { return target.origin === self.location.origin ? f.navigate(target.href) : f; });
      }
    }
    return self.clients.openWindow(target.href);
  }));
});

// Windows 11 Widgets (the app installed from Edge): Goliash's numbers on the Widgets board, from
// /api/v1/summary with the app's sign-in, refreshed every 15 minutes and when the board opens.
function renderWidget(widget) {
  const def = widget.definition;
  return Promise.all([
    fetch(def.msAcTemplate).then(function (r) { return r.text(); }),
    fetch(def.data, { credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 || r.status === 403) return { ok: false, message: "Open Goliash and sign in to see its numbers here." };
        if (!r.ok) return { ok: false, message: "Goliash answered " + r.status + "." };
        return r.json().then(function (d) { d.ok = true; return d; });
      })
      .catch(function () { return { ok: false, message: "Goliash cannot be reached." }; }),
  ]).then(function (parts) {
    return self.widgets.updateByTag(def.tag, { template: parts[0], data: JSON.stringify(parts[1]) });
  });
}

self.addEventListener("widgetinstall", function (e) {
  const def = e.widget.definition;
  e.waitUntil((self.registration.periodicSync && def.update
    ? self.registration.periodicSync.getTags().then(function (tags) {
      if (tags.indexOf(def.tag) < 0) return self.registration.periodicSync.register(def.tag, { minInterval: def.update * 1000 });
    }).catch(function () { /* refreshed when the board opens */ })
    : Promise.resolve()).then(function () { return renderWidget(e.widget); }));
});

self.addEventListener("widgetuninstall", function (e) {
  if (e.widget.instances.length <= 1 && self.registration.periodicSync) {
    e.waitUntil(self.registration.periodicSync.unregister(e.widget.definition.tag).catch(function () {}));
  }
});

self.addEventListener("widgetresume", function (e) { e.waitUntil(renderWidget(e.widget)); });

self.addEventListener("periodicsync", function (e) {
  if (!self.widgets) return;
  e.waitUntil(self.widgets.getByTag(e.tag).then(function (w) { if (w) return renderWidget(w); }));
});

self.addEventListener("widgetclick", function (e) {
  if (e.action === "open") e.waitUntil(self.clients.openWindow("/security"));
});
