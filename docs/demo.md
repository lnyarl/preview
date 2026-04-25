# Demo

This document is a guide for capturing and curating Preview Hub demo artifacts
(screenshots, an asciinema recording, and a short script). All artifacts are
**placeholders** in the repository — replace them with real captures before
publishing.

## Screenshots

Capture the following four pages from the running admin dashboard at
`http://localhost:3000/admin` (with `ADMIN_PASSWORD=demo`).

![Dashboard home](./screenshots/dashboard.png)
![Agents page](./screenshots/agents.png)
![Previews list](./screenshots/previews.png)
![Preview detail with timeline](./screenshots/preview-detail.png)

## asciinema recording

We use [asciinema](https://asciinema.org/) for terminal demos because it
captures the actual text instead of a video file — viewers can copy/paste
commands.

Record:

```bash
asciinema rec docs/demo.cast
```

Stop recording with `Ctrl-D`.

### Steps to demo

The recording walks through a complete PR lifecycle. Follow these steps in
order:

1. Start the hub with `ADMIN_PASSWORD=demo go run ./cmd/hub`.
2. Open `http://localhost:3000/admin/agents`, create an agent named `home`,
   copy the one-time token from the redirected page.
3. Start the agent in another terminal:
   `go run ./cmd/agent start --hub-url=ws://localhost:3000/agent/ws --token=$TOKEN --repo-url=$REPO --label local`.
4. Send a fake `pull_request opened` webhook to
   `http://localhost:3000/webhooks/github` (HMAC-signed with
   `GITHUB_WEBHOOK_SECRET`).
5. Watch the preview transition through `queued → assigned → building →
   running` on `/admin/previews/{id}`.
6. Open `http://pr-1.preview.localhost:3000/` and confirm the preview app
   responds.
7. Send a fake `pull_request closed` webhook and watch the preview transition
   through `teardown → done`.

## Editing the cast

```bash
asciinema play docs/demo.cast    # preview
asciinema upload docs/demo.cast  # publish to asciinema.org
```

Replace `docs/demo.gif` (referenced from the README) with a small GIF generated
from the cast via [`agg`](https://github.com/asciinema/agg) or a screen
recorder.
