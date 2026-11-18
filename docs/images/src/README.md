# Pictures in the README

Every picture in the README is taken from the real web interface with invented
data. Nothing in them comes from a real person, project, or machine.

## Interface pictures and the loop GIF

`ui-*.png` and `demo-loop.gif` come from [`readme/make.sh`](readme/make.sh):

```bash
PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs docs/images/src/readme/make.sh
```

It builds `ctx` and the seed in [`readme/`](readme/), fills a new database in a
temporary directory with the studio "Northwind" (Mia, Theo, four projects,
agents in every state, requests, tasks, knowledge, an idea in the workshop, a
workflow run, a runbook, and a work package on a pull request), starts a server
on `127.0.0.1` under an empty `HOME`, signs in as Mia with a one-time link, and
takes the pictures in light mode at 1280 pixels width with
[`readme/shoot.mjs`](readme/shoot.mjs). The GIF is five of those pages at 2.6
seconds each. The temporary directory is removed at the end.

The seed is built with the tag `readmeshots`, so `go build ./...` and
`go vet ./...` leave it out. When the store changes, build it once with
`go build -tags readmeshots ./docs/images/src/readme` and fix what breaks.

Needs Go, Node with Playwright, ffmpeg, and pngquant. Set `PORT` if 19631 is
taken.

## Social preview

`social-preview.png` (1280 × 640) is rendered from
[`social-preview.html`](social-preview.html) with
[`render-social-preview.sh`](render-social-preview.sh). It uses its own copy of
an older room picture in this directory.
