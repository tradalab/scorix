
export interface LogWriteRequest {
  level?: string;
  message?: string;
  fields?: Record<string, unknown>;
}

export const log = {
  write: (params?: LogWriteRequest) => scorix.invoke<void>("mod:log:Write", params),
};
