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
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
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

const searchSchema = z.object({
  next: z.string().optional(),
});

export const Route = createFileRoute("/_auth/login")({
  validateSearch: searchSchema,
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

  return (
    <Card>
      <CardHeader>
        <CardTitle>Sign in</CardTitle>
        <CardDescription>Use your email and password to continue.</CardDescription>
      </CardHeader>
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} noValidate>
          <CardContent className="space-y-4">
            {formError && (
              <div
                role="alert"
                className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
              >
                {formError}
              </div>
            )}
            <FormField
              control={form.control}
              name="email"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Email</FormLabel>
                  <FormControl>
                    <Input
                      type="email"
                      autoComplete="email"
                      autoFocus
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
                  <FormLabel>Password</FormLabel>
                  <FormControl>
                    <Input
                      type="password"
                      autoComplete="current-password"
                      placeholder="••••••••"
                      {...field}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </CardContent>
          <CardFooter className="flex flex-col gap-3">
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
              Sign in
            </Button>
            {canRegister && (
              <p className="text-center text-sm text-muted-foreground">
                Don&apos;t have an account?{" "}
                <Link
                  to="/register"
                  className="font-medium text-foreground underline-offset-4 hover:underline"
                >
                  Create one
                </Link>
              </p>
            )}
          </CardFooter>
        </form>
      </Form>
    </Card>
  );
}
