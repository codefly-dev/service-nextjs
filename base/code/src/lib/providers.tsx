"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import { useState, type ReactNode } from "react";
import { plugins } from "@/plugins";

export function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30 * 1000,
            retry: 1,
          },
        },
      }),
  );

  // Compose plugin providers around children
  const withPluginProviders = plugins.reduce(
    (acc, plugin) => {
      if (plugin.providers) {
        const Provider = plugin.providers;
        return <Provider>{acc}</Provider>;
      }
      return acc;
    },
    children as React.ReactNode,
  );

  return (
    <ThemeProvider
      attribute="class"
      defaultTheme="dark"
      enableSystem
      disableTransitionOnChange
    >
      <QueryClientProvider client={queryClient}>
        {withPluginProviders}
      </QueryClientProvider>
    </ThemeProvider>
  );
}
