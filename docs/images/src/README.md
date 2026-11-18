# Pictures in the README

Every picture in the README is taken from the real web interface with invented
data. Nothing in them comes from a real person, project, or machine.

## The loop at the top

`demo-loop.webp` comes from [`film/make.sh`](film/make.sh):

```bash
PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs docs/images/src/film/make.sh
```

It builds `ctx` and the seed in [`readme/`](readme/) and starts a throwaway
server like the pictures below. [`film/takes.mjs`](film/takes.mjs) then signs
in as Mia and records four takes at twice the pixel density, with real clicks:
the failing test in the session that found the duplicate receipts, Mia
approving the pitfall that agent proposed, the payments briefing that now
contains it, and the refunds agent's message that links to it. Each take writes
the time and position of what it points at, so [`film/gen.mjs`](film/gen.mjs)
can frame and highlight exactly that. It writes a
[HyperFrames](https://hyperframes.heygen.com) composition, `npx hyperframes`
renders it, and ffmpeg turns it into an animated WebP. GitHub plays an animated
WebP in the README on its own; a video file in the repository does not play
inline. With `KEEP=/path` the recordings and the composition are kept.

`TAKES=all` records the takes of the launch film and of the clips on the
website as well. The launch film needs music and sound effects that are not in
this repository; they live with the website material.

Fonts in [`fonts/`](fonts/) are Fira Sans Condensed Heavy and IBM Plex Sans,
both under the SIL Open Font License, as on the website.

## Interface pictures

`ui-*.png` come from [`readme/make.sh`](readme/make.sh):

```bash
PLAYWRIGHT=/path/to/node_modules/playwright/index.mjs docs/images/src/readme/make.sh
```

It builds `ctx` and the seed in [`readme/`](readme/), fills a new database in a
temporary directory with the studio "Northwind" (Mia, Theo, four projects,
agents in every state, requests, tasks, knowledge, an idea in the workshop, a
workflow run, a runbook, and a work package on a pull request), starts a server
on `127.0.0.1` under an empty `HOME`, signs in as Mia with a one-time link, and
takes the pictures in light mode at 1280 pixels width with
[`readme/shoot.mjs`](readme/shoot.mjs). The temporary directory is removed at
the end.

The seed is built with the tag `readmeshots`, so `go build ./...` and
`go vet ./...` leave it out. When the store changes, build it once with
`go build -tags readmeshots ./docs/images/src/readme` and fix what breaks.

Needs Go, Node with Playwright, and pngquant. Set `PORT` if 19631 is taken
(19632 for the film).

## Social preview

`social-preview.png` (1280 × 640) is rendered from
[`social-preview.html`](social-preview.html) with
[`render-social-preview.sh`](render-social-preview.sh). Its picture,
`social-briefing.png`, is a crop of the payments briefing from the film's
demo instance.
