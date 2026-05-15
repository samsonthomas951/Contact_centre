import "server-only";
import NextAuth from "next-auth";
import Credentials from "next-auth/providers/credentials";
import type { NextAuthConfig } from "next-auth";

// Two backends, picked at process start by AUTH_DEMO_MODE:
//
//   AUTH_DEMO_MODE=1   -> Credentials provider, issues a stub bearer
//                          "demo:<agent>:<tenant>:<role>" the gateway
//                          accepts in DemoMode. Used by docker-compose.demo.
//
//   anything else      -> Generic OIDC provider pointed at Zitadel.
//                          Issues real JWTs the gateway verifies via
//                          OIDC discovery. Production path.
//
// Both write `accessToken` onto the JWT + Session so server components
// hand it to the gateway uniformly.

const demoMode = process.env.AUTH_DEMO_MODE === "1";

const DEMO_TENANT = "11111111-1111-1111-1111-111111111111";
const DEMO_USERS: Record<string, { id: string; name: string; role: "agent" | "supervisor" | "admin" | "dpo" }> = {
  "ada@demo.local":   { id: "22222222-2222-2222-2222-222222222222", name: "Ada Lovelace",   role: "agent" },
  "bob@demo.local":   { id: "33333333-3333-3333-3333-333333333333", name: "Bob Supervisor", role: "supervisor" },
  "carol@demo.local": { id: "44444444-4444-4444-4444-444444444444", name: "Carol Admin",    role: "admin" },
  "diana@demo.local": { id: "55555555-5555-5555-5555-555555555555", name: "Diana DPO",      role: "dpo" },
};

const productionConfig: NextAuthConfig = {
  providers: [
    {
      id: "zitadel",
      name: "Zitadel",
      type: "oidc",
      issuer: process.env.ZITADEL_ISSUER!,
      clientId: process.env.ZITADEL_CLIENT_ID!,
      clientSecret: process.env.ZITADEL_CLIENT_SECRET!,
      authorization: { params: { scope: "openid email profile offline_access" } },
    },
  ],
  session: { strategy: "jwt" },
  callbacks: {
    async jwt({ token, account }) {
      if (account?.access_token) {
        token.accessToken = account.access_token;
        token.expiresAt = account.expires_at;
        token.refreshToken = account.refresh_token;
      }
      return token;
    },
    async session({ session, token }) {
      session.accessToken = token.accessToken as string | undefined;
      return session;
    },
  },
};

const demoConfig: NextAuthConfig = {
  trustHost: true,
  session: { strategy: "jwt" },
  providers: [
    Credentials({
      name: "Demo",
      credentials: { email: { label: "Email", type: "email", placeholder: "ada@demo.local" } },
      async authorize(creds) {
        const email = String(creds?.email || "").toLowerCase().trim();
        const u = DEMO_USERS[email];
        if (!u) return null;
        const bearer = `demo:${u.id}:${DEMO_TENANT}:${u.role}`;
        // bearer rides on the User into the JWT callback below.
        return { id: u.id, name: u.name, email, bearer } as unknown as { id: string };
      },
    }),
  ],
  callbacks: {
    async jwt({ token, user }) {
      if (user) {
        token.accessToken = (user as { bearer?: string }).bearer;
      }
      return token;
    },
    async session({ session, token }) {
      session.accessToken = token.accessToken as string | undefined;
      return session;
    },
  },
};

export const { handlers, auth, signIn, signOut } = NextAuth(
  demoMode ? demoConfig : productionConfig,
);

declare module "next-auth" {
  interface Session {
    accessToken?: string;
  }
}

declare module "@auth/core/jwt" {
  interface JWT {
    accessToken?: string;
    expiresAt?: number;
    refreshToken?: string;
  }
}
