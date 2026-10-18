// Two-factor (TOTP) challenge. Surface: auth. URL: /2fa (under _auth layout).
//
// Ported from v1 routes/auth/totp.tsx, re-fit to better-auth + TanStack Start:
//   - Reached after signIn.email returns { twoFactorRedirect: true }. better-auth
//     holds a short-lived 2FA cookie (NOT a session yet); posting a valid code
//     completes auth and mints the real session cookie.
//   - v1 verifyTotpRequest/verifyBackupCodeRequest(authFetch) ->
//     authClient.twoFactor.verifyTotp({code, trustDevice}) /
//     authClient.twoFactor.verifyBackupCode({code, trustDevice}).
//   - "Trust this device" maps to better-auth's trustDevice flag (writes a
//     trust-device cookie, so future logins skip 2FA — the v1 affordance).
//   - On success we resolve the role-based landing route server-side and go.

import { useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "~/components/ui/form";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { Label } from "~/components/ui/label";
import { authClient } from "~/lib/auth/client";
import {
  resolvePostAuthRedirect,
  safeNext,
  verifyErrorMessage,
} from "./-shared";
import { AuthCard, FormAlert, authLinkClass } from "./-ui";
import { titleHead } from "~/lib/head";

const searchSchema = z.object({
  next: z.string().optional(),
});

export const Route = createFileRoute("/_auth/2fa")({
  validateSearch: searchSchema,
  head: () => titleHead("Two-factor authentication"),
  component: Totp,
});

type Mode = "authenticator" | "backup";

const SCHEMAS = {
  authenticator: z.object({
    code: z
      .string()
      .trim()
      .regex(/^\d{6}$/, "Enter the 6-digit code from your authenticator app"),
  }),
  backup: z.object({
    code: z.string().trim().min(1, "Enter a backup code"),
  }),
} satisfies Record<Mode, z.ZodObject<{ code: z.ZodString }>>;

type CodeValues = { code: string };

function Totp() {
  const [mode, setMode] = useState<Mode>("authenticator");

  return (
    <AuthCard
      title="Two-factor authentication"
      description="Confirm it's you to finish signing in."
      footer={
        <>
          Wrong account or lost your device?{" "}
          <a href="/login" className={authLinkClass}>
            Start over
          </a>
        </>
      }
    >
      <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
        <TabsList className="grid w-full grid-cols-2">
          <TabsTrigger value="authenticator">Authenticator app</TabsTrigger>
          <TabsTrigger value="backup">Backup code</TabsTrigger>
        </TabsList>
        <TabsContent value="authenticator" className="pt-4">
          <ChallengeForm key="authenticator" mode="authenticator" />
        </TabsContent>
        <TabsContent value="backup" className="pt-4">
          <ChallengeForm key="backup" mode="backup" />
        </TabsContent>
      </Tabs>
    </AuthCard>
  );
}

function ChallengeForm({ mode }: { mode: Mode }) {
  const navigate = useNavigate();
  const search = Route.useSearch();
  const [formError, setFormError] = useState<string | null>(null);
  const [trustDevice, setTrustDevice] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);

  const form = useForm<CodeValues>({
    resolver: zodResolver(SCHEMAS[mode]),
    defaultValues: { code: "" },
  });

  const onSubmit = async (values: CodeValues): Promise<void> => {
    setFormError(null);
    const { error } =
      mode === "authenticator"
        ? await authClient.twoFactor.verifyTotp({ code: values.code, trustDevice })
        : await authClient.twoFactor.verifyBackupCode({ code: values.code, trustDevice });
    if (error) {
      setFormError(verifyErrorMessage(error, mode));
      // Clear the wrong code so the next attempt starts fresh.
      form.setValue("code", "");
      form.setFocus("code");
      return;
    }
    // Runtime-resolved path (role landing or validated ?next), not a route
    // literal — use the `href` escape hatch rather than typed `to`.
    const dest = safeNext(search.next) ?? (await resolvePostAuthRedirect());
    navigate({ href: dest });
  };

  const submitting = form.formState.isSubmitting;
  const codeId = `${mode}-trust`;

  return (
    <Form {...form}>
      <form
        ref={formRef}
        onSubmit={form.handleSubmit(onSubmit)}
        noValidate
        className="grid gap-4"
      >
        {formError && <FormAlert>{formError}</FormAlert>}
        <FormField
          control={form.control}
          name="code"
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {mode === "authenticator" ? "6-digit code" : "Backup code"}
              </FormLabel>
              <FormControl>
                {mode === "authenticator" ? (
                  <Input
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    autoFocus
                    placeholder="000000"
                    className="h-11 text-center font-mono text-xl tracking-[0.5em] placeholder:tracking-[0.5em] placeholder:text-muted-foreground/50"
                    {...field}
                    onChange={(e) => {
                      // Accept pasted "123 456" / "123-456": keep digits only.
                      const digits = e.target.value.replace(/\D/g, "").slice(0, 6);
                      field.onChange(digits);
                      // Submit as soon as the sixth digit lands.
                      if (digits.length === 6 && !form.formState.isSubmitting) {
                        queueMicrotask(() => formRef.current?.requestSubmit());
                      }
                    }}
                  />
                ) : (
                  <Input
                    autoComplete="one-time-code"
                    autoFocus
                    spellCheck={false}
                    autoCapitalize="off"
                    placeholder="xxxxx-xxxxx"
                    className="font-mono"
                    {...field}
                  />
                )}
              </FormControl>
              <FormDescription className="text-xs">
                {mode === "authenticator"
                  ? "Open your authenticator app (Google Authenticator, 1Password, Authy …) and enter the current code for WA Gateway."
                  : "Each backup code works once. Use one if you can't reach your authenticator app."}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <div className="flex items-start gap-2">
          <input
            id={codeId}
            type="checkbox"
            checked={trustDevice}
            onChange={(e) => setTrustDevice(e.target.checked)}
            className="mt-0.5 size-4 shrink-0 rounded border-input accent-primary focus-visible:ring-2 focus-visible:ring-ring"
          />
          <Label htmlFor={codeId} className="text-sm leading-snug font-normal text-muted-foreground">
            Don&apos;t ask again on this device for 30 days
          </Label>
        </div>
        <Button type="submit" className="w-full" disabled={submitting}>
          {submitting && (
            <Loader2
              className="size-4 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          )}
          {submitting ? "Verifying…" : "Verify"}
        </Button>
      </form>
    </Form>
  );
}
