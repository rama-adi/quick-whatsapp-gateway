// Login (email + password). Surface: auth. URL: /login (under the pathless
// _auth layout — the "_auth" segment is not part of the path).
//
// Ported from v1 routes/auth/login.tsx, re-fit to better-auth + TanStack Start:
//   - v1 signInRequest(authFetch) -> authClient.signIn.email({email,password})
//   - On success better-auth sets the session cookie; we resolve the role-based
//     landing route server-side (resolvePostAuthRedirect) and navigate there.
//   - If the account has TOTP enabled, better-auth returns
//     { data: { twoFactorRedirect: true } } (NO session yet) — we forward to
//     /2fa to finish the challenge (the v1 {totp_redirect:true} branch).
//   - The already-signed-in skip + the userPanelEnabled probe move to the
//     _auth layout beforeLoad and a registrationEnabled loader (§12 gate).

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import {
  Link,
  createFileRoute,
  useNavigate,
} from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { PasswordInput } from "~/components/password-input";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "~/components/ui/form";
import { authClient } from "~/lib/auth/client";
import {
  registrationEnabled,
  resolvePostAuthRedirect,
  safeNext,
  signInErrorMessage,
} from "./-shared";
import { AuthCard, FormAlert, authLinkClass } from "./-ui";
import { titleHead } from "~/lib/head";

const searchSchema = z.object({
  next: z.string().optional(),
});

export const Route = createFileRoute("/_auth/login")({
  validateSearch: searchSchema,
  head: () => titleHead("Sign in"),
  loader: async () => ({ registrationEnabled: await registrationEnabled() }),
  component: Login,
});

const schema = z.object({
  email: z.string().min(1, "Email is required").email("Enter a valid email"),
  password: z.string().min(1, "Password is required"),
});
type FormValues = z.infer<typeof schema>;

function Login() {
  const navigate = useNavigate();
  const search = Route.useSearch();
  const { registrationEnabled: canRegister } = Route.useLoaderData();
  const [formError, setFormError] = useState<string | null>(null);

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: "", password: "" },
  });

  const onSubmit = async (values: FormValues): Promise<void> => {
    setFormError(null);
    const { data, error } = await authClient.signIn.email({
      email: values.email,
      password: values.password,
    });
    if (error) {
      setFormError(signInErrorMessage(error));
      return;
    }
    // TOTP-enabled account: no session minted yet, finish on /2fa, carrying a
    // validated ?next through so the post-2FA redirect can honor it.
    if (data && (data as { twoFactorRedirect?: boolean }).twoFactorRedirect) {
      const next = safeNext(search.next) ?? undefined;
      navigate({ to: "/2fa", search: { next } });
      return;
    }
    // dest is a runtime-resolved path (role landing or validated ?next), not a
    // route literal, so use the `href` escape hatch rather than typed `to`.
    const dest = safeNext(search.next) ?? (await resolvePostAuthRedirect());
    navigate({ href: dest });
  };

  const submitting = form.formState.isSubmitting;

  const [forgotOpen, setForgotOpen] = useState(false);

  return (
    <AuthCard
      title="Sign in"
      description="Use your email and password to continue."
      footer={
        canRegister ? (
          <>
            Don&apos;t have an account?{" "}
            <Link to="/register" className={authLinkClass}>
              Create one
            </Link>
          </>
        ) : (
          <>Need an account? Ask your gateway operator to create one for you.</>
        )
      }
    >
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-5">
          {formError && <FormAlert>{formError}</FormAlert>}
          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Email</FormLabel>
                <FormControl>
                  <Input
                    type="email"
                    autoComplete="username"
                    autoFocus
                    spellCheck={false}
                    placeholder="you@example.com"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <div className="flex items-center justify-between gap-2">
                  <FormLabel>Password</FormLabel>
                  <button
                    type="button"
                    onClick={() => setForgotOpen((v) => !v)}
                    aria-expanded={forgotOpen}
                    aria-controls="forgot-password-hint"
                    className="rounded-sm text-xs font-medium text-muted-foreground underline-offset-4 outline-none hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-ring/40"
                  >
                    Forgot password?
                  </button>
                </div>
                <FormControl>
                  <PasswordInput
                    autoComplete="current-password"
                    placeholder="Your password"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
                {forgotOpen && (
                  <p
                    id="forgot-password-hint"
                    className="rounded-lg bg-muted/60 px-3 py-2 text-xs text-pretty text-muted-foreground animate-in fade-in-0 duration-200 ease-soft"
                  >
                    Password reset emails aren&apos;t available on this gateway.
                    Ask your gateway operator to set a new password for you, then
                    change it from your Account page after you sign in.
                  </p>
                )}
              </FormItem>
            )}
          />
          <Button type="submit" className="w-full" disabled={submitting}>
            {submitting && (
              <Loader2
                className="size-4 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            )}
            {submitting ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </Form>
    </AuthCard>
  );
}
