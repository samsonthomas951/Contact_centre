// NextAuth v5 ships handlers we re-export here. The catch-all route
// covers /api/auth/signin, /api/auth/callback/zitadel, /api/auth/signout, etc.
import { handlers } from "@/lib/auth";
export const { GET, POST } = handlers;
