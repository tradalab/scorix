
export const store = {
  get: (params: Record<string, unknown>) => scorix.invoke<unknown>("mod:store:Get", params),
  set: (params: Record<string, unknown>) => scorix.invoke<unknown>("mod:store:Set", params),
  delete: (params: Record<string, unknown>) => scorix.invoke<unknown>("mod:store:Delete", params),
  keys: () => scorix.invoke<unknown>("mod:store:Keys"),
};
