---
title: Goli desk bot
description: 'Goli, the Goliash mascot, as a 3D-printable desk robot with a round AMOLED face that rings a bell when Goliash sends a notification.'
---

[**Goli desk bot**](https://github.com/pipozzz/goli-bot) is Goli, the Goliash mascot, as a robot for your desk:
a 3D-printed body around a round 1.75" AMOLED face and an ESP32-S3. He sits there looking tired and kind, and when
Goliash sends a notification he rings a desk bell and shows it under his mouth.

- **New notification:** a *ding*, a surprised face, a count badge, and the title scrolling by.
- **Something critical** (an exploited vulnerability, an agent gone quiet, drift in production): *ding-ding* and an
  alarmed face with a red badge.
- **Good news** (an agent back, drift resolved): a chirp and a happy face.
- Tap his face to mark the alerts seen. He also reacts to petting, tilting and shaking, and falls asleep when
  nothing happens.

The repository has the printable parts (sized for a Bambu Lab A1 mini, with a multi-colour 3MF), the parts list
(a Waveshare ESP32-S3-Touch-AMOLED-1.75, a small speaker and a few M2 screws) and the assembly steps.

## Connect him to Goliash

1. Install the firmware from the browser: open <https://pipozzz.github.io/goli-bot/> in Chrome or Edge, plug the
   board in by USB-C and press **Install**. Let the installer connect him to Wi-Fi.
2. In Goliash, create an API token for the workspace he should follow: **Settings → Workspace settings → API
   tokens** (admins). The default *viewer* token is enough; see [API tokens](/guide/teams/#api-tokens).
3. On Goli's setup page (the installer shows its link; five quick taps on his face show a QR code for it), enter
   the Goliash address (`GOLIASH_PUBLIC_URL`) and the token.

He reads what Goliash sent, `GET /api/v1/notifications/sent`, so he rings for what the workspace's rules send:
add a rule for the events you want to hear (any channel will do; the In Goliash channel keeps them in the bell
only). He updates his firmware himself over Wi-Fi once a day.
