import NextAuth from "next-auth";
import { getAuthConfig, SupportedAuthType } from "@/lib/auth/config";

const authType = (process.env.AUTH_TYPE as SupportedAuthType) || 'none';

// Create the auth configuration with your desired overrides
const authOptions = getAuthConfig(authType, {
  debug: true
});

// Handle authentication routes
const handler = authOptions
    ? NextAuth(authOptions)
    : () => new Response(null, { status: 404 });

// Export the handler functions for GET and POST
export { handler as GET, handler as POST };
