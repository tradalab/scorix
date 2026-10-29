
export interface NotificationNotifyRequest {
  title?: string;
  text?: string;
  level?: string;
  id?: string;
  actions?: NotificationAction[];
}

export interface NotificationAction {
  key?: string;
  label?: string;
}

export interface NotificationNotifyResponse {
  clickable: boolean;
}

export interface NotificationScheduleRequest {
  items?: NotificationScheduledNotify[];
}

export interface NotificationScheduledNotify {
  at?: number;
  title?: string;
  text?: string;
  id?: string;
  actions?: NotificationAction[];
}

export const notification = {
  notify: (params?: NotificationNotifyRequest) => scorix.invoke<NotificationNotifyResponse>("mod:notification:Notify", params),
  /** Schedule replaces every pending notification, so an app recomputes its list on each start rather than tracking what it set before; an empty list cancels them all. Android only. */
  schedule: (params?: NotificationScheduleRequest) => scorix.invoke<string>("mod:notification:Schedule", params),
};
