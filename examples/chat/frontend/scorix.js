const scorix = {
    _resolveHandlers: {},

    async invoke(method, params) {
        const envelope = { type: "invoke", payload: { method, params } }
        const result = await window.__scorix_bind_invoke?.(JSON.stringify(envelope))

        if (result?.error) {
            throw new Error(result.error)
        }

        return result
    },

    _resolve(name, params) {
        const handler = scorix._resolveHandlers[name]
        if (!handler) return
        try {
            return handler(params)
        } catch (e) {
            console.error("Resolve handler error:", e)
        }
    },

    async emit(topic, data) {
        const envelope = { type: "event", payload: { name: topic, data } }
        await window.__scorix_bind_invoke?.(JSON.stringify(envelope))
    },

    _dispatch(data) {
        try {
            const msg = data
            const event = new CustomEvent("scorix:" + msg.payload.name, {
                detail: msg.payload.data,
            })
            window.dispatchEvent(event)
        } catch (e) {
            console.error(e)
        }
    },

    onResolve(name, handler) {
        scorix._resolveHandlers[name] = handler
    },

    on(topic, callback) {
        const handler = (e) => callback(e.detail)
        window.addEventListener("scorix:" + topic, handler)
        return () => window.removeEventListener("scorix:" + topic, handler)
    },
}

// gắn global
if (typeof window !== "undefined") {
    window.scorix = scorix
}

export default scorix
