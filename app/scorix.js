// Scorix frontend bridge — injected by the app in both modes:
//   app mode : via AddScriptToExecuteOnDocumentCreated, transport = WebView2 channel
//   web mode : inlined into served HTML, transport = WebSocket /ipc
// Same {id,kind,name,state,data,error} envelope that internal/ipc speaks.
// This is the only bridge — apps must not ship their own.
(function () {
  if (window.scorix) return; // idempotent injection guard (InitScript + HTML inject)

  const pending = new Map();   // id -> {resolve, reject, onChunk, timer}
  const listeners = new Map(); // event name -> Set(fn)
  let seq = 0;
  const nextId = () => "c-" + (++seq);

  // ── connection status ──
  // "connected" | "connecting" | "disconnected". App mode is always connected.
  // Transitions are announced as a `scorix:connection:status` CustomEvent on
  // window so UI (status badges) can react without polling bridge internals.
  let status = "connecting";
  function setStatus(s) {
    if (status === s) return;
    status = s;
    try { window.dispatchEvent(new CustomEvent("scorix:connection:status", { detail: s })); } catch (_) {}
  }

  function dispatch(raw) {
    let msg;
    try { msg = typeof raw === "string" ? JSON.parse(raw) : raw; } catch (_) { return; }
    if (msg.kind === "event") {
      const set = listeners.get(msg.name);
      if (set) for (const fn of set) { try { fn(msg.data, msg.error); } catch (e) { console.error(e); } }
      return;
    }
    const p = pending.get(msg.id);
    if (!p) return;
    if (msg.state === "chunk") { if (p.onChunk) p.onChunk(msg.data); return; }
    pending.delete(msg.id);
    if (p.timer) clearTimeout(p.timer);
    if (msg.state === "error") p.reject(new Error(msg.error || "scorix: command failed"));
    else p.resolve(msg.data);
  }

  // failAll rejects and clears every pending invoke — used when the transport
  // drops, so callers don't hang forever and the pending map can't leak.
  function failAll(err) {
    for (const [, p] of pending) { if (p.timer) clearTimeout(p.timer); try { p.reject(err); } catch (_) {} }
    pending.clear();
  }

  // ── transport ──
  let send;
  let mode;
  let reconnectNow = null; // web mode: force an immediate (re)connect, returns Promise

  const wv = window.chrome && window.chrome.webview;
  const wk = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.scorix;
  if (wv) {
    // Windows: WebView2 channel.
    mode = "app";
    send = (msg) => wv.postMessage(JSON.stringify(msg));
    wv.addEventListener("message", (e) => dispatch(e.data));
    setStatus("connected");
  } else if (wk) {
    // macOS (WKWebView) and Linux (WebKitGTK) both expose
    // window.webkit.messageHandlers.<name>. Go -> JS arrives via
    // evaluateJavaScript calling the global __scorix_receive.
    mode = "app";
    send = (msg) => wk.postMessage(JSON.stringify(msg));
    window.__scorix_receive = dispatch;
    setStatus("connected");
  } else {
    mode = "web";
    // Reconnecting WebSocket: exponential backoff 1s..30s, pending invokes are
    // rejected on disconnect (their replies are gone with the old connection),
    // outbound messages are queued only while a connect is in flight.
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    // window.__scorix_ws_url lets a dev shell (e.g. `next dev`) point the bridge
    // at a separately-running Go server. Default: same host, /ipc.
    const wsURL = () => window.__scorix_ws_url || (proto + "//" + location.host + "/ipc");
    const MAX_QUEUE = 256;
    let ws = null;
    let queue = [];
    let attempts = 0;
    let timer = null;
    let waiters = []; // init() promises waiting for the next successful open

    function settleWaiters(err) {
      const ws2 = waiters; waiters = [];
      for (const w of ws2) err ? w.reject(err) : w.resolve();
    }

    function connect() {
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
      if (timer) { clearTimeout(timer); timer = null; }
      setStatus("connecting");
      try { ws = new WebSocket(wsURL()); } catch (e) {
        setStatus("disconnected"); settleWaiters(e); schedule(); return;
      }
      ws.onopen = () => {
        attempts = 0;
        setStatus("connected");
        const q = queue; queue = [];
        for (const m of q) { try { ws.send(m); } catch (_) {} }
        settleWaiters(null);
      };
      ws.onmessage = (e) => dispatch(e.data);
      ws.onclose = () => {
        ws = null;
        setStatus("disconnected");
        failAll(new Error("scorix: connection lost"));
        queue = [];
        settleWaiters(new Error("scorix: connection failed"));
        schedule();
      };
      ws.onerror = () => { /* onclose follows and handles state */ };
    }

    function schedule() {
      if (timer) return;
      const delay = Math.min(1000 * Math.pow(2, attempts++), 30000);
      timer = setTimeout(() => { timer = null; connect(); }, delay);
    }

    reconnectNow = () => new Promise((resolve, reject) => {
      if (ws && ws.readyState === WebSocket.OPEN) { resolve(); return; }
      waiters.push({ resolve, reject });
      attempts = 0; // user-initiated retry resets the backoff
      connect();
    });

    send = (msg) => {
      const s = JSON.stringify(msg);
      if (ws && ws.readyState === WebSocket.OPEN) { ws.send(s); return; }
      if (status === "connecting") {
        if (queue.length >= MAX_QUEUE) throw new Error("scorix: send queue full");
        queue.push(s);
        return;
      }
      throw new Error("scorix: offline");
    };

    connect();
  }

  window.scorix = {
    // "app" (native window) or "web" (browser over WebSocket).
    mode,

    // status() — current transport status; transitions also fire the
    // `scorix:connection:status` CustomEvent on window.
    status() { return status; },

    invoke(name, data, opts) {
      const id = nextId();
      return new Promise((resolve, reject) => {
        const entry = { resolve, reject, onChunk: opts && opts.onChunk };
        const timeout = opts && opts.timeout; // ms; opt-in (omit for long/streaming calls)
        if (timeout) {
          entry.timer = setTimeout(() => {
            if (pending.delete(id)) {
              try { send({ id, state: "cancel" }); } catch (_) {}
              reject(new Error("scorix: invoke '" + name + "' timed out after " + timeout + "ms"));
            }
          }, timeout);
        }
        pending.set(id, entry);
        try {
          send({ id, kind: "command", name, state: "start", data: data === undefined ? null : data });
        } catch (e) {
          pending.delete(id);
          if (entry.timer) clearTimeout(entry.timer);
          reject(e);
        }
      });
    },
    emit(name, data) {
      send({ id: nextId(), kind: "event", name, data: data === undefined ? null : data });
    },
    on(name, fn) {
      let set = listeners.get(name);
      if (!set) { set = new Set(); listeners.set(name, set); }
      set.add(fn);
      return () => set.delete(fn);
    },
    cancel(id) {
      const p = pending.get(id);
      if (p) {
        pending.delete(id);
        if (p.timer) clearTimeout(p.timer);
        p.reject(new Error("scorix: cancelled"));
      }
      send({ id, state: "cancel" });
    },

    // init() — the bridge auto-initializes on injection. In web mode, calling
    // init() while disconnected forces an immediate reconnect (resets backoff)
    // and resolves on the next successful open — used by "Reconnect" buttons.
    init() { return reconnectNow ? reconnectNow() : Promise.resolve(); },

    // resolve(name, handler) — register a JS handler the backend may invoke
    // (Go -> JS). Stored for ScorixAPI compatibility; the reverse-RPC dispatch
    // is wired when a backend uses it.
    resolve(name, handler) {
      let set = listeners.get("__resolve__:" + name);
      if (!set) { set = new Set(); listeners.set("__resolve__:" + name, set); }
      set.add(handler);
    },
  };
})();
