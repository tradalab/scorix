#if canImport(UIKit)
import ScorixGo
import UIKit
import WebKit

final class GoBridge: GoSide {
    func start(environ: Data) { with(environ) { scorixStart($0, $1) } }
    func message(_ data: Data) { with(data) { scorixMessage($0, $1) } }
    func callReply(_ data: Data) { with(data) { scorixCallReply($0, $1) } }
    func runTasks() { scorixRunTasks() }
    func openURL(_ url: String) { with(Data(url.utf8)) { scorixOpenURL($0, $1) } }
    func lifecycle(_ code: Int) { scorixLifecycle(Int32(code)) }

    func keepsOut(_ url: String, clicked: Bool) -> Bool {
        var out: Int32 = 0
        with(Data(url.utf8)) { out = scorixKeepsOut($0, $1, clicked ? 1 : 0) }
        return out != 0
    }

    func request(_ packed: Data) -> Data {
        var result = Data()
        with(packed) { ptr, n in
            var len: Int32 = 0
            guard let out = scorixRequest(ptr, n, &len) else { return }
            result = Data(bytes: out, count: Int(len))
            scorixFree(out)
        }
        return result
    }

    private func with(_ data: Data, _ body: (UnsafeMutablePointer<CChar>?, Int32) -> Void) {
        var copy = [CChar](repeating: 0, count: max(data.count, 1))
        data.withUnsafeBytes { raw in
            for (i, b) in raw.enumerated() {
                copy[i] = CChar(bitPattern: b)
            }
        }
        copy.withUnsafeMutableBufferPointer { body($0.baseAddress, Int32(data.count)) }
    }
}

private let hostCallback: scorix_host_fn = { op, data, len in
    let bytes = data.map { Data(bytes: $0, count: Int(len)) } ?? Data()
    Scorix.shared.core.fromGo(op: Int(op), data: bytes)
}

/// The app's way in: call start from application(_:didFinishLaunchingWithOptions:) and show
/// controller as the root view controller.
public final class Scorix {
    public static let shared = Scorix()

    let core = HostCore(go: GoBridge())
    public let controller = ScorixViewController()
    private var started = false

    private init() {
        controller.host = self
        core.surface = controller
    }

    /// Starts the Go side once per process; plugins replace host plugins of the same name.
    public func start(plugins: [ScorixPlugin] = []) {
        guard !started else { return }
        started = true
        core.register(hostPlugins() + plugins)
        scorixSetHost(hostCallback)
        core.goStart(environ: Self.environ())
        observeLifecycle()
    }

    /// Sends name to the page's scorix.on listeners without going through Go.
    public func emit(_ name: String, _ data: Any? = nil) { core.emit(name, data) }

    /// Hands a URL the app was opened with to Go (app.protocols decides what it means).
    public func open(_ url: URL) { core.goOpenURL(url.absoluteString) }

    var webView: WKWebView? { controller.webView }

    static func environ() -> Data {
        let fm = FileManager.default
        let support = fm.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let caches = fm.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        try? fm.createDirectory(at: support, withIntermediateDirectories: true)
        return environLines([
            ("HOME", support.path),
            ("XDG_DATA_HOME", support.path),
            ("XDG_CONFIG_HOME", support.appendingPathComponent("config").path),
            ("XDG_CACHE_HOME", caches.path),
            ("TMPDIR", NSTemporaryDirectory()),
        ])
    }

    private func observeLifecycle() {
        let nc = NotificationCenter.default
        nc.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main) { [weak self] _ in
            self?.core.goLifecycle(Life.background)
            self?.core.allPlugins.forEach { $0.onStop() }
        }
        nc.addObserver(forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main) { [weak self] _ in
            self?.core.goLifecycle(Life.foreground)
            self?.core.allPlugins.forEach { $0.onStart() }
        }
        nc.addObserver(forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main) { [weak self] _ in
            self?.core.goLifecycle(Life.lowMemory)
        }
    }
}

public final class ScorixViewController: UIViewController, PageSurface, WKScriptMessageHandler, WKNavigationDelegate, WKURLSchemeHandler {
    weak var host: Scorix?
    private(set) var webView: WKWebView?
    private var spec: WindowSpec?
    private var theme = "system"
    private var running = Set<ObjectIdentifier>()

