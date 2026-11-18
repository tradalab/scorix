import Foundation

struct Row: Hashable {
    var label = "", value = "", accent = false
}

struct Cell: Hashable {
    var text = "", sub = "", atMs: Int64 = 0, muted = false, accent = false, url = ""
}

struct Card: Hashable {
    var atMs: Int64 = 0, caption = "", title = "", text = "", accent = false
    var rows: [Row] = []
    var cells: [Cell] = []

    init() {}

    init(_ o: [String: Any]) {
        atMs = (o["at_ms"] as? NSNumber)?.int64Value ?? 0
        caption = o["caption"] as? String ?? ""
        title = o["title"] as? String ?? ""
        text = o["text"] as? String ?? ""
        accent = o["accent"] as? Bool ?? false
        rows = (o["rows"] as? [[String: Any]] ?? []).map {
            Row(label: $0["label"] as? String ?? "", value: $0["value"] as? String ?? "", accent: $0["accent"] as? Bool ?? false)
        }
        cells = (o["cells"] as? [[String: Any]] ?? []).map {
            Cell(text: $0["text"] as? String ?? "", sub: $0["sub"] as? String ?? "", atMs: ($0["at_ms"] as? NSNumber)?.int64Value ?? 0,
                 muted: $0["muted"] as? Bool ?? false, accent: $0["accent"] as? Bool ?? false, url: $0["url"] as? String ?? "")
        }
    }
}

struct Timeline_: Hashable {
    var url = ""
    var entries: [Card] = []
    var expiresMs: Int64 = 0
    var fallback = Card()

    init() {}

    init(_ o: [String: Any]) {
        url = o["url"] as? String ?? ""
        entries = (o["entries"] as? [[String: Any]] ?? []).map(Card.init).sorted { $0.atMs < $1.atMs }
        expiresMs = (o["expires_ms"] as? NSNumber)?.int64Value ?? 0
        fallback = Card(o["fallback"] as? [String: Any] ?? [:])
    }

    func shown(at ms: Int64) -> Card {
        if expiresMs != 0 && ms >= expiresMs {
            return fallback
        }
        return entries.last { $0.atMs <= ms } ?? fallback
    }

    func changes(after ms: Int64) -> [Int64] {
        var points = entries.map(\.atMs).filter { $0 > ms }
        if expiresMs > ms {
            points.append(expiresMs)
        }
        return Array(Set(points)).sorted()
    }
}
