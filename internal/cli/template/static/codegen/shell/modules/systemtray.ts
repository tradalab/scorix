
export interface SystemtraySetIconRequest {
  icon?: string;
}

export interface SystemtraySetTooltipRequest {
  tooltip?: string;
}

export interface SystemtraySetTitleRequest {
  title?: string;
}

export const systemtray = {
  setIcon: (params?: SystemtraySetIconRequest) => scorix.invoke<unknown>("mod:systemtray:SetIcon", params),
  setTooltip: (params?: SystemtraySetTooltipRequest) => scorix.invoke<unknown>("mod:systemtray:SetTooltip", params),
  setTitle: (params?: SystemtraySetTitleRequest) => scorix.invoke<unknown>("mod:systemtray:SetTitle", params),
};
