// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Goliash's service worker: it shows web push notifications and opens Goliash when
// one is clicked. It caches nothing.
"use strict";

self.addEventListener("install", function () { self.skipWaiting(); });
self.addEventListener("activate", function (e) { e.waitUntil(self.clients.claim()); });

self.addEventListener("push", function (e) {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (_) { d = { body: e.data.text() }; }
  e.waitUntil(self.registration.showNotification(d.title || "Goliash", {
    body: d.body || "",
    tag: d.tag || undefined,
    renotify: Boolean(d.tag),
    icon: "/static/icon-192.png",
    badge: "/static/icon-192.png",
    data: { url: d.url || "/" },
  }));
});

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
