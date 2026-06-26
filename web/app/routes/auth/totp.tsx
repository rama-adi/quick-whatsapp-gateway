// Two-factor (TOTP) challenge. Surface agent: auth.
// Reached after sign-in returns {totp_redirect:true}; Authula holds a short-lived
// pending cookie. We post the 6-digit authenticator code (or a backup code) to
// complete auth, which then mints the real session cookie.

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { redirect, useNavigate, useSearchParams } from "react-router";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { Label } from "~/components/ui/label";
import { isApiError } from "~/lib/api/envelope";
import { loadSession } from "~/lib/auth/session";
import {
  resolvePostAuthRedirect,
  safeNext,
  verifyBackupCodeRequest,
  verifyTotpRequest,
} from "./_shared";

export async function clientLoader() {
  // Fully authenticated already? Don't sit on the challenge screen.
  const session = await loadSession();
  if (session) {
    throw redirect(await resolvePostAuthRedirect());
  }
  return null;
}

const authenticatorSchema = z.object({
  code: z
    .string()
    .trim()
    .regex(/^\d{6}$/, "Enter the 6-digit code from your authenticator app"),
});

const backupSchema = z.object({
  code: z.string().trim().min(1, "Enter a backup code"),
});

type CodeValues = { code: string };

type Mode = "authenticator" | "backup";

export default function Totp() {
  const [mode, setMode] = useState<Mode>("authenticator");

  return (
    <Card>
      <CardHeader>
        <CardTitle>Two-factor authentication</CardTitle>
        <CardDescription>
          Confirm it&apos;s you to finish signing in.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="authenticator">Authenticator</TabsTrigger>
            <TabsTrigger value="backup">Backup code</TabsTrigger>
          </TabsList>
          <TabsContent value="authenticator" className="pt-4">
            <ChallengeForm
              key="authenticator"
              mode="authenticator"
              schema={authenticatorSchema}
            />
          </TabsContent>
          <TabsContent value="backup" className="pt-4">
            <ChallengeForm key="backup" mode="backup" schema={backupSchema} />
          </TabsContent>
        </Tabs>
      </CardContent>
      <CardFooter>
        <p className="text-center text-sm text-muted-foreground w-full">
          Lost access?{" "}
          <a
            href="/login"
            className="font-medium text-foreground underline-offset-4 hover:underline"
          >
            Start over
          </a>
        </p>
      </CardFooter>
    </Card>
  );
}

function ChallengeForm({
  mode,
  schema,
}: {
  mode: Mode;
  schema: z.ZodType<CodeValues, CodeValues>;
}) {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [formError, setFormError] = useState<string | null>(null);
  const [trustDevice, setTrustDevice] = useState(false);

  const form = useForm<CodeValues>({
    resolver: zodResolver(schema),
    defaultValues: { code: "" },
  });

  const onSubmit = async (values: CodeValues): Promise<void> => {
    setFormError(null);
    try {
      const req = mode === "authenticator" ? verifyTotpRequest : verifyBackupCodeRequest;
      await req({ code: values.code, trustDevice });
      const dest = safeNext(searchParams.get("next")) ?? (await resolvePostAuthRedirect());
      navigate(dest, { replace: true });
    } catch (err) {
      setFormError(toMessage(err, mode));
    }
  };

  const submitting = form.formState.isSubmitting;
  const codeId = `${mode}-trust`;

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="space-y-4">
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
          name="code"
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {mode === "authenticator" ? "Authenticator code" : "Backup code"}
              </FormLabel>
              <FormControl>
                {mode === "authenticator" ? (
                  <Input
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    autoFocus
                    maxLength={6}
                    placeholder="123456"
                    className="text-center text-lg tracking-[0.4em]"
                    {...field}
                  />
                ) : (
                  <Input
                    autoComplete="one-time-code"
                    autoFocus
                    placeholder="xxxx-xxxx"
                    {...field}
                  />
                )}
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <div className="flex items-center gap-2">
          <input
            id={codeId}
            type="checkbox"
            checked={trustDevice}
            onChange={(e) => setTrustDevice(e.target.checked)}
            className="size-4 rounded border-input text-primary accent-primary focus-visible:ring-2 focus-visible:ring-ring"
          />
          <Label htmlFor={codeId} className="text-sm font-normal text-muted-foreground">
            Trust this device for 30 days
          </Label>
        </div>
        <Button type="submit" className="w-full" disabled={submitting}>
          {submitting && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
          Verify
        </Button>
      </form>
    </Form>
  );
}

function toMessage(err: unknown, mode: Mode): string {
  if (isApiError(err)) {
    if (err.isUnauthorized) {
      return mode === "authenticator"
        ? "That code is incorrect or expired. Try again."
        : "That backup code is invalid or already used.";
    }
    if (err.code === "rate_limited") return "Too many attempts. Please wait and try again.";
    return err.message || "Verification failed.";
  }
  return "Unable to reach the server. Check your connection and try again.";
}
