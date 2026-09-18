(() => {
  const workspace = document.querySelector(".coord-workspace");
  if (!workspace) return;

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
  const sync = () => {
    close();
    for (const id of ["coord-rooms", "coord-context"]) {
      const panel = document.getElementById(id);
      if (!panel) continue;
      panel.hidden = media.matches;
      panel.inert = media.matches;
    }
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
  const coordFormTarget = (form, origin) => {
    if (!form || form.method.toLowerCase() !== "post") return null;
    const target = new URL(form.action, origin);
    return target.origin === origin && progressiveFormPaths.has(target.pathname)
      ? target
      : null;
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
  const actionStatus = document.querySelector("[data-coord-action-status]");
  const setLive = (state, label, announcement = "") => {
    document.documentElement.dataset.coordLive = state;
    if (liveStatus) liveStatus.textContent = label;
    if (announcement && announce) announce.textContent = announcement;
  };
  const setActionStatus = (message, error = false) => {
    if (actionStatus) {
      actionStatus.textContent = message;
      actionStatus.hidden = !message;
      actionStatus.dataset.error = error ? "true" : "false";
    }
    if (message && announce) announce.textContent = message;
  };

  const pageParams = () => new URLSearchParams(location.search);
  const currentRoom = () => pageParams().get("room") || "";
  const currentThread = () => pageParams().get("thread") || "";
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
    current.replaceChildren(...incoming.childNodes);
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
    if (oldRoom === newRoom) restoreAnchor(scroll);
    else {
      const messages = document.querySelector(".coord-messages");
      if (messages) messages.scrollTop = messages.scrollHeight;
    }
    restoreDrafts(drafts);
    const hashTarget = target.hash && document.getElementById(target.hash.slice(1));
    if (hashTarget) {
      hashTarget.scrollIntoView({block: "nearest"});
      if (/^(INPUT|SELECT|TEXTAREA|BUTTON|A)$/.test(hashTarget.tagName)) {
        hashTarget.focus();
      }
    } else restoreFocus(focus);
    markHighestRenderedRead();
    if (!drawerWasOpen) openSelectedThread();
    return true;
  };
  const fetchCoordPage = async (target, options = {}) => {
    const lease = applyCoordinator.begin("navigation");
    setActionStatus(options.loadingLabel || "Unterhaltung wird geladen …");
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
          `Unterhaltung konnte nicht geladen werden (HTTP ${response.status}).`,
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
        setActionStatus("Die Serverantwort konnte nicht angezeigt werden.", true);
        return coordPageLoadFailed;
      }
      setActionStatus(options.successLabel || "Unterhaltung aktualisiert");
      return coordPageLoadApplied;
    } catch (error) {
      if (error?.name === "AbortError") return coordPageLoadSuperseded;
      if (!applyCoordinator.current(lease)) return coordPageLoadSuperseded;
      setActionStatus("Offline · Seite wird normal geöffnet.", true);
      return coordPageLoadOffline;
    } finally {
      applyCoordinator.finish(lease);
    }
  };
  const postCoordForm = async (form, target, submitter) => {
    if (postingForms.has(form)) return;
    const body = coordFormBody(form, submitter);
    if (!body) {
      setActionStatus("Datei-Uploads werden hier nicht unterstützt.", true);
      return;
    }
    if (!body.get("csrf_token")) {
      setActionStatus("Sicherheits-Token fehlt; Formular nicht gesendet.", true);
      return;
    }
    postingForms.add(form);
    form.setAttribute("aria-busy", "true");
    if (submitter) submitter.disabled = true;
    const draftContainer = form.closest("[data-coord-draft-key]");
    const lease = applyCoordinator.begin("post");
    setActionStatus("Aktion wird ausgeführt …");
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
        setActionStatus(
          `Aktion fehlgeschlagen (HTTP ${response.status}). Eingaben bleiben erhalten.`,
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
        discardDraftKey: draftContainer?.dataset.coordDraftKey || "",
      })) {
        location.assign(response.url || "/ui/coord");
        return;
      }
      setActionStatus("Aktion abgeschlossen");
    } catch (error) {
      if (error?.name === "AbortError" || !applyCoordinator.current(lease)) {
        return;
      }
      setActionStatus(
        "Offline · Aktion nicht automatisch erneut gesendet; Eingaben bleiben erhalten.",
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
  document.addEventListener("submit", (event) => {
    const form = event.target;
    const target = coordFormTarget(form, location.origin);
    if (!target) return;
    event.preventDefault();
    postCoordForm(form, target, event.submitter);
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
    const selectors = new Set(["[data-coord-sidebar-dynamic]"]);
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
          selectors.forEach((selector) => replaceSurface(selector, incoming));
          bindDrawers();
          restoreAnchor(scroll);
          restoreDrafts(drafts);
          restoreFocus(focus);
          markHighestRenderedRead();
          refreshRetry.success();
          setLive("live", "Live verbunden", "Unterhaltung aktualisiert");
        } catch (error) {
          selectors.forEach(queueSurface);
          if (error?.name === "AbortError" || !applyCoordinator.current(lease)) {
            blockedByUser = true;
            return;
          } else {
            transientFailure = true;
            setLive(
              "offline",
              "Live · Aktualisierung fehlgeschlagen",
              "Live-Aktualisierung fehlgeschlagen",
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
    setLive("unsupported", "Live-Updates nicht unterstützt");
  } else {
    const cursor = workspace.dataset.coordEventCursor || "0";
    let lastEventID = Number(cursor);
    const source = new EventSource(
      `/ui/coord/events?after=${encodeURIComponent(cursor)}`,
    );
    source.addEventListener("open", () => {
      setLive("live", "Live verbunden");
      markHighestRenderedRead();
    });
    source.addEventListener("coord.changed", (raw) => {
      try {
        const eventID = Number(raw.lastEventId);
        if (!Number.isSafeInteger(eventID) || eventID < 0) {
          location.reload();
          return;
        }
        if (eventID <= lastEventID) return;
        lastEventID = eventID;
        refreshVisible(JSON.parse(raw.data));
      } catch (_) {
        location.reload();
      }
    });
    source.addEventListener("resync", () => {
      setLive("resync", "Synchronisiere …");
      location.reload();
    });
    source.addEventListener("session-ended", () => {
      source.close();
      setLive("offline", "Sitzung beendet");
      location.assign("/ui/login");
    });
    source.onerror = () =>
      setLive(
        "offline",
        "Offline · Verbindung wird wiederholt",
        "Live-Verbindung unterbrochen",
      );
  }
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") markHighestRenderedRead();
  });
})();