    public override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .systemBackground
        if let s = spec, webView == nil {
            build(s)
        }
    }

    func open(_ spec: WindowSpec) {
        self.spec = spec
        theme = spec.theme
        title = spec.title
        if isViewLoaded {
            build(spec)
        }
    }

    private func build(_ s: WindowSpec) {
        webView?.removeFromSuperview()
        let config = WKWebViewConfiguration()
        if let scheme = URL(string: s.url)?.scheme, !["http", "https"].contains(scheme.lowercased()) {
            config.setURLSchemeHandler(self, forURLScheme: scheme)
        }
        config.userContentController.add(WeakHandler(self), name: "scorix")
        for js in s.initScripts where !js.isEmpty {
            config.userContentController.addUserScript(WKUserScript(source: js, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        }
        let wv = WKWebView(frame: view.bounds, configuration: config)
        wv.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        wv.navigationDelegate = self
        wv.allowsBackForwardNavigationGestures = false
        if #available(iOS 16.4, *) {
            wv.isInspectable = s.devTools
        }
        view.addSubview(wv)
        webView = wv
        applyTheme()
        if let url = URL(string: s.url) {
            wv.load(URLRequest(url: url))
        }
    }

    func evaluate(_ js: String) { webView?.evaluateJavaScript(js, completionHandler: nil) }

    func navigate(_ url: String) {
        if let u = URL(string: url) {
            webView?.load(URLRequest(url: u))
        }
    }

    func loadHTML(_ html: String, origin: String) { webView?.loadHTMLString(html, baseURL: URL(string: origin + "/")) }

    func showTitle(_ title: String) { self.title = title }

    func addInitScript(_ js: String) {
        webView?.configuration.userContentController.addUserScript(WKUserScript(source: js, injectionTime: .atDocumentStart, forMainFrameOnly: true))
    }

    func setTheme(_ theme: String) {
        self.theme = theme
        applyTheme()
    }

    func finish() {}

    private func applyTheme() {
        let style: UIUserInterfaceStyle
        switch theme {
        case "dark": style = .dark
        case "light": style = .light
        default: style = .unspecified
        }
        overrideUserInterfaceStyle = style
        view.window?.overrideUserInterfaceStyle = style
    }

    public func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let s = spec, originOf(message.frameInfo.securityOrigin) == s.origin else { return }
        guard let text = message.body as? String, !text.isEmpty else { return }
        host?.core.goMessage(Data(text.utf8))
    }

    public func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping @MainActor @Sendable (WKNavigationActionPolicy) -> Void) {
        guard action.targetFrame?.isMainFrame ?? true, let url = action.request.url?.absoluteString,
              let host = host, host.core.goKeepsOut(url, clicked: action.navigationType == .linkActivated) else {
            decisionHandler(.allow)
            return
        }
        decisionHandler(.cancel)
    }

    public func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        if let s = spec {
            build(s)
        }
    }

    public func webView(_ webView: WKWebView, start task: WKURLSchemeTask) {
        let id = ObjectIdentifier(task)
        running.insert(id)
        let req = task.request
        let packed = packRequest(method: req.httpMethod ?? "GET", url: req.url?.absoluteString ?? "", headers: req.allHTTPHeaderFields ?? [:])
        let core = host?.core
        DispatchQueue.global(qos: .userInitiated).async {
            let out = core?.goRequest(packed) ?? Data("500 Internal Server Error\n\n".utf8)
            let res = PackedResponse(out)
            DispatchQueue.main.async {
                guard self.running.remove(id) != nil, let url = req.url else { return }
                var headers = res.headers
                headers["Content-Type"] = res.charset.map { "\(res.mimeType); charset=\($0)" } ?? res.mimeType
                let response = HTTPURLResponse(url: url, statusCode: res.status, httpVersion: "HTTP/1.1", headerFields: headers)
                    ?? URLResponse(url: url, mimeType: res.mimeType, expectedContentLength: res.body.count, textEncodingName: res.charset)
                task.didReceive(response)
                task.didReceive(res.body)
                task.didFinish()
            }
        }
    }

    public func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) {
        running.remove(ObjectIdentifier(task))
    }

    private func originOf(_ o: WKSecurityOrigin) -> String {
        o.port == 0 ? "\(o.protocol)://\(o.host)" : "\(o.protocol)://\(o.host):\(o.port)"
    }
}

private final class WeakHandler: NSObject, WKScriptMessageHandler {
    weak var target: WKScriptMessageHandler?

    init(_ target: WKScriptMessageHandler) { self.target = target }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(controller, didReceive: message)
    }
}
#endif
