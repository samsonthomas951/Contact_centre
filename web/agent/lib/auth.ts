import "server-only";
import NextAuth from "next-auth";

// NextAuth v5 with the generic OIDC provider pointed at Zitadel. We
// keep the access_token on the JWT so server components can pass it
// through to the Go gateway's bearer auth.
//
// `accessToken` is added to both Session and JWT. The gateway expects
// the same JWT it would accept from a curl call, so we use NextAuth's
// raw access_token straight through.
export const { handlers, auth, signIn, signOut } = NextAuth({
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
});

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
