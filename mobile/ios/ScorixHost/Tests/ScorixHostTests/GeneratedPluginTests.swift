import Foundation
import XCTest
@testable import ScorixHost

final class FakeCapture: CapturePluginBase {
    func page(_ req: Empty, reply: Reply<PageImage>) {
        reply.resolve(PageImage(png: Data([1, 2, 3])))
    }
}

final class GeneratedPluginTests: XCTestCase {
    func testAMessageReadsTheKeysGoWritesAndWritesTheSameKeysBack() throws {
        let wire: [String: Any] = ["text": "123456", "sensitive": true, "clear_after_ms": 500]
        let msg = ClipWrite(json: wire)
        XCTAssertEqual(msg.text, "123456")
        XCTAssertTrue(msg.sensitive)
        XCTAssertEqual(msg.clearAfterMs, 500)
        let back = msg.toJSON()
        XCTAssertEqual(back["text"] as? String, "123456")
        XCTAssertEqual(back["sensitive"] as? Bool, true)
        XCTAssertEqual(back["clear_after_ms"] as? Int, 500)
        XCTAssertNoThrow(try JSONSerialization.data(withJSONObject: back))
    }

    func testAMissingOrNullKeyDecodesToTheZeroValue() {
        let msg = ClipWrite(json: ["text": NSNull()])
        XCTAssertEqual(msg.text, "")
        XCTAssertFalse(msg.sensitive)
        XCTAssertEqual(PageImage(json: ["png": NSNull()]).png, Data())
    }

    func testBytesTravelAsStandardBase64LikeGoEncodesThem() {
        let msg = PageImage(json: ["png": "AQID"])
        XCTAssertEqual(msg.png, Data([1, 2, 3]))
        XCTAssertEqual(msg.toJSON()["png"] as? String, "AQID")
    }

    func testRepeatedMessagesDecodeEachElement() {
        let timeline = WidgetTimeline(json: [
            "url": "app://today",
            "entries": [["at_ms": 5, "title": "a"], ["at_ms": 9, "title": "b"]],
            "expires_ms": 10,
        ])
        XCTAssertEqual(timeline.entries.map { $0.title }, ["a", "b"])
        XCTAssertEqual(timeline.entries.map { $0.atMs }, [5, 9])
        let entries = timeline.toJSON()["entries"] as? [[String: Any]]
        XCTAssertEqual(entries?.count, 2)
    }

    func testTheBaseDispatchesAMethodAndRepliesWithTheIdGoSent() {
        var sent: [String: Any] = [:]
        let plugin = FakeCapture()
        XCTAssertEqual(plugin.name, "capture")
        plugin.call(method: "page", args: [:], call: PluginCall(id: 7, plugin: "capture", method: "page") { sent = $0 })
        XCTAssertEqual(sent["id"] as? Int64, 7)
        XCTAssertEqual(sent["ok"] as? Bool, true)
        XCTAssertEqual((sent["data"] as? [String: Any])?["png"] as? String, "AQID")
    }

    func testAnUnknownMethodRejectsWithNoMethod() {
        var sent: [String: Any] = [:]
        FakeCapture().call(method: "zoom", args: [:], call: PluginCall(id: 1, plugin: "capture", method: "zoom") { sent = $0 })
        XCTAssertEqual(sent["ok"] as? Bool, false)
        XCTAssertEqual(sent["code"] as? String, "no_method")
    }

    func testOnlyTheFirstAnswerReachesGo() {
        var replies = 0
        let call = PluginCall(id: 2, plugin: "p", method: "m") { _ in replies += 1 }
        call.resolve()
        call.reject("late", "ignored")
        XCTAssertEqual(replies, 1)
    }
}
