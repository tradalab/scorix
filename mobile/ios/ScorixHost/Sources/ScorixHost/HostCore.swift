import Foundation

enum Op {
    static let post = 1
    static let wake = 2
    static let open = 3
    static let eval = 4
    static let navigate = 5
    static let loadHTML = 6
    static let setTitle = 7
    static let finish = 8
    static let initScript = 9
    static let call = 10
    static let callCancel = 11
    static let setTheme = 12
}

enum Life {
    static let foreground = 1
    static let background = 2
    static let lowMemory = 3
}

protocol GoSide: AnyObject {
    func start(environ: Data)
    func message(_ data: Data)
    func callReply(_ data: Data)
    func runTasks()
    func openURL(_ url: String)
    func keepsOut(_ url: String, clicked: Bool) -> Bool
    func lifecycle(_ code: Int)
    func request(_ packed: Data) -> Data
}

struct WindowSpec: Equatable {
    var url: String
    var origin: String
    var title: String
    var initScripts: [String]
    var devTools: Bool
    var secure: Bool
    var theme: String

    init(json o: [String: Any]) {
        url = o["url"] as? String ?? ""
        origin = o["origin"] as? String ?? ""
        title = o["title"] as? String ?? ""
        let first = o["initScript"] as? String ?? ""
        initScripts = first.isEmpty ? [] : [first]
        devTools = o["devTools"] as? Bool ?? false
        secure = o["secure"] as? Bool ?? false
        let t = o["theme"] as? String ?? ""
        theme = t.isEmpty ? "system" : t
    }
}

protocol PageSurface: AnyObject {
    func open(_ spec: WindowSpec)
    func evaluate(_ js: String)
    func navigate(_ url: String)
    func loadHTML(_ html: String, origin: String)
    func showTitle(_ title: String)
    func addInitScript(_ js: String)
    func setTheme(_ theme: String)
    func finish()
}

final class HostCore {
    private let go: GoSide
    weak var surface: PageSurface?
    private(set) var spec: WindowSpec?
    private var plugins: [String: ScorixPlugin] = [:]
    private var calls: [Int64: PluginCall] = [:]

    init(go: GoSide) {
        self.go = go
    }

    func goStart(environ: Data) { go.start(environ: environ) }
    func goMessage(_ data: Data) { go.message(data) }
    func goOpenURL(_ url: String) { go.openURL(url) }
    func goLifecycle(_ code: Int) { go.lifecycle(code) }
    func goKeepsOut(_ url: String, clicked: Bool) -> Bool { go.keepsOut(url, clicked: clicked) }
    func goRequest(_ packed: Data) -> Data { go.request(packed) }

    func register(_ list: [ScorixPlugin]) {
        for p in list {
            plugins[p.name] = p
        }
    }

    func plugin(named name: String) -> ScorixPlugin? { plugins[name] }

    var allPlugins: [ScorixPlugin] { Array(plugins.values) }

    func fromGo(op: Int, data: Data) {
        onMain { self.handle(op: op, data: data) }
    }

    func handle(op: Int, data: Data) {
        let text = String(decoding: data, as: UTF8.self)
        switch op {
        case Op.post:
            surface?.evaluate(Self.receive(text))
        case Op.wake:
            go.runTasks()
        case Op.open:
            guard let o = Self.object(data) else { return }
            let s = WindowSpec(json: o)
            spec = s
            surface?.open(s)
        case Op.eval:
            surface?.evaluate(text)
        case Op.navigate:
            surface?.navigate(text)
        case Op.loadHTML:
            if let s = spec { surface?.loadHTML(text, origin: s.origin) }
        case Op.setTitle:
            spec?.title = text
            surface?.showTitle(text)
        case Op.finish:
            surface?.finish()
        case Op.initScript:
            spec?.initScripts.append(text)
            surface?.addInitScript(text)
        case Op.call:
            dispatchCall(data)
        case Op.callCancel:
            if let o = Self.object(data), let id = (o["id"] as? NSNumber)?.int64Value {
                calls.removeValue(forKey: id)?.cancel()
            }
        case Op.setTheme:
            spec?.theme = text
            surface?.setTheme(text)
        default:
            break
        }
    }

