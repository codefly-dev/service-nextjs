import { createConnectTransport } from "@connectrpc/connect-web";

/**
 * Creates a Connect transport for a dependency service.
 *
 * Usage:
 *   import { transport } from "@/lib/connect/transport";
 *   import { MyService } from "@/gen/my_service_connect";
 *   import { createClient } from "@connectrpc/connect";
 *
 *   const client = createClient(MyService, transport("backend", "connect"));
 *   const res = await client.version({});
 *
 * The base URL comes from NEXT_PUBLIC_{SERVICE}_{API} env vars,
 * injected by codefly at runtime (e.g. NEXT_PUBLIC_BACKEND_CONNECT).
 */
export function transport(service: string, api = "connect") {
  const envKey = `NEXT_PUBLIC_${service.toUpperCase()}_${api.toUpperCase()}`;
  const baseUrl = process.env[envKey];
  if (!baseUrl) {
    throw new Error(
      `Missing env var ${envKey} — is the ${service} service running and declaring a Connect endpoint?`,
    );
  }
  return createConnectTransport({ baseUrl });
}
