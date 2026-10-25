
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

export const notification = {
  notify: (params?: NotificationNotifyRequest) => scorix.invoke<NotificationNotifyResponse>("mod:notification:Notify", params),
};
