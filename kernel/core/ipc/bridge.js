window.scorix = {
    _resolveHandlers: {},

    // 1. JS → Go: invoke (SYNC)
    invoke: function (method, params) {
        const envelope = {type: "invoke", payload: {method, params}};
        return window.__scorix_bind_invoke(JSON.stringify(envelope));
    },

    // 2. Go → JS: resolve (SYNC) – Go gọi JS handler
    _resolve: function (name, params) {
        const handler = this._resolveHandlers[name];
        if (handler) {
            try {
                return handler(params);
            } catch (e) {
            }
        }
    },

    // 3. JS → Go: event (ASYNC)
    emit: function (topic, data) {
        const envelope = {type: "event", payload: {name: topic, data}};
        window.__scorix_bind_invoke(JSON.stringify(envelope));
    },

    // 4. Go → JS: event (ASYNC)
    _dispatch: function (data) {
        try {
            const msg = JSON.parse(data);
            const event = new CustomEvent('scorix:' + msg.name, {detail: msg.data});
            window.dispatchEvent(event);
        } catch (e) {
        }
    },

    // JS: register resolve handler
    onResolve: function (name, handler) {
        this._resolveHandlers[name] = handler;
    },

    // JS: subscribe (event)
    on: function (topic, callback) {
        const handler = (e) => callback(e.detail);
        window.addEventListener('scorix:' + topic, handler);
        return () => window.removeEventListener('scorix:' + topic, handler);
    }
};