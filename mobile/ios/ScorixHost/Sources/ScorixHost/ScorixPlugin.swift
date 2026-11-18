import Foundation

/// A native plugin Go reaches with native.Call. call runs on the main thread and must end
/// every PluginCall with resolve or reject, now or later.
public protocol ScorixPlugin: AnyObject {
    var name: String { get }
    func call(method: String, args: [String: Any], call: PluginCall)
    func onStart()
    func onStop()
}

extension ScorixPlugin {
    public func onStart() {}
    public func onStop() {}
}

/// The typed side of a PluginCall, for plugins written against a generated base.
public final class Reply<T> {
    public let call: PluginCall
    private let encode: (T) -> Any

    public init(_ call: PluginCall, encode: @escaping (T) -> Any) {
        self.call = call
        self.encode = encode
    }

    public func resolve(_ value: T) { call.resolve(encode(value)) }

    public func reject(_ code: String, _ message: String) { call.reject(code, message) }
}

/// One call from Go. resolve and reject are safe from any thread; only the first one counts.
public final class PluginCall {
    let id: Int64
    public let plugin: String
    public let method: String
    private let send: ([String: Any]) -> Void
    private var done = false
    private var cancelHandler: (() -> Void)?

    init(id: Int64, plugin: String, method: String, send: @escaping ([String: Any]) -> Void) {
        self.id = id
        self.plugin = plugin
        self.method = method
        self.send = send
    }

    /// data is a dictionary, array, string, number, bool or nil.
    public func resolve(_ data: Any? = nil) { finish(["ok": true, "data": data ?? NSNull()]) }

    /// code is the stable reason Go branches on: "canceled", "unavailable".
    public func reject(_ code: String, _ message: String) { finish(["ok": false, "code": code, "error": message]) }

    /// Runs when Go abandons the call; no reply is sent after it.
    public func onCancel(_ handler: @escaping () -> Void) { onMain { self.cancelHandler = handler } }

    func cancel() {
        onMain {
            guard !self.done else { return }
            self.done = true
            self.cancelHandler?()
        }
    }

    private func finish(_ reply: [String: Any]) {
        onMain {
            guard !self.done else { return }
            self.done = true
            var body = reply
            body["id"] = self.id
            self.send(body)
        }
    }
}

func onMain(_ work: @escaping () -> Void) {
    if Thread.isMainThread {
        work()
    } else {
        DispatchQueue.main.async(execute: work)
    }
}
