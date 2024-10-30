import { DefaultSession } from "next-auth"
import { JWT as NextAuthJWT } from "next-auth/jwt"

declare module "next-auth" {
    interface Session extends DefaultSession {
        accessToken?: string;
        error?: string;
    }
}

declare module "next-auth/jwt" {
    interface JWT extends NextAuthJWT {
        accessToken?: string;
        refreshToken?: string;
    }
} 