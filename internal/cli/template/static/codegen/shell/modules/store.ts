
export const store = {
  get: (params: Record<string, unknown>) => scorix.invoke<unknown>("mod:store:Get", params),
  set: (params: Record<string, unknown>) => scorix.invoke<void>("mod:store:Set", params),
  delete: (params: Record<string, unknown>) => scorix.invoke<void>("mod:store:Delete", params),
  keys: () => scorix.invoke<string[]>("mod:store:Keys"),
  /** All returns a deep copy of every key with its value, so a page loads its settings in one call. */
  all: () => scorix.invoke<Record<string, unknown>>("mod:store:All"),
};
