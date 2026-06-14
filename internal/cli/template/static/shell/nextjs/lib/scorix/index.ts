"use client";

export type ScorixStatus = "connected" | "connecting" | "disconnected";

// Matches the window.scorix bridge the Go app injects; apps must not ship their own.
export interface ScorixAPI {
  mode?: "app" | "web";
  invoke<T = any>(
    method: string,
    params?: any,
    options?: { timeout?: number; onChunk?: (chunk: any) => void },
  ): Promise<T>;
  emit(topic: string, data?: any): void | Promise<void>;
  on(topic: string, callback: (data: any, error?: string) => void): () => void;
  resolve(name: string, handler: (data: any) => any): void;
  init(options?: any): Promise<void>;
  status?(): ScorixStatus;
  cancel?(id: string): void;
}

declare global {
  interface Window {
    scorix?: ScorixAPI;
    // Dev override: point the web-mode bridge at a separately running Go server.
    __scorix_ws_url?: string;
  }
}

// The exported wrapper is always async (it awaits bridge readiness), so its
// emit is a Promise even though the raw injected bridge returns void.
export interface ScorixClient extends Omit<ScorixAPI, "emit"> {
  emit(topic: string, data?: any): Promise<void>;
}

let _cachedApi: ScorixAPI | null = null;
let _initPromise: Promise<ScorixAPI> | null = null;

/**
 * Gets the Scorix API instance, waiting for it to be initialized if necessary.
 */
async function getScorix(): Promise<ScorixAPI> {
  if (typeof window === "undefined") {
    throw new Error("Scorix is only available in the browser environment");
  }

  if (window.scorix) return window.scorix;
  if (_cachedApi) return _cachedApi;

  if (!_initPromise) {
    _initPromise = new Promise((resolve, reject) => {
      const start = Date.now();
      const interval = setInterval(() => {
        if (window.scorix) {
          clearInterval(interval);
          _cachedApi = window.scorix;
          resolve(window.scorix);
        } else if (Date.now() - start > 5000) {
          clearInterval(interval);
          reject(new Error("Scorix bridge initialization timed out. window.scorix is injected by the Go app — run the shell through the app (scorix dev), not standalone."));
        }
      }, 50); // Faster check
    });
  }

  return _initPromise;
}

/**
 * The primary Scorix API singleton. 
 * All methods wait for the bridge to be ready before executing.
 */
const scorix: ScorixClient = {
  async invoke<T = any>(method: string, params?: any, options?: any): Promise<T> {
    const api = await getScorix();
    return api.invoke(method, params, options);
  },
  
  async emit(topic: string, data?: any): Promise<void> {
    const api = await getScorix();
    await api.emit(topic, data);
  },
  
  on(topic: string, callback: (data: any, error?: string) => void): () => void {
    if (typeof window === "undefined") return () => {};

    // window.scorix.on returns Promise<unsubscribe> (orchestrator _call is async) — normalize to sync cleanup.
    let cancelled = false;
    let cleanup: (() => void) | null = null;

    Promise.resolve(window.scorix ? window.scorix.on(topic, callback) : getScorix().then(api => api.on(topic, callback)))
      .then(result => {
        if (cancelled) {
          if (typeof result === "function") result();
          return;
        }
        cleanup = typeof result === "function" ? result : null;
      })
      .catch(console.error);

    return () => {
      cancelled = true;
      if (cleanup) cleanup();
    };
  },
  
  resolve(name: string, handler: (data: any) => any): void {
    getScorix().then(api => api.resolve(name, handler)).catch(console.error);
  },
  
  async init(options?: any): Promise<void> {
    const api = await getScorix();
    return api.init(options);
  },
};

export default scorix;
