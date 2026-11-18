# Third-party notices

Ghosttree ships one third-party file inside its binary. Everything else is
written in this repository or comes in through the Go modules listed in
`go.mod`.

## Mermaid

- File: `internal/web/static/vendor/mermaid.min.js`
- Version: 12.3.0, the self-contained script `dist/mermaid.min.js` of the npm
  package `mermaid@12.3.0`, unchanged
- Source: <https://github.com/mermaid-js/mermaid>
- SHA-256: `336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3`
- License: MIT, copyright (c) 2014 - 2022 Knut Sveidqvist. The license text is
  `internal/web/static/vendor/mermaid.LICENSE.txt` and is served next to the
  script.

The script bundles Mermaid's own dependencies (d3, dagre, cytoscape, dompurify,
katex, marked, roughjs and others). The licenses of those parts, as their
authors ask for them, stand in the comment at the end of `mermaid.min.js`.

The interface loads the script only when a text holds a `mermaid` code block,
and only inside a sandboxed frame (`/ui/mermaid-frame`); no page of the
interface runs it.
