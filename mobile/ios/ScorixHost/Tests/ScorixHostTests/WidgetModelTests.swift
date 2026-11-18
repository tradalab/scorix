import Foundation
import XCTest
@testable import ScorixWidgets

final class WidgetModelTests: XCTestCase {
    let timeline = Timeline_([
        "url": "app://today",
        "entries": [["at_ms": 2000, "title": "later"], ["at_ms": 1000, "title": "first", "rows": [["label": "a", "value": "1"]]]],
        "expires_ms": 3000,
        "fallback": ["title": "open the app"],
    ])

    func testTheEntryShownIsTheLastWhoseTimeHasCome() {
        XCTAssertEqual(timeline.shown(at: 1500).title, "first")
        XCTAssertEqual(timeline.shown(at: 2000).title, "later")
        XCTAssertEqual(timeline.shown(at: 1500).rows.first?.value, "1")
    }

    func testBeforeTheFirstEntryAndAfterExpiryTheFallbackShows() {
        XCTAssertEqual(timeline.shown(at: 500).title, "open the app")
        XCTAssertEqual(timeline.shown(at: 3000).title, "open the app")
    }

    func testTheWidgetRedrawsAtEachLaterEntryAndAtExpiry() {
        XCTAssertEqual(timeline.changes(after: 1500), [2000, 3000])
        XCTAssertEqual(timeline.changes(after: 3500), [])
    }
}
