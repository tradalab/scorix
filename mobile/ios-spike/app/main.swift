import UIKit
import WebKit

let processStart = CFAbsoluteTimeGetCurrent()

func writeResult(_ result: [String: Any]) {
    let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
    if let data = try? JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]) {
        try? data.write(to: dir.appendingPathComponent("spike.json"))
    }
}

final class Assets: NSObject, WKURLSchemeHandler {
    func webView(_ webView: WKWebView, start task: WKURLSchemeTask) {
        guard let url = task.request.url else { return }
        var size: Int32 = 0
        let ptr = url.path.withCString { ScorixAsset(UnsafeMutablePointer(mutating: $0), &size) }
        if size < 0 || ptr == nil {
            task.didFailWithError(URLError(.fileDoesNotExist))
            return
        }
        let data = Data(bytes: ptr!, count: Int(size))
        ScorixFree(ptr)
        let mime = url.path.hasSuffix(".txt") ? "text/plain" : "text/html"
        task.didReceive(URLResponse(url: url, mimeType: mime, expectedContentLength: data.count, textEncodingName: "utf-8"))
        task.didReceive(data)
        task.didFinish()
    }

    func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) {}
}

final class Bridge: NSObject, WKScriptMessageHandlerWithReply {
    var readyMs: Double = 0

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage, replyHandler: @escaping (Any?, String?) -> Void) {
        guard let body = message.body as? [String: Any] else {
            replyHandler(nil, "not an object")
            return
        }
        switch body["kind"] as? String {
        case "ready":
            readyMs = (CFAbsoluteTimeGetCurrent() - processStart) * 1000
            replyHandler(["ok": true], nil)
        case "done":
            var result = body
            result["coldStartMs"] = Int(readyMs)
            result["bundleBytes"] = bundleBytes()
            writeResult(result)
            replyHandler(["ok": true], nil)
        case "error":
            writeResult(["error": body["detail"] ?? "unknown"])
            replyHandler(["ok": true], nil)
        default:
            guard let json = try? JSONSerialization.data(withJSONObject: body),
                  let text = String(data: json, encoding: .utf8) else {
                replyHandler(nil, "unencodable")
                return
            }
            let out = text.withCString { ScorixCall(UnsafeMutablePointer(mutating: $0)) }
            let reply = out.map { String(cString: $0) } ?? "{}"
            ScorixFree(out)
            let value = try? JSONSerialization.jsonObject(with: Data(reply.utf8))
            replyHandler(value, nil)
        }
    }

    func bundleBytes() -> Int {
        let url = Bundle.main.bundleURL
        guard let files = FileManager.default.enumerator(at: url, includingPropertiesForKeys: [.fileSizeKey]) else { return 0 }
        var total = 0
        for case let file as URL in files {
            total += (try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
        }
        return total
    }
}

final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?
    let bridge = Bridge()

    func application(_ application: UIApplication, didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        let config = WKWebViewConfiguration()
        config.setURLSchemeHandler(Assets(), forURLScheme: "scorix")
        config.userContentController.addScriptMessageHandler(bridge, contentWorld: .page, name: "go")
        let web = WKWebView(frame: UIScreen.main.bounds, configuration: config)
        let controller = UIViewController()
        controller.view = web
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.rootViewController = controller
        window.makeKeyAndVisible()
        self.window = window
        web.load(URLRequest(url: URL(string: "scorix://app/index.html")!))
        return true
    }
}

UIApplicationMain(CommandLine.argc, CommandLine.unsafeArgv, nil, NSStringFromClass(AppDelegate.self))
