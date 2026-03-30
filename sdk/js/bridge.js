(function() {
  /**
   * Scorix Bridge Orchestrator
   * This file decides whether to use AppBridge (WebView) or WebBridge (WebSocket)
   * based on the current environment.
   */
  const scorix = {
    _bridge: null,

    async init(options = {}) {
      // 1. Detect environment
      if (window.__scorix__ipc_emit) {
        console.debug("Scorix: App Mode detected (WebView)");
        // AppBridge should be loaded
        this._bridge = typeof AppBridge !== 'undefined' ? AppBridge : window.scorix;
      } else {
        console.debug("Scorix: Web Mode detected (WebSocket)");
        // WebBridge should be loaded
        this._bridge = typeof WebBridge !== 'undefined' ? WebBridge : window.scorix;
      }

      // 2. Initialize the chosen bridge
      if (this._bridge && this._bridge !== this && typeof this._bridge.init === 'function') {
        return this._bridge.init(options);
      }
      
      return Promise.resolve();
    },

    invoke(method, params, options) {
      if (!this._bridge) throw new Error("Scorix: Bridge not initialized. Call init() first.");
      return this._bridge.invoke(method, params, options);
    },

    emit(topic, data) {
      if (!this._bridge) throw new Error("Scorix: Bridge not initialized. Call init() first.");
      return this._bridge.emit(topic, data);
    },

    on(topic, callback) {
      if (!this._bridge) throw new Error("Scorix: Bridge not initialized. Call init() first.");
      return this._bridge.on(topic, callback);
    },

    resolve(name, handler) {
      if (!this._bridge) throw new Error("Scorix: Bridge not initialized. Call init() first.");
      return this._bridge.resolve(name, handler);
    }
  };

  if (typeof window !== "undefined") {
    window.scorix = scorix;
  }
})();
