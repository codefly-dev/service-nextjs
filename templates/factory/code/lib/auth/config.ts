import { NextAuthOptions, DefaultSession, Account, Profile } from "next-auth";
import { JWT } from "next-auth/jwt";
import Auth0Provider from "next-auth/providers/auth0";


export type SupportedAuthType = 'auth0' | 'none';

type AuthOptionsOverride = Partial<NextAuthOptions>;

function validateAuthConfig() {
    const requiredVars = {
        AUTH0_DOMAIN: process.env.AUTH0_DOMAIN!,
        AUTH0_CLIENT_ID: process.env.AUTH0_CLIENT_ID!,
        AUTH0_CLIENT_SECRET: process.env.AUTH0_CLIENT_SECRET!,
        AUTH0_AUDIENCE: process.env.AUTH0_AUDIENCE!,
    } as const;

    const missingVars = Object.entries(requiredVars)
        .filter(([_, value]) => !value)
        .map(([key]) => key);

    if (missingVars.length > 0) {
        throw new Error(`Missing required environment variables: ${missingVars.join(', ')}`);
    }

    return requiredVars;
}

async function getClientCredentialsToken() {
    try {
        const config = validateAuthConfig();
        
        console.log('Requesting token with config:', {
            domain: config.AUTH0_DOMAIN,
            clientId: config.AUTH0_CLIENT_ID.slice(0, 5) + '...',
            audience: config.AUTH0_AUDIENCE,
            hasSecret: !!config.AUTH0_CLIENT_SECRET,
        });

        const response = await fetch(`https://${config.AUTH0_DOMAIN}/oauth/token`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({
                grant_type: 'client_credentials',
                client_id: config.AUTH0_CLIENT_ID!,
                client_secret: config.AUTH0_CLIENT_SECRET!,
                audience: config.AUTH0_AUDIENCE!,
            }),
        });

        const data = await response.json();
        
        if (!response.ok) {
            console.error('Auth0 error response:', {
                status: response.status,
                statusText: response.statusText,
                data
            });
            throw new Error(`Auth0 error: ${JSON.stringify(data)}`);
        }

        return data;
    } catch (error) {
        console.error('Full error details:', {
            message: error instanceof Error ? error.message : 'Unknown error',
            error
        });
        throw error;
    }
}

const auth0Config: NextAuthOptions = {
    providers: [
        Auth0Provider({
            clientId: process.env.AUTH0_CLIENT_ID!,
            clientSecret: process.env.AUTH0_CLIENT_SECRET!,
            issuer: `https://${process.env.AUTH0_DOMAIN}`,
            authorization: {
                params: {
                    audience: process.env.AUTH0_AUDIENCE,
                    scope: 'openid profile email offline_access',
                    response_type: 'code',
                }
            }
        })
    ],
    callbacks: {
        async jwt({ token, account, profile }: {
            token: JWT & { accessTokenExpires?: number }
            account: Account | null
            profile?: Profile
        }) {
            if (account && profile) {
                return {
                    ...token,
                    accessToken: account.access_token,
                    accessTokenExpires: account.expires_at! * 1000,
                    refreshToken: account.refresh_token,
                    user: {
                        id: profile.sub,
                        email: profile.email,
                        name: profile.name,
                    },
                };
            }

            if (token.accessTokenExpires && Date.now() < token.accessTokenExpires) {
                return token;
            }

            try {
                const response = await fetch(`https://${process.env.AUTH0_DOMAIN}/oauth/token`, {
                    headers: { "Content-Type": "application/x-www-form-urlencoded" },
                    body: new URLSearchParams({
                        grant_type: "refresh_token",
                        client_id: process.env.AUTH0_CLIENT_ID!,
                        client_secret: process.env.AUTH0_CLIENT_SECRET!,
                        refresh_token: token.refreshToken as string,
                    }),
                    method: "POST",
                });

                const tokens = await response.json();
                if (!response.ok) throw tokens;

                return {
                    ...token,
                    accessToken: tokens.access_token,
                    accessTokenExpires: Date.now() + tokens.expires_in * 1000,
                    refreshToken: tokens.refresh_token ?? token.refreshToken,
                };
            } catch (error) {
                console.error("Error refreshing access token", error);
                return { ...token, error: "RefreshAccessTokenError" };
            }
        },
        async session({ session, token }: { session: any; token: any }) {
            if (!process.env.AUTH0_AUDIENCE) {
                throw new Error('AUTH0_AUDIENCE environment variable is not set');
            }
            try {
                console.log('Getting client credentials with env vars:', {
                    domain: process.env.AUTH0_DOMAIN,
                    clientId: process.env.AUTH0_CLIENT_ID?.slice(0, 5) + '...',
                    audience: process.env.AUTH0_AUDIENCE,
                });
                
                const clientCreds = await getClientCredentialsToken();
                return {
                    ...session,
                    user: token.user,
                    clientToken: clientCreds.access_token,
                };
            } catch (error) {
                console.error('Session callback error:', error);
                return {
                    ...session,
                    user: token.user,
                    error: error instanceof Error ? error.message : 'Failed to get client credentials'
                };
            }
        },
    },
    session: {
        strategy: "jwt",
    },
    pages: {
        // Remove these custom pages if you don't have them implemented
        // signIn: "/auth/signin",
        // error: "/auth/error",
    },
};

export function getAuthConfig(
    authType: SupportedAuthType,
    override?: AuthOptionsOverride
): NextAuthOptions {
    // For 'none' auth type, return a minimal config that effectively disables auth
    if (authType === 'none') {
        return {
            providers: [],
            session: { strategy: "jwt" },
            callbacks: {
                async session({ session }) {
                    return session;
                },
                async jwt({ token }) {
                    return token;
                }
            }
        };
    }

    // Auth0 specific configuration
    if (authType === 'auth0') {
        if (!process.env.AUTH0_CLIENT_ID || !process.env.AUTH0_CLIENT_SECRET || !process.env.AUTH0_DOMAIN) {
            throw new Error('Missing Auth0 configuration environment variables');
        }

        // Apply overrides if provided, but preserve the callbacks
        if (override) {
            const { callbacks, ...restOverride } = override;
            return {
                ...auth0Config,
                ...restOverride,
                callbacks: auth0Config.callbacks
            };
        }

        return auth0Config;
    }

    throw new Error(`Unsupported auth type: ${authType}`);
}
