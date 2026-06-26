// Register (self sign-up). Surface agent: auth.
// Only reachable when USER_PANEL_ENABLED — Authula exposes the sign-up route
// only then; the loader probes it and redirects to /login when disabled.
// Authula auto-signs-in on sign-up, so a session cookie is set on success.

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Link, redirect, useNavigate } from "react-router";
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
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "~/components/ui/form";
import { isApiError } from "~/lib/api/envelope";
import { loadSession } from "~/lib/auth/session";
import { resolvePostAuthRedirect, signUpRequest } from "./_shared";

export async function clientLoader() {
  const session = await loadSession();
  if (session) {
    throw redirect(await resolvePostAuthRedirect());
  }
  // Sign-up is only exposed when the user panel is enabled.
  let enabled = true;
  try {
    const res = await fetch("/auth/email-password/sign-up", {
      method: "OPTIONS",
      credentials: "include",
    });
    enabled = res.status !== 404;
  } catch {
    enabled = true;
  }
  if (!enabled) {
    throw redirect("/login");
  }
  return null;
}

const schema = z
  .object({
    name: z.string().trim().min(1, "Name is required").max(120),
    email: z.string().min(1, "Email is required").email("Enter a valid email"),
    password: z.string().min(8, "Use at least 8 characters").max(128),
    confirm: z.string().min(1, "Confirm your password"),
  })
  .refine((v) => v.password === v.confirm, {
    path: ["confirm"],
    message: "Passwords do not match",
  });
type FormValues = z.infer<typeof schema>;

export default function Register() {
  const navigate = useNavigate();
  const [formError, setFormError] = useState<string | null>(null);

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", email: "", password: "", confirm: "" },
  });

  const onSubmit = async (values: FormValues): Promise<void> => {
    setFormError(null);
    try {
      await signUpRequest({
        name: values.name,
        email: values.email,
        password: values.password,
      });
      navigate(await resolvePostAuthRedirect(), { replace: true });
    } catch (err) {
      setFormError(toMessage(err));
    }
  };

  const submitting = form.formState.isSubmitting;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Create account</CardTitle>
        <CardDescription>Set up your dashboard access.</CardDescription>
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
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Name</FormLabel>
                  <FormControl>
                    <Input autoComplete="name" autoFocus placeholder="Jane Doe" {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
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
                      autoComplete="new-password"
                      placeholder="••••••••"
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>At least 8 characters.</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name="confirm"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Confirm password</FormLabel>
                  <FormControl>
                    <Input
                      type="password"
                      autoComplete="new-password"
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
              Create account
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              Already have an account?{" "}
              <Link
                to="/login"
                className="font-medium text-foreground underline-offset-4 hover:underline"
              >
                Sign in
              </Link>
            </p>
          </CardFooter>
        </form>
      </Form>
    </Card>
  );
}

function toMessage(err: unknown): string {
  if (isApiError(err)) {
    if (err.code === "conflict") return "An account with that email already exists.";
    if (err.code === "validation_error") return err.message || "Please check the form and try again.";
    if (err.code === "rate_limited") return "Too many attempts. Please wait and try again.";
    if (err.isForbidden || err.code === "not_implemented")
      return "Self-registration is currently disabled.";
    return err.message || "Sign up failed.";
  }
  return "Unable to reach the server. Check your connection and try again.";
}
