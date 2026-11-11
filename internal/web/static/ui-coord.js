// A narrow screen with a single room has nothing to pick: go straight in.
(() => {
  const welcome = document.querySelector("[data-sole-room]");
  if (welcome && welcome.dataset.soleRoom && matchMedia("(max-width: 720px)").matches) {
    location.replace(welcome.dataset.soleRoom);
  }
})();
(() => {
  const workspace = document.querySelector(".coord-workspace");
  if (!workspace) return;

  // Texts for status lines come from the server catalog, not from this file.
  let texts = {};
  try {
    texts = JSON.parse(workspace.dataset.coordTexts || "{}");
  } catch (_) {
    texts = {};
  }
  const text = (key) => texts[key] || "";
  const media = matchMedia("(max-width: 1100px)");
  const backdrop = document.querySelector("[data-coord-drawer-close]");
  document.documentElement.classList.add("coord-enhanced");
  let openPanel = null;
  let returnFocus = null;
  let outsideState = [];

  const toggles =
    () => [...document.querySelectorAll("[data-coord-drawer-target]")];
  const setOutsideInert = (panel) => {
    outsideState = [];
    let branch = panel;
    while (branch && branch !== document.body) {
      const parent = branch.parentElement;
      if (!parent) break;
      for (const sibling of parent.children) {
        if (
          sibling === branch || sibling === backdrop ||
          sibling.tagName === "SCRIPT"
        ) continue;
        outsideState.push([sibling, sibling.inert]);
        sibling.inert = true;
      }
      branch = parent;
    }
  };
  const restoreOutside = () => {
    for (const [element, wasInert] of outsideState) element.inert = wasInert;
    outsideState = [];
  };
  const close = () => {
    if (!openPanel) return;
    openPanel.hidden = true;
    openPanel.inert = true;
    restoreOutside();
    if (backdrop) backdrop.hidden = true;
    document.body.classList.remove("coord-drawer-open");
    toggles().forEach((button) =>
      button.setAttribute("aria-expanded", "false")
    );
    const focus = returnFocus;
    openPanel = null;
    returnFocus = null;
    if (focus && focus.isConnected) focus.focus();
  };
  // The context column (threads, who is here) is never taken away: below the
  // chat on a narrow screen, beside it on a wide one.
  const sync = () => {
    close();
  };
  const open = (button) => {
    close();
    const panel = document.getElementById(button.dataset.coordDrawerTarget);
    if (!panel || !media.matches || !backdrop) return;
    returnFocus = button;
    openPanel = panel;
    panel.hidden = false;
    panel.inert = false;
    setOutsideInert(panel);
    backdrop.hidden = false;
    document.body.classList.add("coord-drawer-open");
    button.setAttribute("aria-expanded", "true");
    (panel.querySelector("a,button,input,select,textarea,summary") || panel)
      .focus();
  };
  // Choosing a room in the rooms drawer is a decision to go there: the drawer
  // steps aside. Live refreshes (no history entry, same room) leave it alone.
  const shouldCloseRoomsDrawer = (openPanelId, history, oldRoom, newRoom) =>
    openPanelId === "coord-rooms" && (history === "push" || oldRoom !== newRoom);
  const bindDrawerCloseControls = () => {
    document.querySelectorAll("[data-coord-drawer-close]").forEach((control) => {
      if (control.dataset.coordDrawerCloseBound) return;
      control.dataset.coordDrawerCloseBound = "true";
      control.addEventListener("click", close);
    });
  };
  const bindDrawers = () => {
    toggles().forEach((button) => {
      if (button.dataset.coordDrawerBound) return;
      button.dataset.coordDrawerBound = "true";
      button.addEventListener("click", () => open(button));
    });
    bindDrawerCloseControls();
  };
  const openSelectedThread = () => {
    if (!media.matches || !document.querySelector(".coord-thread-detail")) return;
    const contextButton = toggles().find(
      (button) => button.dataset.coordDrawerTarget === "coord-context",
    );
    if (contextButton) open(contextButton);
  };
  bindDrawers();
  // The chat is a scroll area of its own with the composer fixed beneath it. It
  // opens at the newest message (or at the one a link names).
  const chatOf = () => document.querySelector(".coord-messages");
  const scrollFlowToEnd = () => {
    const flow = chatOf();
    const target = location.hash && document.getElementById(location.hash.slice(1));
    if (target && flow && flow.contains(target)) target.scrollIntoView({block: "center"});
    else if (flow) flow.scrollTop = flow.scrollHeight;
  };
  // The page itself does not scroll: the shell gets the height of what is left of the window.
  const fitShell = () => {
    const shell = document.querySelector(".coord-shell");
    if (!shell) return;
    // Everything on the page that is not the shell: what lies above it and what follows (footer, margins).
    const box = shell.getBoundingClientRect();
    const above = box.top + window.scrollY;
    let below = 0;
    for (let node = shell; node && node !== document.body; node = node.parentElement) {
      for (let next = node.nextElementSibling; next; next = next.nextElementSibling) {
        const style = getComputedStyle(next);
        if (/^(SCRIPT|STYLE|LINK)$/.test(next.tagName) || /^(fixed|absolute)$/.test(style.position)) continue;
        below += next.offsetHeight + parseFloat(style.marginTop) + parseFloat(style.marginBottom);
      }
      const parent = getComputedStyle(node.parentElement || node);
      below += parseFloat(parent.paddingBottom) + parseFloat(parent.borderBottomWidth) + parseFloat(getComputedStyle(node).marginBottom);
    }
    document.documentElement.style.setProperty("--coord-top", `${Math.max(0, Math.round(above + below))}px`);
  };
  fitShell();
  addEventListener("resize", fitShell);
  scrollFlowToEnd();
  const newPill = () => document.querySelector("[data-chat-new]");
  const nearEnd = (flow) => flow.scrollHeight - flow.scrollTop - flow.clientHeight < 80;
  const lastSeq = (flow) => {
    const items = flow ? flow.querySelectorAll(":scope > li[data-seq]") : [];
    return items.length ? Number(items[items.length - 1].dataset.seq) : 0;
  };
  const syncFlowFade = (flow) => {
    flow.classList.toggle("is-scrolled", flow.scrollTop > 2);
    const pill = newPill();
    if (pill && nearEnd(flow)) pill.hidden = true;
  };
  document.addEventListener("scroll", (event) => {
    if (event.target instanceof Element && event.target.matches(".coord-messages")) syncFlowFade(event.target);
  }, true);
  document.addEventListener("click", (event) => {
    const pill = event.target.closest?.("[data-chat-new]");
    if (!pill) return;
    const flow = chatOf();
    if (flow) flow.scrollTo({top: flow.scrollHeight, behavior: "smooth"});
    pill.hidden = true;
  });
  // The divider "New" stays where it was drawn while the room is open: the
  // read mark moves on as soon as the messages are seen, the divider does not.
  let unreadFrom = 0;
  let dividerNode = null;
  const rememberDivider = () => {
    const flow = chatOf();
    unreadFrom = Number(flow?.dataset.unreadFrom || 0);
    dividerNode = flow?.querySelector(":scope > .cm-new")?.cloneNode(true) || null;
  };
  const applyDivider = () => {
    const flow = chatOf();
    if (!flow || !unreadFrom || !dividerNode || flow.querySelector(":scope > .cm-new")) return;
    const first = [...flow.querySelectorAll(":scope > li[data-seq]")].find((item) => Number(item.dataset.seq) >= unreadFrom);
    if (first) flow.insertBefore(dividerNode.cloneNode(true), first);
  };
  rememberDivider();
  const initialFlow = chatOf();
  if (initialFlow) syncFlowFade(initialFlow);
  addEventListener("keydown", (event) => {
    if (event.key === "Escape") close();
  });
  media.addEventListener("change", sync);
  sync();
  openSelectedThread();

  const createCoordApplyCoordinator = () => {
    let generation = 0;
    let active = null;
    const idleListeners = new Set();
    return {
      begin(kind) {
        if (kind === "refresh" && active && active.kind !== "refresh") {
          return null;
        }
        generation += 1;
        if (active) active.controller.abort();
        active = {kind, generation, controller: new AbortController()};
        return active;
      },
      current(lease) {
        return active === lease && generation === lease.generation &&
          !lease.controller.signal.aborted;
      },
      finish(lease) {
        if (active !== lease) return;
        active = null;
        idleListeners.forEach((listener) => listener());
      },
      onIdle(listener) {
        idleListeners.add(listener);
        return () => idleListeners.delete(listener);
      },
    };
  };
  const progressiveFormPaths = new Set([
    "/ui/coord/send",
    "/ui/coord/thread/post",
    "/ui/coord/read",
    "/ui/coord/unread",
    "/ui/coord/attention/action",
    "/ui/coord/standing/create",
    "/ui/coord/standing/end",
    "/ui/coord/direct/start",
    "/ui/coord/group/create",
    "/ui/coord/group/update",
    "/ui/coord/group/leave",
    "/ui/coord/thread/create",
    "/ui/coord/thread/state",
  ]);
  const coordPageTarget = (anchor, origin) => {
    if (
      !anchor || anchor.target || anchor.download ||
      anchor.hasAttribute?.("download")
    ) return null;
    const target = new URL(anchor.href, origin);
    return target.origin === origin && target.pathname === "/ui/coord"
      ? target
      : null;
  };
  const coordFormTarget = (form, origin, submitter) => {
    if (!form || form.method.toLowerCase() !== "post") return null;
    // A submit button may post elsewhere (the directive button does).
    const action = submitter?.hasAttribute("formaction") ? submitter.formAction : form.action;
    const target = new URL(action, origin);
    return target.origin === origin && progressiveFormPaths.has(target.pathname)
      ? target
      : null;
  };
  // Minutes east of UTC for the picked moment, so DST of the target date counts.
  const coordExpiryOffset = (value) => {
    const moment = new Date(value);
    return value && !Number.isNaN(moment.getTime()) ? String(-moment.getTimezoneOffset()) : "";
  };
  const stampCoordExpiryOffset = (form) => {
    const field = form.querySelector('[name="expires_at"]');
    const offset = form.querySelector('[name="expires_offset"]');
    if (field && offset) offset.value = coordExpiryOffset(field.value);
  };
  const coordFormBody = (form, submitter) => {
    const data = new FormData(form);
    if (submitter?.name) data.append(submitter.name, submitter.value);
    const body = new URLSearchParams();
    for (const [name, value] of data.entries()) {
      if (typeof value !== "string") return null;
      body.append(name, value);
    }
    return body;
  };
  const coordPageLoadApplied = "applied";
  const coordPageLoadFailed = "failed";
  const coordPageLoadForbidden = "forbidden";
  const coordPageLoadOffline = "offline";
  const coordPageLoadRedirected = "redirected";
  const coordPageLoadSuperseded = "superseded";
  const coordFailedPopstateTarget = (result, currentURL) => {
    if (
      result === coordPageLoadApplied ||
      result === coordPageLoadRedirected ||
      result === coordPageLoadSuperseded
    ) {
      return "";
    }
    return result === coordPageLoadForbidden ? "/ui/coord" : currentURL;
  };
  const createCoordRefreshRetry = (schedule) => {
    const delays = [250, 1000, 3000];
    let failures = 0;
    return {
      failure() {
        const delay = delays[failures];
        if (delay === undefined) return false;
        failures += 1;
        schedule(delay);
        return true;
      },
      success() {
        failures = 0;
      },
    };
  };

  const liveStatus = document.querySelector("[data-coord-live-status]");
  const announce = document.getElementById("coord-status");
  // The message lives inside the conversation, which is swapped: look it up each time.
  const actionStatusEl = () => document.querySelector("[data-coord-action-status]");
  const setLive = (state, label, announcement = "") => {
    document.documentElement.dataset.coordLive = state;
    if (liveStatus) liveStatus.textContent = label;
    if (announcement && announce) announce.textContent = announcement;
  };
  let actionTimer = null;
  const setActionStatus = (message, error = false) => {
    const actionStatus = actionStatusEl();
    if (actionStatus) {
      actionStatus.textContent = message;
      actionStatus.hidden = !message;
      actionStatus.dataset.error = error ? "true" : "false";
      clearTimeout(actionTimer);
      // Progress and confirmations fade; errors stay until the next action.
      if (message && !error) {
        actionTimer = setTimeout(() => { const el = actionStatusEl(); if (el) el.hidden = true; }, 3000);
      }
    }
    if (message && announce) announce.textContent = message;
  };

  // A page swap is not news worth a banner: clear the "loading" line and
  // leave the confirmation to screen readers.
  const quietSuccess = (message) => {
    setActionStatus("");
    if (announce) announce.textContent = message;
  };

  const pageParams = () => new URLSearchParams(location.search);
  const currentRoom = () => pageParams().get("room") || "";
  const currentThread = () => pageParams().get("thread") || "";
  const incomingRoomOf = (target) => new URL(target.href).searchParams.get("room") || "";
  const threadKey = () => `${currentRoom()}#${currentThread()}`;
  const anchorState = () => {
    const params = pageParams();
    const roomAround = params.get("around");
    const threadAround = params.get("thread_around");
    const id = threadAround
      ? `thread-message-${threadAround}`
      : (roomAround ? `message-${roomAround}` : "");
    const element = id ? document.getElementById(id) : null;
    const messages = document.querySelector(".coord-messages");
    const context = document.getElementById("coord-context");
    const container = threadAround ? context : messages;
    return {
      id,
      threadKey: threadKey(),
      top: element && container
        ? element.getBoundingClientRect().top -
          container.getBoundingClientRect().top
        : null,
      container,
      messages,
      messageScrollTop: messages ? messages.scrollTop : 0,
      messageNearBottom: messages
        ? messages.scrollHeight - messages.scrollTop - messages.clientHeight <
          80
        : false,
      context,
      contextScrollTop: context ? context.scrollTop : 0,
    };
  };
  const restoreAnchor = (state) => {
    if (state.id && state.top !== null && state.container) {
      const element = document.getElementById(state.id);
      if (element) {
        const nextTop = element.getBoundingClientRect().top -
          state.container.getBoundingClientRect().top;
        state.container.scrollTop += nextTop - state.top;
      }
    }
    if (state.messages && state.container !== state.messages) {
      state.messages.scrollTop = state.messageNearBottom
        ? state.messages.scrollHeight
        : state.messageScrollTop;
    }
    if (state.context && state.container !== state.context) {
      state.context.scrollTop = state.contextScrollTop;
    }
    if (!state.id && state.messages) {
      state.messages.scrollTop = state.messageNearBottom
        ? state.messages.scrollHeight
        : state.messageScrollTop;
    }
    if (!state.id && state.context) {
      state.context.scrollTop = state.contextScrollTop;
    }
  };
  const replaceSurface = (selector, incomingDocument) => {
    const current = document.querySelector(selector);
    const incoming = incomingDocument.querySelector(selector);
    if (!current || !incoming) return;
    // Copies: one element may be named by two surfaces (the messages of a thread
    // are both), and the nodes of the incoming page can only move once.
    current.replaceChildren(...[...incoming.childNodes].map((node) => node.cloneNode(true)));
  };
  const focusIdentity = (element) => ({
    key: element?.dataset?.coordFocusKey || "",
    tag: element?.tagName || "",
    id: element?.id || "",
    name: element?.getAttribute?.("name") || "",
    href: element?.getAttribute?.("href") || "",
    text: element?.textContent || "",
  });
  const sameFocusIdentity = (state, element) => {
    if (state.key) return element?.dataset?.coordFocusKey === state.key;
    return (state.id && element.id === state.id) ||
      (state.href && element.getAttribute("href") === state.href) ||
      (state.name && element.getAttribute("name") === state.name &&
        element.textContent === state.text) ||
      (!state.id && !state.href && !state.name && state.text &&
        element.textContent === state.text);
  };
  const captureFocus = () => {
    const element = document.activeElement;
    if (!element || element === document.body) return null;
    return focusIdentity(element);
  };
  const restoreFocus = (state) => {
    if (
      !state ||
      (document.activeElement && document.activeElement !== document.body)
    ) return;
    const candidates = state.key
      ? [...document.querySelectorAll("[data-coord-focus-key]")]
      : [...document.querySelectorAll(state.tag.toLowerCase())];
    const match = candidates.find((element) => sameFocusIdentity(state, element));
    if (match) match.focus();
  };
  const draftControlSelector =
    "input:not([type='hidden']),select,textarea,button,summary";
  const draftControlBase = (control) => {
    const tag = control.tagName;
    const type = (control.getAttribute("type") || control.type || "")
      .toLowerCase();
    const name = control.getAttribute("name") || "";
    let stableValue = "";
    if (type === "checkbox" || type === "radio") stableValue = control.value;
    else if (tag === "BUTTON") stableValue = control.value || control.textContent;
    else if (tag === "SUMMARY") stableValue = control.textContent;
    return `${tag}:${type}:${name}:${stableValue}`;
  };
  const draftControlKeys = (controls) => {
    const ordinals = new Map();
    return controls.map((control) => {
      const base = draftControlBase(control);
      const ordinal = ordinals.get(base) || 0;
      ordinals.set(base, ordinal + 1);
      return `${base}:${ordinal}`;
    });
  };
  const captureDrafts = () =>
    [...document.querySelectorAll("[data-coord-draft-key]")].map(
      (container) => {
        const controls = [...container.querySelectorAll(draftControlSelector)];
        const controlKeys = draftControlKeys(controls);
        const focusIndex = controls.indexOf(document.activeElement);
        return {
          key: container.dataset.coordDraftKey,
          open: container.tagName === "DETAILS" ? container.open : null,
          controls: controls.map((control, index) => ({
            key: controlKeys[index],
            value: control.value,
            checked: control.checked,
            selectionStart: typeof control.selectionStart === "number"
              ? control.selectionStart
              : null,
            selectionEnd: typeof control.selectionEnd === "number"
              ? control.selectionEnd
              : null,
          })),
          focus: focusIndex >= 0 ? controlKeys[focusIndex] : "",
        };
      },
    );
  const restoreDrafts = (drafts) => {
    for (const draft of drafts) {
      const container = [...document.querySelectorAll("[data-coord-draft-key]")]
        .find((candidate) => candidate.dataset.coordDraftKey === draft.key);
      if (!container) continue;
      if (draft.open !== null && container.tagName === "DETAILS") {
        container.open = draft.open;
      }
      const controls = [...container.querySelectorAll(draftControlSelector)];
      const controlKeys = draftControlKeys(controls);
      for (const saved of draft.controls) {
        const index = controlKeys.indexOf(saved.key);
        if (index < 0) continue;
        const control = controls[index];
        if (control.type === "checkbox" || control.type === "radio") {
          control.checked = saved.checked;
        } else if (
          control.tagName !== "BUTTON" && control.tagName !== "SUMMARY" &&
          control.type !== "file"
        ) control.value = saved.value;
      }
      if (draft.focus) {
        const focusIndex = controlKeys.indexOf(draft.focus);
        if (focusIndex >= 0) {
          const control = controls[focusIndex];
          const saved = draft.controls.find((item) => item.key === draft.focus);
          control.focus();
          if (
            saved && saved.selectionStart !== null &&
            typeof control.setSelectionRange === "function"
          ) {
            control.setSelectionRange(saved.selectionStart, saved.selectionEnd);
          }
        }
      }
    }
  };

  const applyCoordinator = createCoordApplyCoordinator();
  const postingForms = new WeakSet();
  const coordPageDocument = (html) =>
    new DOMParser().parseFromString(html, "text/html");
  const canonicalCoordURL = (response, requested) => {
    const target = new URL(response.url || requested.href, location.origin);
    if (target.origin !== location.origin || target.pathname !== "/ui/coord") {
      return null;
    }
    if (!target.hash && requested.hash && !response.redirected) {
      target.hash = requested.hash;
    }
    return target;
  };
  const applyCoordPage = (incoming, target, options = {}) => {
    const selectors = ["#coord-rooms", ".coord-conversation", "#coord-context"];
    if (selectors.some((selector) => !incoming.querySelector(selector))) {
      return false;
    }
    const oldRoom = currentRoom();
    const drawerWasOpen = openPanel?.id === "coord-context";
    const scroll = anchorState();
    const focus = captureFocus();
    const drafts = captureDrafts().filter(
      (draft) => draft.key !== options.discardDraftKey,
    );
    if (oldRoom === incomingRoomOf(target) && scroll.threadKey === `${incomingRoomOf(target)}#${new URL(target.href).searchParams.get("thread") || ""}`) {
      incoming.querySelectorAll(".coord-messages > .cm-new").forEach((node) => node.remove());
    }
    selectors.forEach((selector) => replaceSurface(selector, incoming));
    const incomingWorkspace = incoming.querySelector(".coord-workspace");
    if (incomingWorkspace?.dataset.coordEventCursor) {
      workspace.dataset.coordEventCursor =
        incomingWorkspace.dataset.coordEventCursor;
    }
    if (options.history === "push") {
      history.pushState({coord: true}, "", target.href);
    } else if (options.history === "replace") {
      history.replaceState({coord: true}, "", target.href);
    }
    bindDrawers();
    const closeRooms = shouldCloseRoomsDrawer(
      openPanel?.id, options.history, oldRoom, currentRoom(),
    );
    if (closeRooms) {
      returnFocus = null;
      close();
    }
    if (openPanel) {
      const reopenedPanel = document.getElementById(openPanel.id);
      restoreOutside();
      openPanel = reopenedPanel;
      if (!reopenedPanel) return false;
      reopenedPanel.hidden = false;
      reopenedPanel.inert = false;
      setOutsideInert(reopenedPanel);
      const toggle = toggles().find(
        (button) => button.dataset.coordDrawerTarget === openPanel.id,
      );
      if (toggle) {
        toggle.setAttribute("aria-expanded", "true");
        returnFocus = toggle;
      }
    }
    const newRoom = currentRoom();
    const sameThread = scroll.threadKey === threadKey();
    if (oldRoom === newRoom && sameThread && !options.toEnd) restoreAnchor(scroll);
    else {
      const messages = chatOf();
      if (messages) messages.scrollTop = messages.scrollHeight;
    }
    if (oldRoom !== newRoom || !sameThread) rememberDivider();
    else applyDivider();
    restoreDrafts(drafts);
    syncThreadSends();
    fitShell();
    const hashTarget = target.hash && document.getElementById(target.hash.slice(1));
    if (hashTarget) {
      hashTarget.scrollIntoView({block: "nearest"});
      if (/^(INPUT|SELECT|TEXTAREA|BUTTON|A)$/.test(hashTarget.tagName)) {
        hashTarget.focus();
      }
    } else if (closeRooms) {
      const heading = document.querySelector(".coord-conversation-head h2");
      if (heading) {
        heading.tabIndex = -1;
        heading.focus({preventScroll: true});
      }
    } else restoreFocus(focus);
    markHighestRenderedRead();
    if (!drawerWasOpen) openSelectedThread();
    return true;
  };
  const fetchCoordPage = async (target, options = {}) => {
    const lease = applyCoordinator.begin("navigation");
    setActionStatus(options.loadingLabel || text("loading"));
    try {
      const response = await fetch(target.href, {
        credentials: "same-origin",
        headers: {"X-Coord-Progressive": "1"},
        signal: lease.controller.signal,
      });
      if (!applyCoordinator.current(lease)) return coordPageLoadSuperseded;
      const responseURL = new URL(response.url || target.href, location.origin);
      if (responseURL.pathname === "/ui/login") {
        location.assign("/ui/login");
        return coordPageLoadRedirected;
      }
      if (!response.ok) {
        setActionStatus(
          text("load_failed").replace("%s", response.status),
          true,
        );
        return response.status === 403 || response.status === 404
          ? coordPageLoadForbidden
          : coordPageLoadFailed;
      }
      const canonical = canonicalCoordURL(response, target);
      const html = await response.text();
      if (!applyCoordinator.current(lease)) return coordPageLoadSuperseded;
      const incoming = coordPageDocument(html);
      if (!canonical || !applyCoordPage(incoming, canonical, options)) {
        setActionStatus(text("render_failed"), true);
        return coordPageLoadFailed;
      }
      if (options.successLabel) setActionStatus(options.successLabel);
      else quietSuccess(text("updated"));
      return coordPageLoadApplied;
    } catch (error) {
      if (error?.name === "AbortError") return coordPageLoadSuperseded;
      if (!applyCoordinator.current(lease)) return coordPageLoadSuperseded;
      setActionStatus(text("offline_page"), true);
      return coordPageLoadOffline;
    } finally {
      applyCoordinator.finish(lease);
    }
  };
  // The server says in a sentence what was wrong (a name that fits several, a
  // request without addressee): that sentence is shown, not a bare status.
  const refusal = async (response) => {
    try {
      if (!/^text\/plain/.test(response.headers.get("Content-Type") || "")) return "";
      const body = (await response.text()).trim();
      return body && body.length < 400 ? body : "";
    } catch (_) {
      return "";
    }
  };
  const postCoordForm = async (form, target, submitter) => {
    if (postingForms.has(form)) return;
    const body = coordFormBody(form, submitter);
    if (!body) {
      setActionStatus(text("no_uploads"), true);
      return;
    }
    if (!body.get("csrf_token")) {
      setActionStatus(text("no_token"), true);
      return;
    }
    postingForms.add(form);
    form.setAttribute("aria-busy", "true");
    if (submitter) submitter.disabled = true;
    const draftContainer = form.closest("[data-coord-draft-key]");
    const lease = applyCoordinator.begin("post");
    setActionStatus(text("working"));
    try {
      const response = await fetch(target.href, {
        method: "POST",
        body,
        credentials: "same-origin",
        redirect: "follow",
        headers: {"X-Coord-Progressive": "1"},
        signal: lease.controller.signal,
      });
      if (!applyCoordinator.current(lease)) return;
      const responseURL = new URL(response.url || target.href, location.origin);
      if (responseURL.pathname === "/ui/login") {
        location.assign("/ui/login");
        return;
      }
      if (!response.ok) {
        const reason = response.status < 500 ? await refusal(response) : "";
        setActionStatus(
          reason ? `${reason} ${text("kept")}` : text("failed").replace("%s", response.status),
          true,
        );
        return;
      }
      const canonical = canonicalCoordURL(response, target);
      const html = await response.text();
      if (!applyCoordinator.current(lease)) return;
      const incoming = coordPageDocument(html);
      if (!canonical || !applyCoordPage(incoming, canonical, {
        history: "push",
        toEnd: true,
        discardDraftKey: draftContainer?.dataset.coordDraftKey || "",
      })) {
        location.assign(response.url || "/ui/coord");
        return;
      }
      // The message is in the chat: that is the confirmation. Screen readers are told.
      quietSuccess(text("done"));
    } catch (error) {
      if (error?.name === "AbortError" || !applyCoordinator.current(lease)) {
        return;
      }
      setActionStatus(
        text("offline_post"),
        true,
      );
    } finally {
      applyCoordinator.finish(lease);
      postingForms.delete(form);
      if (form.isConnected) {
        form.removeAttribute("aria-busy");
        if (submitter) submitter.disabled = false;
      }
    }
  };
  document.addEventListener("click", (event) => {
    if (
      event.defaultPrevented || event.button !== 0 || event.metaKey ||
      event.ctrlKey || event.shiftKey || event.altKey
    ) return;
    const anchor = event.target.closest?.("a");
    if (!anchor || (anchor.getAttribute("href") || "").startsWith("#")) return;
    const target = coordPageTarget(anchor, location.origin);
    if (!target) return;
    event.preventDefault();
    fetchCoordPage(target, {history: "push"}).then((result) => {
      if (result === coordPageLoadOffline) location.assign(target.href);
    });
  });
  document.addEventListener("submit", (event) => stampCoordExpiryOffset(event.target), true);
  document.addEventListener("submit", (event) => {
    const form = event.target;
    const target = coordFormTarget(form, location.origin, event.submitter);
    if (!target) return;
    event.preventDefault();
    // A request goes to someone: say so before anything is sent.
    if (event.submitter?.matches?.("[data-needs-to]")) {
      const to = form.querySelector('input[name="mentions"]');
      if (!to || !to.value) {
        setActionStatus(text("need_to"), true);
        form.querySelector("[data-pick] [data-dd]")?.focus();
        return;
      }
    }
    postCoordForm(form, target, event.submitter);
  });
  // The status row of a thread: a different status makes the send button say
  // what it will do, and the words become optional. The thread composer has no
  // `required` of its own (without script a bare status change must post); this
  // adds it back while the status is unchanged and the words are the only content.
  const syncThreadSend = (form) => {
    const picked = form.querySelector("input[data-state-input]:checked");
    const area = form.querySelector("textarea[name='body']");
    const button = form.querySelector("button.comp-send");
    if (!picked || !area || !button) return;
    const was = form.querySelector("input[name='state_was']")?.value;
    const changed = picked.value !== was;
    area.required = !changed;
    button.textContent = changed
      ? (area.value.trim() ? picked.dataset.labelText : picked.dataset.label)
      : button.dataset.sendLabel;
  };
  const syncThreadSends = () => document.querySelectorAll("form.coord-thread-composer").forEach(syncThreadSend);
  document.addEventListener("change", (event) => {
    if (event.target.matches?.("input[data-state-input]")) syncThreadSend(event.target.form);
  });
  document.addEventListener("input", (event) => {
    if (event.target.matches?.("textarea[name='body']") && event.target.form?.classList.contains("coord-thread-composer")) syncThreadSend(event.target.form);
  });
  // Composer: the mode pill picks what a post becomes. A request needs a kind,
  // anything else carries none.
  document.addEventListener("change", (event) => {
    const radio = event.target;
    if (!radio.matches?.('input[name="mode"]') || !radio.form) return;
    const kinds = [...radio.form.querySelectorAll('input[name="intent"]')];
    const to = [...radio.form.querySelectorAll(".comp-to input")];
    if (radio.value === "request") {
      if (!kinds.some((kind) => kind.checked) && kinds[0]) kinds[0].checked = true;
    } else {
      kinds.forEach((kind) => { kind.checked = false; });
      to.forEach((target) => { target.checked = false; });
    }
    // The addressee is the one thing a request cannot do without.
    to.forEach((target) => { target.required = radio.value === "request"; });
  });
  // Enter sends, Shift+Enter breaks the line. A directive stays in force until
  // it is ended, so it is only ever set with its own button.
  document.addEventListener("keydown", (event) => {
    if (
      event.key !== "Enter" || event.shiftKey || event.altKey || event.ctrlKey ||
      event.metaKey || event.isComposing || event.defaultPrevented
    ) return;
    const area = event.target;
    if (!(area instanceof HTMLTextAreaElement) || area.name !== "body" || !area.form) return;
    const form = area.form;
    if (form.querySelector('input[name="mode"]:checked')?.value === "directive") return;
    if (!area.value.trim()) return;
    event.preventDefault();
    const button = [...form.querySelectorAll("button.comp-send")]
      .find((candidate) => candidate.offsetParent !== null);
    form.requestSubmit(button);
  });
  addEventListener("popstate", () => {
    const target = new URL(location.href);
    if (target.pathname !== "/ui/coord") return;
    fetchCoordPage(target).then((result) => {
      const fallback = coordFailedPopstateTarget(result, location.href);
      if (fallback) location.assign(fallback);
    });
  });

  let pending = new Set();
  let refreshing = false;
  let refreshTimer = null;
  const queueSurface = (selector) => pending.add(selector);
  const queueSurfaces = (selectors) => selectors.forEach(queueSurface);
  const roomSurfaces = [
    ".coord-conversation-head > div",
    ".coord-paging",
    ".coord-messages",
    "[data-coord-read-actions]",
  ];
  const contextSurfaces = [
    ".coord-participants",
    "[data-coord-standing-list]",
    "[data-coord-thread-list]",
  ];
  const threadSurfaces = [
    "[data-coord-thread-list]",
    ".coord-thread-detail > header",
    ".coord-thread-paging",
    ".coord-thread-messages",
  ];
  const coordEventSurfaces = (event, room, thread) => {
    const selectors = new Set([
      "[data-coord-sidebar-dynamic]",
      "[data-coord-rooms-count]",
    ]);
    if (event.kind === "attention") {
      selectors.add("[data-coord-attention-details]");
    }
    if (event.object_kind === "room" && event.object_id === room) {
      if (event.kind !== "read" && event.kind !== "attention") {
        roomSurfaces.forEach((selector) => selectors.add(selector));
      }
      contextSurfaces.forEach((selector) => selectors.add(selector));
    }
    if (event.object_kind === "discussion" && event.object_id === thread) {
      threadSurfaces.forEach((selector) => selectors.add(selector));
    }
    if (event.object_kind === "thread") {
      threadSurfaces.forEach((selector) => selectors.add(selector));
      selectors.add(".coord-messages");
    }
    return [...selectors];
  };
  const scheduleRefreshDrain = (delay = 35) => {
    if (refreshing || refreshTimer !== null || !pending.size) return;
    refreshTimer = setTimeout(() => {
      refreshTimer = null;
      drainRefreshQueue();
    }, delay);
  };
  const refreshRetry = createCoordRefreshRetry(scheduleRefreshDrain);
  const drainRefreshQueue = async () => {
    if (refreshing || !pending.size) return;
    refreshing = true;
    let blockedByUser = false;
    let transientFailure = false;
    try {
      while (pending.size) {
        const lease = applyCoordinator.begin("refresh");
        if (!lease) {
          blockedByUser = true;
          return;
        }
        const selectors = [...pending];
        pending = new Set();
        const scroll = anchorState();
        const focus = captureFocus();
        const drafts = captureDrafts();
        try {
          const response = await fetch(location.href, {
            headers: { "X-Coord-Fragment": "visible" },
            credentials: "same-origin",
            signal: lease.controller.signal,
          });
          if (!applyCoordinator.current(lease)) return;
          if (new URL(response.url).pathname === "/ui/login") {
            location.assign("/ui/login");
            return;
          }
          if (response.status === 403 || response.status === 404) {
            location.assign("/ui/coord");
            return;
          }
          if (!response.ok) throw new Error(`coord refresh ${response.status}`);
          const html = await response.text();
          if (!applyCoordinator.current(lease)) return;
          const incoming = new DOMParser().parseFromString(html, "text/html");
          // The divider "New" is ours (it stays where it was drawn); the server's would move with the read mark.
          incoming.querySelectorAll(".coord-messages > .cm-new").forEach((node) => node.remove());
          const flowBefore = chatOf();
          const seenBefore = lastSeq(flowBefore);
          const wasAtEnd = flowBefore ? nearEnd(flowBefore) : true;
          selectors.forEach((selector) => replaceSurface(selector, incoming));
          bindDrawers();
          restoreAnchor(scroll);
          applyDivider();
          const flowAfter = chatOf();
          // At the end the chat goes along with what comes; elsewhere a pill says there is more.
          if (flowAfter && lastSeq(flowAfter) > seenBefore) {
            if (wasAtEnd) flowAfter.scrollTop = flowAfter.scrollHeight;
            else if (newPill()) newPill().hidden = false;
          }
          restoreDrafts(drafts);
          syncThreadSends();
          restoreFocus(focus);
          markHighestRenderedRead();
          refreshRetry.success();
          setLive("live", text("live"), text("updated"));
        } catch (error) {
          selectors.forEach(queueSurface);
          if (error?.name === "AbortError" || !applyCoordinator.current(lease)) {
            blockedByUser = true;
            return;
          } else {
            transientFailure = true;
            setLive(
              "offline",
              text("refresh_failed"),
              text("refresh_failed_announce"),
            );
            return;
          }
        } finally {
          applyCoordinator.finish(lease);
        }
      }
    } finally {
      refreshing = false;
      if (pending.size && transientFailure) refreshRetry.failure();
      else if (pending.size && !blockedByUser) scheduleRefreshDrain();
    }
  };
  applyCoordinator.onIdle(scheduleRefreshDrain);
  const refreshVisible = (event) => {
    queueSurfaces(coordEventSurfaces(event, currentRoom(), currentThread()));
    scheduleRefreshDrain();
  };

  let lastReadSubmitted = "";
  const markHighestRenderedRead = () => {
    if (document.visibilityState !== "visible") return;
    const room = currentRoom();
    const messages = [
      ...document.querySelectorAll(
        ".coord-messages > .coord-message[id^='message-']",
      ),
    ];
    const last = messages[messages.length - 1];
    const csrf = document.querySelector("input[name='csrf_token']")?.value;
    if (!room || !last || !csrf) return;
    const sequence = last.id.slice("message-".length);
    const key = `${room}:${sequence}`;
    if (!/^\d+$/.test(sequence) || key === lastReadSubmitted) return;
    lastReadSubmitted = key;
    const body = new URLSearchParams({ csrf_token: csrf, room, sequence });
    fetch("/ui/coord/read", {
      method: "POST",
      body,
      credentials: "same-origin",
      redirect: "manual",
    }).catch(() => {
      lastReadSubmitted = "";
    });
  };

  if (!("EventSource" in window)) {
    setLive("unsupported", text("unsupported"));
  } else {
    const cursor = workspace.dataset.coordEventCursor || "0";
    // Der Cursor ist undurchsichtig (versiegelt); er wird nur durchgereicht und
    // nie als Zahl gelesen oder verglichen.
    let lastEventID = cursor;
    const source = new EventSource(
      `/ui/coord/events?after=${encodeURIComponent(cursor)}`,
    );
    source.addEventListener("open", () => {
      setLive("live", text("live"));
      markHighestRenderedRead();
    });
    source.addEventListener("coord.changed", (raw) => {
      try {
        if (!raw.lastEventId) {
          location.reload();
          return;
        }
        if (raw.lastEventId === lastEventID) return;
        lastEventID = raw.lastEventId;
        refreshVisible(JSON.parse(raw.data));
      } catch (_) {
        location.reload();
      }
    });
    source.addEventListener("resync", () => {
      setLive("resync", text("resync"));
      location.reload();
    });
    source.addEventListener("session-ended", () => {
      source.close();
      setLive("offline", text("session_ended"));
      location.assign("/ui/login");
    });
    source.onerror = () =>
      setLive(
        "offline",
        text("offline_retry"),
        text("offline_retry_announce"),
      );
  }
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") markHighestRenderedRead();
  });
})();
