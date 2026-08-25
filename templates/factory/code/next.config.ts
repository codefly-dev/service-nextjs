import { fileURLToPath } from "node:url";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  reactCompiler: true,
  experimental: {
    // Cap worker fan-out under `codefly run` — see agents/services/nextjs/runtime.go
    cpus: 1,
    workerThreads: false,
  },
  turbopack: {
    // Pin the watch/resolution root to this service directory. Left to infer
    // it, Turbopack walks up to an outer lockfile and watches the whole parent
    // tree — under codefly that tree is a lazybox per-task worktree git keeps
    // rewriting, so the watcher wakes on every FS event and pegs a CPU core.
    root: fileURLToPath(new URL(".", import.meta.url)),
  },
};

export default nextConfig;
