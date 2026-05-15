import { redirect } from "next/navigation";

// Root path lands the agent on /inbox. Middleware redirects to login
// when there's no session.
export default function Home() {
  redirect("/inbox");
}
