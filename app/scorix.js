// Scorix frontend bridge — injected in both modes (app: InitScript over the native
// channel; web: inlined HTML over WebSocket /ipc). Speaks internal/ipc's
// {id,kind,name,state,data,error} envelope. The only bridge — apps ship none.
(function () {
  if (window.scorix) return; // idempotent guard (InitScript + HTML inject both run)

  const pending = new Map();   // id -> {resolve, reject, onChunk, timer}  (kind:"command")
  const streams = new Map();   // id -> stream controller                 (kind:"rpc")
  const listeners = new Map(); // event name -> Set(fn)
  let seq = 0;
  const nextId = () => "c-" + (++seq);

  // rpc wire: open{state:"open"}+request → server msg{state:"msg"}* → done|error.
  // Returns an async-iterable, cancelable consumer.
  function makeStream(id, name) {
    const buffer = []; // msgs awaiting a consumer
    const waiters = []; // pending next() {resolve,reject}
    let ended = false;
    let failure = null;

    function pushMsg(data) {
      if (ended) return;
      if (waiters.length) waiters.shift().resolve({ value: data, done: false });
      else buffer.push(data);
    }
    function finish(err) {
      if (ended) return;
      ended = true;
      failure = err || null;
      while (waiters.length) {
        const w = waiters.shift();
        failure ? w.reject(failure) : w.resolve({ value: undefined, done: true });
      }
    }
    function cancel() {
      if (ended) return;
      try { send({ id, kind: "rpc", name, state: "cancel" }); } catch (_) {}
      streams.delete(id);
      finish(null); // graceful local completion
    }
    const iterator = {
      next() {
        if (buffer.length) return Promise.resolve({ value: buffer.shift(), done: false });
        if (ended) return failure ? Promise.reject(failure) : Promise.resolve({ value: undefined, done: true });
        return new Promise((resolve, reject) => waiters.push({ resolve, reject }));
      },
      return() { cancel(); return Promise.resolve({ value: undefined, done: true }); },
    };
    return { pushMsg, finish, iterable: { [Symbol.asyncIterator]: () => iterator, cancel } };
  }

  // "connected"|"connecting"|"disconnected" (app mode always connected).
  // Transitions fire a `scorix:connection:status` CustomEvent on window.
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
    if (msg.kind === "rpc") {
      const st = streams.get(msg.id);
      if (!st) return; // unknown/closed call — drop (never resolve a foreign frame)
      if (msg.state === "msg") { st.pushMsg(msg.data); return; }
      if (msg.state === "done") { streams.delete(msg.id); st.finish(null); return; }
      if (msg.state === "error") { streams.delete(msg.id); st.finish(new Error(msg.error || "scorix: stream failed")); return; }
      return;
    }
    const p = pending.get(msg.id);
    if (!p) {
      const st = streams.get(msg.id);
      if (st) {
        streams.delete(msg.id);
        st.finish(new Error(msg.error || "scorix: protocol mismatch — rebuild/restart the app (backend is on an older protocol)"));
      }
      return;
    }
    if (msg.state === "chunk") { if (p.onChunk) p.onChunk(msg.data); return; }
    pending.delete(msg.id);
    if (p.timer) clearTimeout(p.timer);
    if (msg.state === "error") p.reject(new Error(msg.error || "scorix: command failed"));
    else p.resolve(msg.data);
  }

  // failAll rejects+clears every pending invoke/stream on transport drop, so
  // callers don't hang and the maps can't leak.
  function failAll(err) {
    for (const [, p] of pending) { if (p.timer) clearTimeout(p.timer); try { p.reject(err); } catch (_) {} }
    pending.clear();
    for (const [, st] of streams) { try { st.finish(err); } catch (_) {} }
    streams.clear();
  }

  let send;
  let mode;
  let reconnectNow = null; // web mode: force an immediate (re)connect, returns Promise

  const wv = window.chrome && window.chrome.webview;
  const wk = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.scorix;
  if (wv) {
    mode = "app"; // Windows: WebView2 channel
    send = (msg) => wv.postMessage(JSON.stringify(msg));
    wv.addEventListener("message", (e) => dispatch(e.data));
    setStatus("connected");
  } else if (wk) {
    // macOS (WKWebView) / Linux (WebKitGTK): Go->JS arrives via evaluateJavaScript
    // calling the global __scorix_receive.
    mode = "app";
    send = (msg) => wk.postMessage(JSON.stringify(msg));
    window.__scorix_receive = dispatch;
    setStatus("connected");
  } else {
    mode = "web";
    // Reconnecting WS: backoff 1s..30s; pending invokes rejected on disconnect
    // (replies gone with the connection); outbound queued only mid-connect.
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    // __scorix_ws_url lets a dev shell (e.g. `next dev`) point at a separate Go server.
    const wsURL = () => window.__scorix_ws_url || (proto + "//" + location.host + "/ipc");
    const MAX_QUEUE = 256;
    let ws = null;
    let queue = [];
    let attempts = 0;
    let timer = null;
    let waiters = []; // init() promises awaiting the next successful open

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
      attempts = 0; // user-initiated retry resets backoff
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
    mode, // "app" (native window) or "web" (browser over WebSocket)

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
    // serverStream(name, data) — 1->N rpc; async-iterable with .cancel(), ends on
    // the server's `done`, throws on `error`.
    serverStream(name, data) {
      const id = nextId();
      const st = makeStream(id, name);
      streams.set(id, st);
      try {
        send({ id, kind: "rpc", name, state: "open", data: data === undefined ? null : data });
      } catch (e) {
        streams.delete(id);
        st.finish(e);
      }
      return st.iterable;
    },

    // duplex(name) — N<->N rpc; async-iterable of server msgs plus send/end/cancel.
    duplex(name) {
      const id = nextId();
      const st = makeStream(id, name);
      streams.set(id, st);
      const iface = st.iterable;
      // A write on a dropped socket must fail the stream gracefully, not throw into caller code.
      iface.send = (data) => {
        try { send({ id, kind: "rpc", name, state: "data", data: data === undefined ? null : data }); }
        catch (e) { streams.delete(id); st.finish(e); }
      };
      iface.end = () => {
        try { send({ id, kind: "rpc", name, state: "end" }); }
        catch (e) { streams.delete(id); st.finish(e); }
      };
      try {
        send({ id, kind: "rpc", name, state: "open" });
      } catch (e) {
        streams.delete(id);
        st.finish(e);
      }
      return iface;
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

    // init() — web mode: force an immediate reconnect (resets backoff), resolves on
    // next open. Auto-initialized on injection otherwise.
    init() { return reconnectNow ? reconnectNow() : Promise.resolve(); },

    // resolve(name, handler) — register a Go->JS handler. Stored for ScorixAPI compat;
    // reverse-RPC dispatch is wired when a backend uses it.
    resolve(name, handler) {
      let set = listeners.get("__resolve__:" + name);
      if (!set) { set = new Set(); listeners.set("__resolve__:" + name, set); }
      set.add(handler);
    },
  };
})();
