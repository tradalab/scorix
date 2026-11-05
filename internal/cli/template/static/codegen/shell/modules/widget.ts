
export interface WidgetTimeline {
  kind?: string;
  entries?: WidgetEntry[];
  expires?: number;
  fallback?: WidgetEntry;
  url?: string;
}

export interface WidgetEntry {
  at?: number;
  caption?: string;
  title?: string;
  text?: string;
  accent?: boolean;
  rows?: WidgetRow[];
  cells?: WidgetCell[];
}

export interface WidgetRow {
  label?: string;
  value?: string;
  accent?: boolean;
}

export interface WidgetCell {
  text?: string;
  sub?: string;
  at?: number;
  muted?: boolean;
  accent?: boolean;
  url?: string;
}

export interface WidgetPin {
  kind?: string;
}

export const widget = {
  /** SetTimeline is how a widget gets content with no page running: the app hands over every entry ahead of time and refreshes them on its next start. */
  setTimeline: (params?: WidgetTimeline) => scorix.invoke<string>("mod:widget:SetTimeline", params),
  /** RequestPin opens the launcher's dialog for placing a widget of the kind; "ok" means the dialog was shown, not that the user placed it. A launcher without the dialog, or Android before 8, gives unavailable. */
  requestPin: (params?: WidgetPin) => scorix.invoke<string>("mod:widget:RequestPin", params),
};
