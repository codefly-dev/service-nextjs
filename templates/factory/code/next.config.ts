import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  experimental: {
    // Cap worker fan-out under `codefly run` — see agents/services/nextjs/runtime.go
    cpus: 1,
    workerThreads: false,
  },
};

export default nextConfig;