    func emit(_ name: String, _ data: Any? = nil) {
        let envelope: [String: Any] = ["id": "ios-\(name)", "kind": "event", "name": name, "data": data ?? NSNull()]
        guard let json = try? JSONSerialization.data(withJSONObject: envelope) else { return }
        let text = String(decoding: json, as: UTF8.self)
        onMain { self.surface?.evaluate(Self.receive(text)) }
    }

    private func dispatchCall(_ data: Data) {
        guard let o = Self.object(data),
              let id = (o["id"] as? NSNumber)?.int64Value,
              let name = o["plugin"] as? String,
              let method = o["method"] as? String else { return }
        let call = PluginCall(id: id, plugin: name, method: method) { [weak self] reply in
            self?.reply(id, reply)
        }
        calls[id] = call
        guard let plugin = plugins[name] else {
            call.reject("no_plugin", "no native plugin named \(name)")
            return
        }
        plugin.call(method: method, args: o["args"] as? [String: Any] ?? [:], call: call)
    }

    private func reply(_ id: Int64, _ body: [String: Any]) {
        calls.removeValue(forKey: id)
        guard let data = try? JSONSerialization.data(withJSONObject: body) else { return }
        go.callReply(data)
    }

    var pendingCalls: Int { calls.count }

    static func receive(_ json: String) -> String {
        let literal = (try? JSONSerialization.data(withJSONObject: [json], options: [.fragmentsAllowed]))
            .map { String(decoding: $0, as: UTF8.self) } ?? "[\"\"]"
        let arg = literal.dropFirst().dropLast()
        return "window.__scorix_receive && window.__scorix_receive(\(arg))"
    }

    static func object(_ data: Data) -> [String: Any]? {
        (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
    }
}

struct PackedResponse: Equatable {
    var status: Int
    var reason: String
    var headers: [String: String]
    var mimeType: String
    var charset: String?
    var body: Data

    init(_ packed: Data) {
        let bytes = [UInt8](packed)
        var lines: [String] = []
        var start = 0
        var bodyStart = bytes.count
        var i = 0
        while i < bytes.count {
            if bytes[i] == 0x0A {
                let line = String(decoding: bytes[start..<i], as: UTF8.self)
                if line.isEmpty && !lines.isEmpty {
                    bodyStart = i + 1
                    break
                }
                lines.append(line)
                start = i + 1
            }
            i += 1
        }
        let statusLine = lines.first ?? ""
        let parts = statusLine.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: false)
        status = Int(parts.first ?? "") ?? 500
        reason = parts.count > 1 ? String(parts[1]) : ""
        var h: [String: String] = [:]
        var contentType = "application/octet-stream"
        for line in lines.dropFirst() {
            guard let r = line.range(of: ": ") else { continue }
            let k = String(line[..<r.lowerBound])
            let v = String(line[r.upperBound...])
            if k.caseInsensitiveCompare("Content-Type") == .orderedSame {
                contentType = v
            }
            h[k] = v
        }
        headers = h
        let mimePart = contentType.split(separator: ";").first.map(String.init) ?? contentType
        mimeType = mimePart.trimmingCharacters(in: .whitespaces)
        if let r = contentType.range(of: "charset=") {
            charset = String(contentType[r.upperBound...]).trimmingCharacters(in: .whitespaces)
        } else {
            charset = nil
        }
        body = bodyStart <= bytes.count ? Data(bytes[bodyStart...]) : Data()
    }
}

func packRequest(method: String, url: String, headers: [String: String]) -> Data {
    var head = "\(method) \(url)\n"
    for (k, v) in headers.sorted(by: { $0.key < $1.key }) {
        head += "\(k): \(v)\n"
    }
    return Data(head.utf8)
}

func environLines(_ vars: [(String, String)]) -> Data {
    Data(vars.map { "\($0.0)=\($0.1)" }.joined(separator: "\n").utf8)
}
