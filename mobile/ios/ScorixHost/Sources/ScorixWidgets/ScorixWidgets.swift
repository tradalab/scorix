#if canImport(WidgetKit) && canImport(SwiftUI) && os(iOS)
import SwiftUI
import WidgetKit

public enum ScorixWidgetLayout: String {
    case card, clock, grid
}

struct WidgetState: TimelineEntry {
    let date: Date
    let card: Card
    let url: String
}

struct Provider: TimelineProvider {
    let kind: String

    func load() -> Timeline_ {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "ScorixAppGroup") as? String,
              let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group),
              let data = try? Data(contentsOf: dir.appendingPathComponent("widget-\(kind).json")),
              let o = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            return Timeline_()
        }
        return Timeline_(o)
    }

    func placeholder(in context: Context) -> WidgetState {
        WidgetState(date: Date(), card: Card(), url: "")
    }

    func getSnapshot(in context: Context, completion: @escaping (WidgetState) -> Void) {
        let t = load()
        completion(WidgetState(date: Date(), card: t.shown(at: Self.now()), url: t.url))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<WidgetState>) -> Void) {
        let t = load()
        let now = Self.now()
        var states = [WidgetState(date: Date(), card: t.shown(at: now), url: t.url)]
        for ms in t.changes(after: now) {
            states.append(WidgetState(date: Date(timeIntervalSince1970: Double(ms) / 1000), card: t.shown(at: ms), url: t.url))
        }
        completion(Timeline(entries: states, policy: .never))
    }

    static func now() -> Int64 { Int64(Date().timeIntervalSince1970 * 1000) }
}

struct CardView: View {
    let state: WidgetState
    let layout: ScorixWidgetLayout

    var body: some View {
        content
            .padding(12)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .widgetURL(URL(string: state.url))
    }

    @ViewBuilder
    var content: some View {
        let c = state.card
        switch layout {
        case .clock:
            VStack(alignment: .leading, spacing: 4) {
                if !c.caption.isEmpty { Text(c.caption).font(.caption).foregroundStyle(.secondary) }
                Text(c.title).font(.system(size: 34, weight: .bold)).foregroundStyle(c.accent ? Color.accentColor : .primary).minimumScaleFactor(0.5)
                if !c.text.isEmpty { Text(c.text).font(.footnote).lineLimit(2) }
            }
        case .grid:
            VStack(alignment: .leading, spacing: 4) {
                if !c.title.isEmpty { Text(c.title).font(.headline) }
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 2), count: 7), spacing: 2) {
                    ForEach(Array(c.cells.enumerated()), id: \.offset) { _, cell in
                        VStack(spacing: 0) {
                            Text(cell.text).font(.caption2.weight(cell.accent ? .bold : .regular))
                            if !cell.sub.isEmpty { Text(cell.sub).font(.system(size: 8)).foregroundStyle(.secondary) }
                        }
                        .foregroundStyle(cell.muted ? Color.secondary : (cell.accent ? Color.accentColor : Color.primary))
                    }
                }
            }
        case .card:
            VStack(alignment: .leading, spacing: 4) {
                if !c.caption.isEmpty { Text(c.caption).font(.caption).foregroundStyle(.secondary) }
                Text(c.title).font(.headline).foregroundStyle(c.accent ? Color.accentColor : .primary)
                if !c.text.isEmpty { Text(c.text).font(.subheadline).lineLimit(3) }
                ForEach(Array(c.rows.enumerated()), id: \.offset) { _, row in
                    HStack {
                        Text(row.label).font(.caption).foregroundStyle(.secondary)
                        Spacer()
                        Text(row.value).font(.caption.weight(row.accent ? .bold : .regular))
                    }
                }
            }
        }
    }
}

/// One home-screen widget fed by the app's widget module; list it in the widget extension's
/// WidgetBundle with the kind and layout scorix.yaml declares under widgets.
public struct ScorixWidget: Widget {
    let kind: String
    let label: String
    let layout: ScorixWidgetLayout

    public init(kind: String, label: String, layout: ScorixWidgetLayout = .card) {
        self.kind = kind
        self.label = label
        self.layout = layout
    }

    public var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: Provider(kind: kind)) { state in
            CardView(state: state, layout: layout).containerBackground(.background, for: .widget)
        }
        .configurationDisplayName(label)
        .supportedFamilies(layout == .grid ? [.systemLarge, .systemMedium] : [.systemSmall, .systemMedium])
    }
}
#endif
