/* Block view of a workflow file (REQ-697, stage E4): nodes and edges like n8n.
   The page hands over the text, what the server read from it (keys in the
   order of the file, text blocks, instruction, check), the catalogue and the
   texts. The layout follows the fixed order of a workflow, trigger → filter →
   inputs → block (model and budget below it) → results, and is drawn anew on
   every change; nothing about positions is stored. Every edit goes to
   POST /ui/workflows/visual, which rewrites the text on the server and sends
   back text and view; the form's textarea holds the text, so saving stays the
   save of the text form. Without this script the page shows the text. */
(() => {
  "use strict";

  const SVGNS = "http://www.w3.org/2000/svg";
  const SCALARS = ["description", "on", "every", "chain", "uses", "model", "research", "prompt"];

  function parseJSON(s, fallback) {
    try { return JSON.parse(s || ""); } catch (_) { return fallback; }
  }
  function fmt(t, ...args) {
    let i = 0;
    return String(t || "").replace(/%[ds]/g, () => String(args[i++]));
  }
  function blockName(uses) { return String(uses || "").split("@")[0].trim(); }
  function shortName(uses) { return blockName(uses).replace(/^ghosttree\//, ""); }
  function money(v) {
    const n = Number(String(v).replace(",", "."));
    if (!isFinite(n)) return String(v);
    const lang = (document.documentElement && document.documentElement.lang) || "en";
    return n.toLocaleString(lang, { minimumFractionDigits: n % 1 ? 2 : 0, maximumFractionDigits: 2 }) + " $";
  }

  // keysOf maps the keys of a view to their values.
  function keysOf(view) {
    const m = {};
    for (const k of (view && view.keys) || []) m[k.key] = k;
    return m;
  }

  // problemsFor are the errors or warnings that belong to a node: its key, and
  // for a node of one item (a kind of result) that item or the key as a whole.
  function problemsFor(list, key, item) {
    return (list || []).filter((p) => p.key === key && (!item || !p.item || p.item === item));
  }

  // graph lays the view out in columns. It is a pure function of the view, so
  // the same file always gives the same picture.
  function graph(view, catalog, pending) {
    const keys = keysOf(view);
    const has = (k) => Object.prototype.hasOwnProperty.call(keys, k);
    const errs = (view && view.errors) || [];
    const warns = (view && view.warnings) || [];
    const nodes = {};
    const node = (id, o) => (nodes[id] = Object.assign({ id, errors: [], warnings: [], notes: [] }, o));
    // A scalar key without a value ("description:") is drawn like a node still to fill in.
    const blank = (k) => SCALARS.includes(k) && keys[k] && !keys[k].scalar;
    const keyNode = (k, o) => node(k, Object.assign({ key: k, value: keys[k], line: keys[k] && keys[k].line, ghost: blank(k), errors: problemsFor(errs, k), warnings: problemsFor(warns, k) }, o));
    const head = [], cols = [[], [], [], [], []], subs = [], subNotes = [], rest = [], edges = [];

    if (has("description") || pending === "description") head.push(keyNode("description", { kind: "description", ghost: !has("description") || blank("description") }));
    cols[0].push(has("on") ? keyNode("on", { kind: "trigger" }) : node("on", { key: "on", kind: "trigger", ghost: true }));
    // The filter stands below the trigger it narrows, the chain below that.
    if (has("branches")) cols[0].push(keyNode("branches", { kind: "trigger" }));
    if (has("every")) cols[0].push(keyNode("every", { kind: "trigger" }));
    if (has("chain")) cols[0].push(keyNode("chain", { kind: "trigger", sub: true }));
    if (has("inputs")) cols[2].push(keyNode("inputs", { kind: "input" }));
    if ((view && view.body) || pending === "body") cols[3].push(node("body", { key: "body", kind: "task", value: { scalar: (view && view.body) || "" }, line: view && view.body_line, ghost: !(view && view.body) }));
    if (has("prompt")) cols[3].push(keyNode("prompt", { kind: "task" }));
    cols[3].push(has("uses") ? keyNode("uses", { kind: "task" }) : node("uses", { key: "uses", kind: "task", ghost: true }));
    for (const k of ["model", "budget", "research"]) if (has(k)) subs.push(keyNode(k, { kind: "limit", sub: true }));
    if (has("results")) {
      for (const p of keys.results.pairs || []) {
        node("results:" + p.key, { key: "results", item: p.key, kind: "result", value: p, line: keys.results.line,
          errors: problemsFor(errs, "results", p.key), warnings: problemsFor(warns, "results", p.key) });
        cols[4].push(nodes["results:" + p.key]);
      }
      // A problem about results as a whole (no item) goes to the first kind.
      if (!cols[4].length) cols[4].push(keyNode("results", { kind: "result", item: "" }));
    }
    // Text blocks: beside the key they stand above in the file.
    for (const b of (view && view.blocks) || []) {
      const n = node("block:" + b.id, { kind: "text", block: b, line: b.line, value: { scalar: b.text },
        errors: errs.filter((p) => (b.key && p.key === b.key) || (!p.key && p.line >= b.line && p.line < b.line + b.text.split("\n").length)) });
      const owner = b.owner;
      if (!owner) { rest.push(n); continue; }
      n.owner = owner === "results" ? (cols[4][0] && cols[4][0].id) : owner;
      if (owner === "description") head.push(n);
      else if (["on", "chain"].includes(owner)) cols[0].splice(cols[0].indexOf(nodes[owner]), 0, n);
      else if (["branches", "every"].includes(owner)) cols[0].splice(cols[0].indexOf(nodes[owner]), 0, n);
      else if (owner === "inputs") cols[2].push(n);
      else if (["prompt", "uses"].includes(owner)) cols[3].splice(cols[3].indexOf(nodes[owner]), 0, n);
      else if (["model", "budget", "research"].includes(owner)) subNotes.push(n);
      else if (owner === "results") cols[4].push(n);
      else rest.push(n);
    }
    // Edges: the main line, the instruction and the limits into the block, results out of it.
    const line = ["on", has("branches") ? "branches" : has("every") ? "every" : null, has("inputs") ? "inputs" : null, "uses"].filter(Boolean);
    for (let i = 0; i < line.length - 1; i++) edges.push({ from: line[i], to: line[i + 1] });
    if (nodes.body) edges.push({ from: "body", to: "uses" });
    if (nodes.prompt) edges.push({ from: "prompt", to: "uses" });
    for (const n of cols[4]) if (n.kind === "result") edges.push({ from: "uses", to: n.id, err: n.errors.length > 0 });
    for (const n of subs) if (n.kind === "limit") edges.push({ from: n.id, to: "uses", dash: true });
    if (nodes.chain) edges.push({ from: "chain", to: "on", dash: true });
    for (const n of Object.values(nodes)) if (n.kind === "text" && n.owner && nodes[n.owner]) edges.push({ from: n.id, to: n.owner, dash: true, note: true });
    // Problems that belong to no node: the file as a whole.
    const placed = new Set();
    for (const n of Object.values(nodes)) for (const p of n.errors) placed.add(p);
    const loose = errs.filter((p) => !placed.has(p)).concat(warns.filter((p) => !Object.values(nodes).some((n) => n.warnings.includes(p))));
    const names = ["trigger", "filter", "inputs", "task", "results"];
    return { nodes, head, cols: cols.map((c, i) => Object.assign(c, { name: names[i] })).filter((c) => c.length), subs, subNotes, rest, edges, loose };
  }

  // palette lists what can be added to a file: per item a label, the edit and
  // the node to open afterwards (or a pending node that needs text first).
  function palette(view, catalog) {
    const keys = keysOf(view);
    const has = (k) => Object.prototype.hasOwnProperty.call(keys, k);
    const ev = has("on") ? keys.on.scalar : "";
    const block = (catalog.blocks || []).find((b) => b.name === blockName(has("uses") && keys.uses.scalar));
    const fits = (o) => !ev || !o.events || !o.events.length || o.events.includes(ev);
    const more = [], inputs = [], results = [];
    if (!has("on")) more.push({ key: "on", edit: { op: "set", key: "on", value: "pr.merged" }, open: "on" });
    if (!has("uses")) {
      const first = (catalog.blocks || []).find(fits);
      if (first) more.push({ key: "uses", edit: { op: "set", key: "uses", value: first.name + (first.version ? "@" + first.version : "") }, open: "uses" });
    }
    if (ev === "pr.merged" && !has("branches")) more.push({ key: "branches", edit: { op: "set", key: "branches", list: ["main"] }, open: "branches" });
    if (ev === "schedule" && !has("every")) more.push({ key: "every", edit: { op: "set", key: "every", value: "60" }, open: "every" });
    if (!(view && view.body) && (!block || block.instruction)) more.push({ key: "body", pending: "body" });
    if (!has("model") && (!block || block.model)) more.push({ key: "model", edit: { op: "set", key: "model", value: catalog.default_alias || "mittel" }, open: "model" });
    if (!has("budget")) more.push({ key: "budget", edit: { op: "set", key: "budget", pairs: [{ key: "run_usd", value: "0.20" }] }, open: "budget" });
    if (!has("chain")) more.push({ key: "chain", edit: { op: "set", key: "chain", value: "true" }, open: "chain" });
    if (!has("description")) more.push({ key: "description", pending: "description" });
    const now = has("inputs") ? keys.inputs.list || [] : [];
    for (const i of catalog.inputs || []) {
      if (!fits(i) || now.includes(i.name)) continue;
      inputs.push({ key: "inputs", name: i.name, label: i.label, edit: { op: "set", key: "inputs", list: now.concat(i.name) }, open: "inputs" });
    }
    const pairs = has("results") ? keys.results.pairs || [] : [];
    for (const k of catalog.kinds || []) {
      if (block && block.results && !block.results.includes(k.name)) continue;
      if (pairs.some((p) => p.key === k.name)) continue;
      results.push({ key: "results", name: k.name, label: k.label, edit: { op: "set", key: "results", pairs: pairs.concat({ key: k.name, value: "1" }) }, open: "results:" + k.name });
    }
    return { more, inputs, results };
  }

  // ---- drawing -------------------------------------------------------------------------

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function svgIcon(paths) {
    if (!document.createElementNS) return el("span", "i");
    const s = document.createElementNS(SVGNS, "svg");
    s.setAttribute("viewBox", "0 0 16 16");
    s.setAttribute("class", "i");
    s.setAttribute("aria-hidden", "true");
    for (const d of paths) {
      const p = document.createElementNS(SVGNS, "path");
      p.setAttribute("d", d);
      s.appendChild(p);
    }
    return s;
  }
  const ICONS = {
    trigger: ["M9 1.8 3.5 9h4l-.6 5.2L12.5 7h-4z"],
    branches: ["M4.5 2v12", "M11.5 4.5c0 3-3 3.5-7 6"],
    every: ["M8 2.25a5.75 5.75 0 1 0 0 11.5 5.75 5.75 0 0 0 0-11.5", "M8 4.8V8l2.2 1.4"],
    input: ["M2.5 8h7", "m7 5 3 3-3 3", "M10 2.5h3.5v11H10"],
    task: ["M8 1.8 13.5 5v6L8 14.2 2.5 11V5z", "M2.5 5 8 8.2 13.5 5", "M8 8.2v6"],
    body: ["M3 2.5h10v11H3z", "M5.5 5.5h5", "M5.5 8h5", "M5.5 10.5h3"],
    result: ["M8 2.5v8", "m5 7.5 3 3 3-3", "M2.5 11.5v2h11v-2"],
    limit: ["M4 4h8v8H4z", "M6.5 2v2", "M9.5 2v2", "M6.5 12v2", "M9.5 12v2"],
    budget: ["M8 2.25a5.75 5.75 0 1 0 0 11.5 5.75 5.75 0 0 0 0-11.5", "M9.8 6.1c-.4-.6-1-.9-1.8-.9-1 0-1.8.6-1.8 1.4 0 1.9 3.6 1 3.6 2.9 0 .8-.8 1.4-1.8 1.4-.8 0-1.5-.3-1.9-.9", "M8 4.2v7.6"],
    text: ["m5.5 4.5-3.5 3.5 3.5 3.5", "m10.5 4.5 3.5 3.5-3.5 3.5"],
    description: ["M3 4h10", "M3 8h10", "M3 12h6"],
    plus: ["M8 3.2v9.6", "M3.2 8h9.6"],
    x: ["m4 4 8 8", "M12 4l-8 8"],
    tri: ["M8 2.2 14.2 13H1.8z", "M8 6.5v3", "M8 11.3v.2"],
    check: ["m3.5 8.5 3 3 6-7"],
  };
  function iconFor(n) {
    if (n.key === "branches") return ICONS.branches;
    if (n.key === "every") return ICONS.every;
    if (n.key === "budget") return ICONS.budget;
    if (n.key === "body" || n.key === "prompt") return ICONS.body;
    return ICONS[n.kind] || ICONS.task;
  }

  function mount(root) {
    if (root.dataset.wfvOn) return null;
    root.dataset.wfvOn = "1";
    const L = parseJSON(root.dataset.l10n, {});
    const catalog = parseJSON(root.dataset.catalog, {});
    const model = parseJSON(root.dataset.model, null);
    if (!model || !model.view) return null;
    const editable = root.hasAttribute("data-edit");
    const form = root.closest("form");
    const textarea = editable && form ? form.querySelector('textarea[name="text"]') : null;
    const state = { text: model.text, view: model.view, selected: "", pending: "", mode: "visual", busy: Promise.resolve(), paletteOpen: false };
    const label = (k) => L[k] || k;
    const optLabel = (list, name) => ((catalog[list] || []).find((o) => o.name === name) || {}).label || name;

    root.classList.add("wfv-on");
    if (editable) root.classList.add("box");
    // Head: the two views, the check, the stage, and on a phone the "+".
    const headEl = el("div", "box-h wfv-h");
    const tabs = el("div", "wfv-seg");
    tabs.setAttribute("role", "tablist");
    tabs.setAttribute("aria-label", label("view"));
    const tabV = el("button", "on", label("visual"));
    const tabT = el("button", "", label("text"));
    for (const [b, m] of [[tabV, "visual"], [tabT, "text"]]) {
      b.type = "button";
      b.setAttribute("role", "tab");
      b.dataset.mode = m;
      tabs.appendChild(b);
    }
    headEl.appendChild(tabs);
    headEl.appendChild(el("span", "sp"));
    const chk = el("span", "wfv-chk");
    const stage = el("span", "wfv-stage");
    headEl.appendChild(chk);
    headEl.appendChild(stage);
    let plusBtn = null;
    if (editable) {
      plusBtn = el("button", "gbtn sm wfv-plus");
      plusBtn.type = "button";
      plusBtn.setAttribute("aria-label", label("add"));
      plusBtn.setAttribute("aria-expanded", "false");
      plusBtn.appendChild(svgIcon(ICONS.plus));
      headEl.appendChild(plusBtn);
    }
    const fail = el("div", "wfv-fail");
    fail.setAttribute("role", "alert");
    fail.hidden = true;
    const pal = el("div", "wfv-pal");
    pal.setAttribute("aria-label", label("palette"));
    const stageEl = el("div", "wfv-stagebox");
    const canvas = el("div", "wfv-canvas");
    const graphEl = el("div", "wfv-graph");
    const loose = el("ul", "wfv-loose");
    const insp = el("aside", "wfv-insp");
    insp.hidden = true;
    const spacer = el("div", "wfv-spacer");
    canvas.appendChild(loose);
    canvas.appendChild(graphEl);
    stageEl.appendChild(canvas);
    stageEl.appendChild(insp);
    const textEls = Array.from(root.querySelectorAll(".wfv-text"));
    root.insertBefore(spacer, root.firstChild);
    root.insertBefore(stageEl, root.firstChild);
    if (editable) root.insertBefore(pal, root.firstChild);
    root.insertBefore(fail, root.firstChild);
    root.insertBefore(headEl, root.firstChild);

    function setMode(m) {
      state.mode = m;
      tabV.classList.toggle("on", m === "visual");
      tabT.classList.toggle("on", m === "text");
      tabV.setAttribute("aria-selected", String(m === "visual"));
      tabT.setAttribute("aria-selected", String(m === "text"));
      for (const t of textEls) t.hidden = m !== "text";
      stageEl.hidden = m !== "visual";
      pal.hidden = m !== "visual";
      chk.hidden = stage.hidden = m !== "visual";
      if (plusBtn) plusBtn.hidden = m !== "visual";
      if (m !== "visual") closeInspector();
    }
    tabs.addEventListener("click", (ev) => {
      const b = ev.target.closest && ev.target.closest("button[data-mode]");
      if (!b || b.dataset.mode === state.mode) return;
      if (b.dataset.mode === "visual" && textarea && textarea.value !== state.text) {
        state.text = textarea.value;
        send(null).then(() => setMode("visual"));
        return;
      }
      setMode(b.dataset.mode);
    });

    // ---- talking to the server ----
    function send(edit) {
      state.busy = state.busy.then(async () => {
        const body = new URLSearchParams();
        body.set("csrf_token", form ? (form.querySelector('input[name="csrf_token"]') || {}).value || "" : "");
        body.set("project", form ? (form.querySelector('input[name="project"]') || {}).value || "" : "");
        body.set("text", state.text);
        body.set("edit", edit ? JSON.stringify(edit) : "");
        let data = {}, ok = false;
        try {
          const res = await window.fetch("/ui/workflows/visual", { method: "POST", body, credentials: "same-origin", cache: "no-store",
            headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" } });
          ok = res.ok;
          data = await res.json().catch(() => ({}));
          if (!ok && !data.error) data.error = String(res.status);
        } catch (e) {
          data = { error: String((e && e.message) || e) };
        }
        if (!ok) {
          fail.textContent = fmt(label("failed"), data.error || "");
          fail.hidden = false;
          return false;
        }
        fail.hidden = true;
        state.text = data.text;
        state.view = data.view;
        if (textarea) textarea.value = data.text;
        render();
        return true;
      });
      return state.busy;
    }

    // ---- nodes ----
    function nodeBody(n) {
      const b = el("div", "wfn-b");
      const v = n.value || {};
      const add = (cls, text) => b.appendChild(el("span", cls, text));
      if (n.ghost) {
        add("wfn-ghost", n.key === "on" ? label("choose_on") : n.key === "uses" ? label("choose_uses") : "…");
        return b;
      }
      switch (n.key) {
        case "description": add("wfn-txt", v.scalar); break;
        case "on": add("wfn-main", optLabel("events", v.scalar)); add("wfn-sub mono", v.scalar); break;
        case "branches":
        case "inputs": {
          const ul = el("ul", "wfn-list");
          for (const it of v.list || []) {
            const li = el("li", n.key === "branches" ? "mono" : "", n.key === "inputs" ? optLabel("inputs", it) : it);
            li.title = it;
            if (n.errors.some((p) => p.item === it)) li.classList.add("err");
            ul.appendChild(li);
          }
          b.appendChild(ul);
          break;
        }
        case "every": add("wfn-main", fmt(label("every_n"), Number(v.scalar) || v.scalar)); break;
        case "chain": add("wfn-main", label("allowed")); break;
        case "research": add("wfn-main", label("research_on")); break;
        case "uses": add("wfn-main mono", shortName(v.scalar)); add("wfn-sub", optLabel("blocks", blockName(v.scalar))); break;
        case "body":
        case "prompt": add("wfn-txt clamp", v.scalar); break;
        case "model": add("wfn-main mono", v.scalar); break;
        case "budget":
          for (const p of v.pairs || []) add("wfn-main", fmt(label(p.key === "month_usd" ? "per_month" : "per_run"), money(p.value)));
          break;
        case "results": add("wfn-main", fmt(label("max"), Number(v.value) || v.value)); break;
        default: if (n.kind === "text") { const pre = el("pre", "wfn-pre"); pre.textContent = n.block.text; b.appendChild(pre); }
      }
      for (const p of n.errors.concat(n.warnings)) {
        const e = el("div", n.errors.includes(p) ? "wferr" : "wfwarn");
        e.appendChild(svgIcon(ICONS.tri));
        e.appendChild(el("span", "", p.message));
        b.appendChild(e);
      }
      return b;
    }
    function title(n) {
      if (n.kind === "result") return optLabel("kinds", n.item) || label("node.results");
      if (n.kind === "text") return label("node.text");
      return label("node." + n.key);
    }
    function drawNode(n) {
      const d = el("div", "wfn k-" + n.kind + (n.sub ? " sub" : "") + (n.ghost ? " ghost" : "") + (n.errors.length ? " err" : "") + (n.id === state.selected ? " sel" : ""));
      d.dataset.node = n.id;
      if (editable) {
        d.setAttribute("role", "button");
        d.tabIndex = 0;
        d.setAttribute("aria-pressed", String(n.id === state.selected));
      }
      const h = el("div", "wfn-h");
      h.appendChild(svgIcon(iconFor(n)));
      h.appendChild(el("span", "", title(n)));
      if (n.line && n.kind === "text") h.appendChild(el("span", "wfn-ln", fmt(label("line"), n.line)));
      d.appendChild(h);
      d.appendChild(nodeBody(n));
      return d;
    }

    let G = null;
    function render() {
      G = graph(state.view, catalog, state.pending);
      if (state.selected && !G.nodes[state.selected]) state.selected = "";
      const errs = (state.view.errors || []).length;
      chk.className = "wfv-chk " + (errs ? "bad" : "ok");
      chk.textContent = errs ? (errs === 1 ? label("errors_one") : fmt(label("errors"), errs)) : label("checked");
      stage.textContent = fmt(label("stage"), state.view.stage);
      loose.textContent = "";
      for (const p of G.loose) {
        const li = el("li", (state.view.errors || []).includes(p) ? "wferr" : "wfwarn");
        li.appendChild(svgIcon(ICONS.tri));
        li.appendChild(el("span", "", (p.line ? label("file") + ", " + fmt(label("line"), p.line) + ": " : "") + p.message));
        loose.appendChild(li);
      }
      loose.hidden = !G.loose.length;
      graphEl.textContent = "";
      if (G.head.length) {
        const row = el("div", "wfv-head");
        for (const n of G.head) row.appendChild(drawNode(n));
        graphEl.appendChild(row);
      }
      const flow = el("div", "wfv-flow");
      for (const col of G.cols) {
        const c = el("div", "wfv-col c-" + col.name);
        for (const n of col) {
          c.appendChild(drawNode(n));
          if (n.id === "uses" && G.subs.length) {
            const s = el("div", "wfv-subs");
            for (const m of G.subs) s.appendChild(drawNode(m));
            c.appendChild(s);
            for (const m of G.subNotes) c.appendChild(drawNode(m));
          }
        }
        flow.appendChild(c);
      }
      graphEl.appendChild(flow);
      if (G.rest.length) {
        const row = el("div", "wfv-rest");
        for (const n of G.rest) row.appendChild(drawNode(n));
        graphEl.appendChild(row);
      }
      if (editable) renderPalette();
      renderInspector();
      edges();
    }

    // Edges are measured from the laid-out nodes: beside each other they run
    // from right to left, one above the other from bottom to top.
    function edges() {
      const old = graphEl.querySelector("svg.wfv-edges");
      if (old) old.remove();
      if (!G || !document.createElementNS || !graphEl.getBoundingClientRect) return;
      const svg = document.createElementNS(SVGNS, "svg");
      svg.setAttribute("class", "wfv-edges");
      svg.setAttribute("aria-hidden", "true");
      const base = graphEl.getBoundingClientRect();
      const rect = (id) => {
        const e = graphEl.querySelector('[data-node="' + id + '"]');
        if (!e) return null;
        const r = e.getBoundingClientRect();
        return { x: r.left - base.left, y: r.top - base.top, w: r.width, h: r.height };
      };
      // One below the other (a phone), the nodes of the flow are joined in their
      // order, so that no edge runs through the nodes between its ends.
      const flow = graphEl.querySelector(".wfv-flow");
      const vertical = flow && window.getComputedStyle && window.getComputedStyle(flow).flexDirection === "column";
      let list = G.edges;
      if (vertical) {
        const ids = Array.from(flow.querySelectorAll("[data-node]")).map((e) => e.dataset.node);
        list = [];
        for (let i = 0; i < ids.length - 1; i++) {
          const a = G.nodes[ids[i]], b = G.nodes[ids[i + 1]];
          list.push({ from: a.id, to: b.id, dash: !!(a.sub || b.sub || a.kind === "text" || b.kind === "text"), err: b.errors.length > 0 && b.kind === "result" });
        }
      }
      for (const ed of list) {
        const a = rect(ed.from), b = rect(ed.to);
        if (!a || !b) continue;
        let d;
        if (b.x >= a.x + a.w - 2) {
          const x1 = a.x + a.w, y1 = a.y + Math.min(a.h / 2, 26), x2 = b.x, y2 = b.y + Math.min(b.h / 2, 26), m = Math.max(24, (x2 - x1) / 2);
          d = `M${x1},${y1} C${x1 + m},${y1} ${x2 - m},${y2} ${x2},${y2}`;
        } else if (b.y >= a.y + a.h - 2) {
          const x1 = a.x + Math.min(a.w / 2, 70), y1 = a.y + a.h, x2 = b.x + Math.min(b.w / 2, 70), y2 = b.y, m = Math.max(12, (y2 - y1) / 2);
          d = `M${x1},${y1} C${x1},${y1 + m} ${x2},${y2 - m} ${x2},${y2}`;
        } else if (a.y >= b.y + b.h - 2) {
          const x1 = a.x + Math.min(a.w / 2, 70), y1 = a.y, x2 = b.x + Math.min(b.w / 2, 70), y2 = b.y + b.h, m = Math.max(12, (y1 - y2) / 2);
          d = `M${x1},${y1} C${x1},${y1 - m} ${x2},${y2 + m} ${x2},${y2}`;
        } else {
          continue;
        }
        const p = document.createElementNS(SVGNS, "path");
        p.setAttribute("d", d);
        p.setAttribute("class", [ed.dash ? "dash" : "", ed.err ? "err" : "", ed.note ? "note" : ""].join(" ").trim());
        svg.appendChild(p);
      }
      graphEl.insertBefore(svg, graphEl.firstChild);
    }
    if (window.ResizeObserver) new window.ResizeObserver(() => edges()).observe(graphEl);
    else window.addEventListener("resize", edges);

    // ---- palette ----
    function chip(item, text, sub) {
      const b = el("button", "wfv-chip k-" + (item.key === "inputs" ? "input" : item.key === "results" ? "result" : "more"));
      b.type = "button";
      b.appendChild(svgIcon(ICONS.plus));
      b.appendChild(el("span", "", text));
      if (sub) b.title = sub;
      b.addEventListener("click", () => addItem(item));
      return b;
    }
    function renderPalette() {
      const P = palette(state.view, catalog);
      pal.textContent = "";
      const group = (name, items, text) => {
        if (!items.length) return;
        const g = el("div", "wfv-pg");
        g.appendChild(el("span", "wfv-pgh", label("group." + name)));
        for (const it of items) g.appendChild(chip(it, text(it), it.name));
        pal.appendChild(g);
      };
      group("more", P.more, (it) => label("node." + it.key));
      group("inputs", P.inputs, (it) => it.label);
      group("results", P.results, (it) => it.label);
      pal.classList.toggle("open", state.paletteOpen);
      if (plusBtn) plusBtn.setAttribute("aria-expanded", String(state.paletteOpen));
    }
    function addItem(item) {
      state.paletteOpen = false;
      if (item.pending) {
        state.pending = item.pending;
        state.selected = item.pending;
        render();
        return;
      }
      send(item.edit).then((ok) => {
        if (ok && item.open) select(item.open);
      });
    }
    if (plusBtn) plusBtn.addEventListener("click", () => {
      state.paletteOpen = !state.paletteOpen;
      if (state.paletteOpen) closeInspector();
      renderPalette();
    });

    // ---- inspector ----
    function select(id) {
      state.selected = id;
      state.paletteOpen = false;
      render();
      const n = graphEl.querySelector('[data-node="' + id + '"]');
      // On a phone the sheet covers the lower part: the node moves above it.
      if (n && n.getBoundingClientRect && window.matchMedia && window.matchMedia("(max-width: 767px)").matches) {
        const sheet = insp.getBoundingClientRect ? insp.getBoundingClientRect().height : 0;
        if (spacer.style) spacer.style.height = sheet + "px";
        const r = n.getBoundingClientRect();
        const room = window.innerHeight - sheet - 12;
        if (r.bottom > room || r.top < 8) window.scrollBy({ top: r.top - Math.max(8, room - r.height), behavior: "smooth" });
      }
    }
    function closeInspector() {
      state.selected = "";
      if (state.pending) state.pending = "";
      insp.hidden = true;
      if (spacer.style) spacer.style.height = "0px";
      for (const e of graphEl.querySelectorAll(".wfn.sel")) e.classList.remove("sel");
    }
    graphEl.addEventListener("click", (ev) => {
      if (!editable) return;
      const n = ev.target.closest && ev.target.closest("[data-node]");
      if (n) select(n.dataset.node);
    });
    graphEl.addEventListener("keydown", (ev) => {
      if (!editable || (ev.key !== "Enter" && ev.key !== " ")) return;
      const n = ev.target.closest && ev.target.closest("[data-node]");
      if (n) { ev.preventDefault(); select(n.dataset.node); }
    });

    function field(labelText, input) {
      const f = el("label", "wfv-f");
      f.appendChild(el("span", "", labelText));
      f.appendChild(input);
      return f;
    }
    function input(type, value, cls) {
      const i = el("input", "gfield " + (cls || ""));
      i.type = type;
      i.value = value == null ? "" : String(value);
      i.autocomplete = "off";
      return i;
    }
    function choices(name, opts, current, onPick) {
      const box = el("div", "wfv-choices");
      box.setAttribute("role", "radiogroup");
      for (const o of opts) {
        const l = el("label", "wfv-choice" + (o.name === current ? " on" : "") + (o.off ? " off" : ""));
        const r = el("input");
        r.type = "radio";
        r.name = name;
        r.value = o.name;
        r.checked = o.name === current;
        r.addEventListener("change", () => onPick(o.name));
        l.appendChild(r);
        const t = el("span", "wfv-ct");
        t.appendChild(el("span", "wfv-cl", o.label || o.name));
        if (o.sub) t.appendChild(el("span", "wfv-cs mono", o.sub));
        l.appendChild(t);
        box.appendChild(l);
      }
      return box;
    }
    function checks(opts, current, onChange) {
      const box = el("div", "wfv-choices");
      for (const o of opts) {
        const l = el("label", "wfv-choice" + (current.includes(o.name) ? " on" : ""));
        const c = el("input");
        c.type = "checkbox";
        c.value = o.name;
        c.checked = current.includes(o.name);
        c.addEventListener("change", () => {
          const picked = Array.from(box.querySelectorAll("input")).filter((x) => x.checked).map((x) => x.value);
          // Keep the order of the file, new ones at the end.
          onChange(current.filter((x) => picked.includes(x)).concat(picked.filter((x) => !current.includes(x))));
        });
        l.appendChild(c);
        const t = el("span", "wfv-ct");
        t.appendChild(el("span", "wfv-cl", o.label || o.name));
        t.appendChild(el("span", "wfv-cs mono", o.name));
        l.appendChild(t);
        box.appendChild(l);
      }
      return box;
    }

    function renderInspector() {
      const n = G && state.selected ? G.nodes[state.selected] : null;
      if (!editable || !n) { insp.hidden = true; if (spacer.style) spacer.style.height = "0px"; return; }
      insp.hidden = false;
      insp.textContent = "";
      insp.setAttribute("aria-label", title(n));
      const h = el("div", "wfv-ih");
      h.appendChild(svgIcon(iconFor(n)));
      h.appendChild(el("b", "", title(n)));
      if (n.line) h.appendChild(el("span", "wfv-iln", fmt(label("line"), n.line)));
      h.appendChild(el("span", "sp"));
      const x = el("button", "wfv-ix");
      x.type = "button";
      x.setAttribute("aria-label", label("close"));
      x.appendChild(svgIcon(ICONS.x));
      x.addEventListener("click", closeInspector);
      h.appendChild(x);
      insp.appendChild(h);
      const b = el("div", "wfv-ib");
      for (const p of n.errors.concat(n.warnings)) {
        const e = el("div", n.errors.includes(p) ? "wferr" : "wfwarn");
        e.appendChild(svgIcon(ICONS.tri));
        e.appendChild(el("span", "", p.message));
        b.appendChild(e);
      }
      const keys = keysOf(state.view);
      const v = n.value || {};
      const ev = keys.on ? keys.on.scalar : "";
      const fits = (o) => !ev || !o.events || !o.events.length || o.events.includes(ev);
      const set = (key, value) => send({ op: "set", key, value: String(value) });
      switch (n.key) {
        case "description": {
          const i = input("text", v.scalar);
          i.maxLength = 300;
          i.addEventListener("change", () => (i.value.trim() ? set("description", i.value) : send({ op: "remove", key: "description" })).then(() => { state.pending = ""; }));
          b.appendChild(field(label("node.description"), i));
          break;
        }
        case "on":
          b.appendChild(choices("wfv-on", (catalog.events || []).map((o) => ({ name: o.name, label: o.label, sub: o.name })), v.scalar, (name) => set("on", name)));
          break;
        case "branches": {
          const list = (v.list || []).slice();
          const box = el("div", "wfv-rows");
          const commit = () => send({ op: "set", key: "branches", list: Array.from(box.querySelectorAll("input")).map((i) => i.value.trim()).filter(Boolean) });
          const row = (val) => {
            const r = el("div", "wfv-row");
            const i = input("text", val, "mono");
            i.setAttribute("aria-label", label("branch"));
            i.addEventListener("change", commit);
            const rm = el("button", "wfv-ix");
            rm.type = "button";
            rm.setAttribute("aria-label", label("remove"));
            rm.appendChild(svgIcon(ICONS.x));
            rm.addEventListener("click", () => { r.remove(); commit(); });
            r.appendChild(i);
            r.appendChild(rm);
            box.appendChild(r);
            return i;
          };
          for (const it of list) row(it);
          b.appendChild(box);
          const more = el("button", "gbtn sm");
          more.type = "button";
          more.appendChild(svgIcon(ICONS.plus));
          more.appendChild(el("span", "", label("branch")));
          more.addEventListener("click", () => row("").focus());
          b.appendChild(more);
          break;
        }
        case "every": {
          const i = input("number", v.scalar);
          i.min = "5";
          i.max = "10080";
          i.addEventListener("change", () => set("every", i.value));
          b.appendChild(field(label("minutes"), i));
          break;
        }
        case "chain":
        case "research": {
          const l = el("label", "wfv-choice on");
          const c = el("input");
          c.type = "checkbox";
          c.checked = true;
          c.addEventListener("change", () => (c.checked ? set(n.key, "true") : send({ op: "remove", key: n.key })));
          l.appendChild(c);
          l.appendChild(el("span", "wfv-cl", n.key === "chain" ? label("allowed") : label("research_on")));
          b.appendChild(l);
          break;
        }
        case "inputs": {
          const cur = v.list || [];
          const opts = (catalog.inputs || []).filter((o) => fits(o) || cur.includes(o.name));
          for (const it of cur) if (!opts.some((o) => o.name === it)) opts.push({ name: it, label: label("not_catalog") });
          b.appendChild(checks(opts, cur, (list) => send({ op: "set", key: "inputs", list })));
          break;
        }
        case "uses": {
          const cur = blockName(v.scalar);
          const opts = (catalog.blocks || []).map((o) => ({ name: o.name, label: o.label, sub: shortName(o.name), off: !fits(o), version: o.version }));
          b.appendChild(choices("wfv-uses", opts, cur, (name) => {
            const o = opts.find((x) => x.name === name);
            const keepVersion = String(v.scalar || "").includes("@") || !v.scalar;
            set("uses", name + (keepVersion && o && o.version ? "@" + o.version : ""));
          }));
          break;
        }
        case "body": {
          const t = el("textarea", "gfield wfv-ta");
          t.value = v.scalar || "";
          t.rows = 6;
          t.addEventListener("change", () => send({ op: "body", text: t.value }).then(() => { state.pending = ""; }));
          b.appendChild(field(label("node.body"), t));
          break;
        }
        case "prompt": {
          const i = input("text", v.scalar);
          i.addEventListener("change", () => set("prompt", i.value));
          b.appendChild(field(label("node.prompt"), i));
          break;
        }
        case "model": {
          const names = (catalog.aliases || []).slice();
          if (v.scalar && !names.includes(v.scalar)) names.push(v.scalar);
          if (names.length) b.appendChild(choices("wfv-model", names.map((a) => ({ name: a, label: a })), v.scalar, (name) => set("model", name)));
          const i = input("text", v.scalar, "mono");
          i.addEventListener("change", () => i.value.trim() && set("model", i.value));
          b.appendChild(field(label("alias"), i));
          break;
        }
        case "budget": {
          const get = (k) => ((v.pairs || []).find((p) => p.key === k) || {}).value || "";
          const run = input("number", get("run_usd"));
          const month = input("number", get("month_usd"));
          for (const i of [run, month]) {
            i.step = "0.01";
            i.min = "0.01";
            i.addEventListener("change", () => {
              const pairs = [];
              if (run.value.trim()) pairs.push({ key: "run_usd", value: run.value.trim() });
              if (month.value.trim()) pairs.push({ key: "month_usd", value: month.value.trim() });
              send(pairs.length ? { op: "set", key: "budget", pairs } : { op: "remove", key: "budget" });
            });
          }
          b.appendChild(field(label("run_usd"), run));
          b.appendChild(field(label("month_usd"), month));
          break;
        }
        case "results": {
          const i = input("number", v.value);
          i.min = "1";
          i.addEventListener("change", () => {
            const pairs = ((keys.results && keys.results.pairs) || []).map((p) => (p.key === n.item ? { key: p.key, value: i.value.trim() } : p));
            send({ op: "set", key: "results", pairs });
          });
          b.appendChild(field(fmt(label("max"), "").replace(/\s*$/, ""), i));
          break;
        }
        default:
          if (n.kind === "text") {
            const t = el("textarea", "gfield mono wfv-ta");
            t.value = n.block.text;
            t.rows = Math.min(10, n.block.text.split("\n").length + 1);
            t.addEventListener("change", () => send({ op: "block", block: n.block.id, old: n.block.text, text: t.value }));
            b.appendChild(t);
          }
      }
      if (!n.ghost) {
        const rm = el("button", "gbtn sm wfv-rm");
        rm.type = "button";
        rm.textContent = label("remove");
        rm.addEventListener("click", () => {
          let e;
          if (n.kind === "text") e = { op: "block", block: n.block.id, old: n.block.text, text: "" };
          else if (n.key === "body") e = { op: "body", text: "" };
          else if (n.key === "results" && n.item) e = { op: "set", key: "results", pairs: ((keys.results && keys.results.pairs) || []).filter((p) => p.key !== n.item) };
          else e = { op: "remove", key: n.key };
          closeInspector();
          send(e);
        });
        b.appendChild(rm);
      }
      insp.appendChild(b);
    }
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape" && (state.selected || state.paletteOpen)) {
        state.paletteOpen = false;
        closeInspector();
        if (editable) renderPalette();
      }
    });

    setMode("visual");
    render();
    return { state, send, select, render };
  }

  window.GTWfv = { graph, palette, mount, SCALARS };
  for (const r of document.querySelectorAll("[data-wfv]")) mount(r);
})();
