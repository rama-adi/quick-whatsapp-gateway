// Login (email + password). Surface agent: auth.
// On success: cookie session is set by Authula; we redirect into the app shell.
// If the account has TOTP enabled, Authula returns {totp_redirect:true} (no
// session yet) and we forward to /2fa to complete the challenge.

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Link, redirect, useNavigate, useSearchParams } from "react-router";
import { Loader2 } from "lucide-react";

import type { Route } from "./+types/login";

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
import { isApiError } from "~/lib/api/envelope";
import { loadSession } from "~/lib/auth/session";
import {
  isTotpRedirect,
  resolvePostAuthRedirect,
  safeNext,
  signInRequest,
} from "./_shared";

export async function clientLoader() {
  // Already signed in? Skip the form.
  const session = await loadSession();
  if (session) {
    throw redirect(await resolvePostAuthRedirect());
  }
  return { userPanelEnabled: await probePanel() };
}

async function probePanel(): Promise<boolean> {
  // Self-registration is exposed only when the user panel is enabled.
  try {
    const res = await fetch("/auth/email-password/sign-up", {
      method: "OPTIONS",
      credentials: "include",
    });
    return res.status !== 404;
  } catch {
    return true;
  }
}

const schema = z.object({
  email: z.string().min(1, "Email is required").email("Enter a valid email"),
  password: z.string().min(1, "Password is required"),
});
type FormValues = z.infer<typeof schema>;

export default function Login({ loaderData }: Route.ComponentProps) {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [formError, setFormError] = useState<string | null>(null);

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: "", password: "" },
  });

  const onSubmit = async (values: FormValues): Promise<void> => {
    setFormError(null);
    try {
      const result = await signInRequest(values);
      if (isTotpRedirect(result)) {
        const next = safeNext(searchParams.get("next"));
        navigate(next ? `/2fa?next=${encodeURIComponent(next)}` : "/2fa", {
          replace: true,
        });
        return;
      }
      const dest = safeNext(searchParams.get("next")) ?? (await resolvePostAuthRedirect());
      navigate(dest, { replace: true });
    } catch (err) {
      setFormError(toMessage(err));
    }
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
              {submitting && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
              Sign in
            </Button>
            {loaderData.userPanelEnabled && (
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

function toMessage(err: unknown): string {
  if (isApiError(err)) {
    if (err.isUnauthorized) return "Incorrect email or password.";
    if (err.code === "rate_limited") return "Too many attempts. Please wait and try again.";
    return err.message || "Sign in failed.";
  }
  return "Unable to reach the server. Check your connection and try again.";
}
