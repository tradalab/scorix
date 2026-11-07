
export const store = {
  get: (params: Record<string, unknown>) => scorix.invoke<unknown>("mod:store:Get", params),
  set: (params: Record<string, unknown>) => scorix.invoke<void>("mod:store:Set", params),
  delete: (params: Record<string, unknown>) => scorix.invoke<void>("mod:store:Delete", params),
  keys: () => scorix.invoke<string[]>("mod:store:Keys"),
};
