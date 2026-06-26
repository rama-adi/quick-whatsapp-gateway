// Login placeholder. Stage 1 only needs this route to exist so the session
// guard's redirect({ to: "/login" }) typechecks. Stage 2 ports the real
// better-auth email/password sign-in form (v1 lives in
// app/_v1-surfaces/auth/login.tsx).

import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/login")({
  component: Login,
});

function Login() {
  return (
    <main className="container mx-auto flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
      <h1 className="text-2xl font-semibold">Sign in</h1>
      <p className="text-muted-foreground">Auth form lands in the next stage.</p>
    </main>
  );
}
