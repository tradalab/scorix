// Package widget feeds the Android home-screen widget; desktop reports unavailable.
package widget

import (
	"context"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/hostapi"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/native"
)

type Module struct{}

func New() *Module { return &Module{} }

func (m *Module) Name() string       { return "widget" }
func (m *Module) Version() string    { return "1.0.0" }
func (m *Module) Capability() string { return "widget" }

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[widget] loading (v%s)", m.Version()))
	module.Expose(m, "SetTimeline", ctx.IPC)
	return nil
}

func (m *Module) OnStart() error  { return nil }
func (m *Module) OnStop() error   { return nil }
func (m *Module) OnUnload() error { return nil }

// Entry is three lines, top to bottom; Title is the large one. A clock widget draws the time above
// them instead. A grid widget draws Title and Text above its Cells, with arrows that step through
// the entries; a card or clock sized large enough adds Rows below.
type Entry struct {
	// At is Unix milliseconds: the entry shows from then until a later one is due.
	At      int64  `json:"at,omitempty"`
	Caption string `json:"caption,omitempty"`
	Title   string `json:"title,omitempty"`
	Text    string `json:"text,omitempty"`
	Accent  bool   `json:"accent,omitempty"`
	Rows    []Row  `json:"rows,omitempty"`
	Cells   []Cell `json:"cells,omitempty"`
}

type Row struct {
	Label  string `json:"label,omitempty"`
	Value  string `json:"value,omitempty"`
	Accent bool   `json:"accent,omitempty"`
}

// Cell is one square of a grid, seven to a row.
type Cell struct {
	Text string `json:"text,omitempty"`
	Sub  string `json:"sub,omitempty"`
	// At is the start of the day the cell stands for, in Unix milliseconds: it is drawn as today
	// for 24 hours from then. 0 marks a header.
	At     int64 `json:"at,omitempty"`
	Muted  bool  `json:"muted,omitempty"`
	Accent bool  `json:"accent,omitempty"`
	// URL is what a tap on the cell opens the app on; empty, the tap opens the timeline's URL.
	URL string `json:"url,omitempty"`
}

type Timeline struct {
	// Kind is the id of an android.widgets entry in scorix.yaml.
	Kind    string  `json:"kind"`
	Entries []Entry `json:"entries"`
	// Expires (Unix milliseconds) ends the last entry, so a stale day is not shown as today.
	Expires int64 `json:"expires,omitempty"`
	// Fallback shows before the first entry and after Expires; empty, the widget shows the app name.
	Fallback Entry `json:"fallback"`
	// URL is what a tap opens the app on; empty opens it plainly.
	URL string `json:"url,omitempty"`
}

// Stored whole in the host's preferences and parsed on every redraw.
const maxEntries = 400

// What the layouts have room for: a large card shows four rows, a grid seven rows of seven.
const (
	maxRows  = 4
	maxCells = 49
)

// SetTimeline is how a widget gets content with no page running: the app hands over every entry
// ahead of time and refreshes them on its next start.
// JS: scorix.invoke("mod:widget:SetTimeline", { kind: "today", entries: [{ at: midnight, title: "15", text: "Rằm tháng Tám" }], expires })
func (m *Module) SetTimeline(ctx context.Context, t Timeline) (string, error) {
	if t.Kind == "" {
		return "", fault.New("invalid_request", "kind is required: the id of an android.widgets entry in scorix.yaml")
	}
	if len(t.Entries) > maxEntries {
		return "", fault.New("invalid_request", fmt.Sprintf("%d entries, over the %d limit", len(t.Entries), maxEntries))
	}
	for _, e := range append([]Entry{t.Fallback}, t.Entries...) {
		if len(e.Rows) > maxRows || len(e.Cells) > maxCells {
			return "", fault.New("invalid_request", fmt.Sprintf("an entry has %d rows and %d cells, over the %d and %d limits", len(e.Rows), len(e.Cells), maxRows, maxCells))
		}
	}
	w := &hostapi.WidgetTimeline{Kind: t.Kind, Url: t.URL, ExpiresMs: t.Expires, Fallback: entry(t.Fallback)}
	for _, e := range t.Entries {
		w.Entries = append(w.Entries, entry(e))
	}
	if _, err := hostapi.Widget.SetTimeline(ctx, w); err != nil {
		return "", mapErr(err)
	}
	return "ok", nil
}

func entry(e Entry) hostapi.WidgetEntry {
	h := hostapi.WidgetEntry{AtMs: e.At, Caption: e.Caption, Title: e.Title, Text: e.Text, Accent: e.Accent}
	for _, r := range e.Rows {
		h.Rows = append(h.Rows, hostapi.WidgetRow{Label: r.Label, Value: r.Value, Accent: r.Accent})
	}
	for _, c := range e.Cells {
		h.Cells = append(h.Cells, hostapi.WidgetCell{Text: c.Text, Sub: c.Sub, AtMs: c.At, Muted: c.Muted, Accent: c.Accent, Url: c.URL})
	}
	return h
}

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "home-screen widgets exist only on Android")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}
