
/** Copies text on the user's machine: the module on desktop, the page's clipboard in web mode. Rejects when nothing copied. */
export async function writeClipboard(text: string): Promise<void> {
  if (!isWebMode() && !(await webModeReady())) {
    await clipboard.write({ text });
    return;
  }
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      copyBySelection(text);
      return;
    }
  }
  copyBySelection(text);
}

function copyBySelection(text: string): void {
  const el = document.createElement("textarea");
  el.value = text;
  el.setAttribute("readonly", "");
  el.style.position = "fixed";
  el.style.opacity = "0";
  document.body.appendChild(el);
  el.select();
  const copied = document.execCommand("copy");
  el.remove();
  if (!copied) throw new Error("the browser refused to copy");
}
