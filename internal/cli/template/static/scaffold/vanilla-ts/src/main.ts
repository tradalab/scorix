import scorix from "@/lib/scorix"

const app = document.querySelector<HTMLElement>("#app")!

function render(status: string, error?: string) {
  app.innerHTML = `
    <h1>{{ .Name }}</h1>
    <p class="muted">Your Scorix-powered application is ready. Built with Go, TypeScript, and high-performance IPC.</p>
    <p class="status ${error ? "bad" : "ok"}">System status: <strong>${status}</strong></p>
    ${error ? `<pre class="debug">[DEBUG] ${error}</pre>` : ""}
    <p class="muted">Start by editing <code>idl/app.proto</code> to define your services.</p>
  `
}

render("Initializing...")

try {
  const reply = (await scorix.invoke("healthz:ping")) as { status?: string }
  render(reply.status || "Running Smoothly")
} catch (err) {
  const message = err instanceof Error ? err.message : String(err)
  console.error("Scorix Connection Error:", err)
  render("System Error", message)
}
