// Register (self sign-up). Surface: auth. URL: /register (under _auth layout).
//
// Ported from v1 routes/auth/register.tsx, re-fit to better-auth + TanStack Start:
//   - v1 signUpRequest(authFetch) -> authClient.signUp.email({name,email,password})
//   - better-auth auto-signs-in on sign-up (a session cookie is set), so we go
//     straight to the role-resolved landing route. A personal org is auto-created
//     server-side (auth server databaseHooks.user.create.after, §7/§12).
//   - Gated by USER_REGISTRATION_ENABLED (§12, §14): the loader checks
//     registrationEnabled() and redirects to /login when disabled — replacing
//     v1's OPTIONS-probe. (The server also enforces it via disableSignUp.)

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import {
  Link,
  createFileRoute,
  redirect,
  useNavigate,
} from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { PasswordInput } from "~/components/password-input";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "~/components/ui/form";
import { authClient } from "~/lib/auth/client";
import {
  registrationEnabled,
  resolvePostAuthRedirect,
  signUpErrorMessage,
} from "./-shared";
import { AuthCard, FormAlert, authLinkClass } from "./-ui";
import { titleHead } from "~/lib/head";

export const Route = createFileRoute("/_auth/register")({
  head: () => titleHead("Create account"),
  loader: async () => {
    if (!(await registrationEnabled())) {
      throw redirect({ to: "/login" });
    }
  },
  component: Register,
});

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

function Register() {
  const navigate = useNavigate();
  const [formError, setFormError] = useState<string | null>(null);

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", email: "", password: "", confirm: "" },
  });

  const onSubmit = async (values: FormValues): Promise<void> => {
    setFormError(null);
    const { error } = await authClient.signUp.email({
      name: values.name,
      email: values.email,
      password: values.password,
    });
    if (error) {
      setFormError(signUpErrorMessage(error));
      return;
    }
    navigate({ to: await resolvePostAuthRedirect() });
  };

  const submitting = form.formState.isSubmitting;

  return (
    <AuthCard
      title="Create your account"
      description="You get your own organization to connect WhatsApp numbers and create API keys."
      footer={
        <>
          Already have an account?{" "}
          <Link to="/login" className={authLinkClass}>
            Sign in
          </Link>
        </>
      }
    >
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-5">
          {formError && <FormAlert>{formError}</FormAlert>}
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
            render={({ field, fieldState }) => (
              <FormItem>
                <FormLabel>Password</FormLabel>
                <FormControl>
                  <PasswordInput
                    autoComplete="new-password"
                    placeholder="At least 8 characters"
                    {...field}
                  />
                </FormControl>
                {fieldState.error ? (
                  <FormMessage />
                ) : (
                  <FormDescription className="text-xs">
                    Use 8 or more characters. A short passphrase is easier to remember
                    than random symbols.
                  </FormDescription>
                )}
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
                  <PasswordInput
                    autoComplete="new-password"
                    placeholder="Type it again"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
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
            {submitting ? "Creating account…" : "Create account"}
          </Button>
        </form>
      </Form>
    </AuthCard>
  );
}
