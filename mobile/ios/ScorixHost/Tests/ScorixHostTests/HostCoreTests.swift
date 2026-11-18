import Foundation
import XCTest
@testable import ScorixHost

final class FakeGo: GoSide {
    var replies: [[String: Any]] = []
    var ranTasks = 0
    func start(environ: Data) {}
    func message(_ data: Data) {}
    func callReply(_ data: Data) {
        replies.append((try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:])
    }
    func runTasks() { ranTasks += 1 }
    func openURL(_ url: String) {}
    func keepsOut(_ url: String, clicked: Bool) -> Bool { false }
    func lifecycle(_ code: Int) {}
    func request(_ packed: Data) -> Data { Data() }
}

final class FakeSurface: PageSurface {
    var opened: WindowSpec?
    var evaluated: [String] = []
    var titles: [String] = []
    var scripts: [String] = []
    var themes: [String] = []
    var finished = false
    var html: [(String, String)] = []
    func open(_ spec: WindowSpec) { opened = spec }
    func evaluate(_ js: String) { evaluated.append(js) }
    func navigate(_ url: String) {}
    func loadHTML(_ html: String, origin: String) { self.html.append((html, origin)) }
    func showTitle(_ title: String) { titles.append(title) }
    func addInitScript(_ js: String) { scripts.append(js) }
    func setTheme(_ theme: String) { themes.append(theme) }
    func finish() { finished = true }
}

final class SlowEcho: ScorixPlugin {
    let name = "echo"
    var held: PluginCall?
    var canceled = false
    func call(method: String, args: [String: Any], call: PluginCall) {
        if method == "later" {
            held = call
            call.onCancel { self.canceled = true }
            return
        }
        call.resolve(args)
    }
}

final class HostCoreTests: XCTestCase {
    func core() -> (HostCore, FakeGo, FakeSurface) {
        let go = FakeGo()
        let s = FakeSurface()
        let c = HostCore(go: go)
        c.surface = s
        return (c, go, s)
    }

    func op(_ c: HostCore, _ op: Int, _ text: String) { c.handle(op: op, data: Data(text.utf8)) }

    func testOpenCarriesTheSpecGoSentAndASystemThemeByDefault() {
        let (c, _, s) = core()
        op(c, Op.open, #"{"url":"scorix://app/index.html","origin":"scorix://app","title":"T","initScript":"init()","devTools":true,"secure":true}"#)
        XCTAssertEqual(s.opened?.url, "scorix://app/index.html")
        XCTAssertEqual(s.opened?.origin, "scorix://app")
        XCTAssertEqual(s.opened?.initScripts, ["init()"])
        XCTAssertEqual(s.opened?.theme, "system")
        XCTAssertTrue(s.opened?.secure ?? false)
    }

    func testAPostReachesThePageAsAStringTheBridgeParses() throws {
        let (c, _, s) = core()
        op(c, Op.post, #"{"id":"1","state":"done","data":"a\"b"}"#)
        let js = try XCTUnwrap(s.evaluated.first)
        XCTAssertTrue(js.hasPrefix("window.__scorix_receive && window.__scorix_receive(\""))
        let arg = String(js.dropFirst("window.__scorix_receive && window.__scorix_receive(".count).dropLast())
        let decoded = try JSONSerialization.jsonObject(with: Data(arg.utf8), options: [.fragmentsAllowed]) as? String
        XCTAssertEqual(decoded, #"{"id":"1","state":"done","data":"a\"b"}"#)
    }

    func testWakeRunsGosQueuedTasks() {
        let (c, go, _) = core()
        op(c, Op.wake, "")
        XCTAssertEqual(go.ranTasks, 1)
    }

    func testTitleThemeScriptsAndFinishGoToTheSurface() {
        let (c, _, s) = core()
        op(c, Op.open, #"{"url":"u","origin":"scorix://app"}"#)
        op(c, Op.setTitle, "New")
        op(c, Op.setTheme, "dark")
        op(c, Op.initScript, "more()")
        op(c, Op.loadHTML, "<p>x</p>")
        op(c, Op.finish, "")
        XCTAssertEqual(s.titles, ["New"])
        XCTAssertEqual(s.themes, ["dark"])
        XCTAssertEqual(s.scripts, ["more()"])
        XCTAssertEqual(s.html.first?.1, "scorix://app")
        XCTAssertTrue(s.finished)
        XCTAssertEqual(c.spec?.theme, "dark")
    }

    func testACallReachesItsPluginAndTheReplyGoesBackWithTheId() {
        let (c, go, _) = core()
        c.register([SlowEcho()])
        op(c, Op.call, #"{"id":42,"plugin":"echo","method":"now","args":{"x":1}}"#)
        XCTAssertEqual(go.replies.count, 1)
        XCTAssertEqual((go.replies[0]["id"] as? NSNumber)?.int64Value, 42)
        XCTAssertEqual(go.replies[0]["ok"] as? Bool, true)
        XCTAssertEqual((go.replies[0]["data"] as? [String: Any])?["x"] as? Int, 1)
        XCTAssertEqual(c.pendingCalls, 0)
    }

    func testACallToAMissingPluginRejectsWithNoPlugin() {
        let (c, go, _) = core()
        op(c, Op.call, #"{"id":1,"plugin":"ghost","method":"m"}"#)
        XCTAssertEqual(go.replies.first?["code"] as? String, "no_plugin")
    }

    func testCancelRunsThePluginsHandlerAndNoReplyFollows() {
        let (c, go, _) = core()
        let p = SlowEcho()
        c.register([p])
        op(c, Op.call, #"{"id":7,"plugin":"echo","method":"later"}"#)
        XCTAssertEqual(c.pendingCalls, 1)
        op(c, Op.callCancel, #"{"id":7}"#)
        XCTAssertTrue(p.canceled)
        p.held?.resolve("late")
        XCTAssertTrue(go.replies.isEmpty)
        XCTAssertEqual(c.pendingCalls, 0)
    }

    func testAPackedResponseFromGoReadsBackIntoStatusTypeAndBody() {
        let r = PackedResponse(Data("206 Partial Content\nContent-Type: text/plain; charset=utf-8\nContent-Range: bytes 0-1/5\n\nhi\n\nthere".utf8))
        XCTAssertEqual(r.status, 206)
        XCTAssertEqual(r.reason, "Partial Content")
        XCTAssertEqual(r.mimeType, "text/plain")
        XCTAssertEqual(r.charset, "utf-8")
        XCTAssertEqual(r.headers["Content-Range"], "bytes 0-1/5")
        XCTAssertEqual(String(decoding: r.body, as: UTF8.self), "hi\n\nthere")
        let empty = PackedResponse(Data("404 Not Found\n\n".utf8))
        XCTAssertEqual(empty.status, 404)
        XCTAssertEqual(empty.body, Data())
    }

    func testARequestIsPackedTheWayGoReadsIt() {
        let packed = packRequest(method: "GET", url: "scorix://app/a.txt", headers: ["Range": "bytes=0-1"])
        XCTAssertEqual(String(decoding: packed, as: UTF8.self), "GET scorix://app/a.txt\nRange: bytes=0-1\n")
    }

    func testEmitWrapsAnEventEnvelopeForTheBridge() throws {
        let (c, _, s) = core()
        c.emit("timer:state", ["left": 3])
        let js = try XCTUnwrap(s.evaluated.first)
        XCTAssertTrue(js.contains(#"\"kind\":\"event\""#))
        XCTAssertTrue(js.contains(#"\"name\":\"timer:state\""#))
    }
}
