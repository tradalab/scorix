"use client";

import { useEffect, useState } from "react";
import { {{ (index .Services 0).Package }} } from "@/api";

export default function Home() {
  const [status, setStatus] = useState<string>("Initializing...");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const checkHealth = async () => {
      try {
        const reply = await {{ (index .Services 0).Package }}.{{ (index (index .Services 0).RPCs 0).MethodName | lowerFirst }}({});
        setStatus((reply as any).status || "Running Smoothly");
      } catch (err: any) {
        console.error("Scorix Connection Error:", err);
        setError(err.message || "Failed to connect to backend");
        setStatus("System Error");
      }
    };

    checkHealth();
  }, []);

  return (
    <main className="min-h-screen bg-slate-950 text-slate-50 flex flex-col items-center justify-center p-6 selection:bg-blue-500/30">
      {/* Background Glow */}
      <div className="absolute inset-0 overflow-hidden pointer-events-none">
        <div className="absolute -top-[25%] -left-[10%] w-[70%] h-[70%] bg-blue-600/10 blur-[120px] rounded-full" />
        <div className="absolute -bottom-[25%] -right-[10%] w-[70%] h-[70%] bg-purple-600/10 blur-[120px] rounded-full" />
      </div>

      <div className="relative z-10 w-full max-w-2xl text-center space-y-8">
        <div className="space-y-4">
          <h1 className="text-6xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-b from-white to-slate-400">
            {{ .Proto.Package }}
          </h1>
          <p className="text-slate-400 text-lg max-w-md mx-auto">
            Your Scorix-powered application is ready for takeoff. Built with Go, Next.js, and high-performance IPC.
          </p>
        </div>

        <div className="flex flex-col items-center gap-4">
          <div className={`px-6 py-3 rounded-2xl border transition-all duration-500 flex items-center gap-3 ${error
            ? "bg-red-500/10 border-red-500/20 text-red-400"
            : "bg-slate-900/50 border-slate-800 text-slate-300"
            }`}>
            <div className={`w-2 h-2 rounded-full animate-pulse ${error ? "bg-red-500" : "bg-emerald-500"
              }`} />
            <span className="font-medium tracking-wide uppercase text-xs">
              System Status: <span className="text-white ml-1">{status}</span>
            </span>
          </div>

          {error && (
            <p className="text-red-500/60 text-sm font-mono">
              [DEBUG] {error}
            </p>
          )}
        </div>

        <div className="grid grid-cols-1 md:grid-cols-1 gap-4 mt-12">
          <div className="p-6 text-left bg-slate-900/40 border border-slate-800 rounded-3xl opacity-60">
            <h3 className="text-lg font-semibold">Project Root &rarr;</h3>
            <p className="mt-2 text-sm text-slate-500 leading-relaxed">
              Start by editing <code className="text-slate-300">proto/app.proto</code> to define your services.
            </p>
          </div>
        </div>
      </div>

      <footer className="absolute bottom-8 text-slate-600 text-xs tracking-widest uppercase font-medium">
        Powered by Scorix
      </footer>
    </main>
  );
}
