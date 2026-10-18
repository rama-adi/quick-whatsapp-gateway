// Account settings for the signed-in person. Surface: every signed-in user
// (members and platform admins). URL: /account. Reached from the user menu.
//
// Everything here talks to better-auth directly (same origin, cookie
// session), not to the gateway API:
//   - Profile: authClient.updateUser({ name })
//   - Password: authClient.changePassword({ currentPassword, newPassword,
//     revokeOtherSessions })
//   - Two-factor (TOTP, the twoFactor() plugin in lib/auth/server.ts):
//       enable({ password }) -> { totpURI, backupCodes } (not active yet)
//       verifyTotp({ code })   -> confirms the app works and turns 2FA on
//       generateBackupCodes({ password }), disable({ password })
//     The 2FA state comes from the reactive better-auth session
//     (user.twoFactorEnabled); the plugin's client re-fetches it after every
//     /two-factor/* call.
//
// Turning 2FA on and changing the password with "sign out other devices"
// rotate the session token, so we drop the cached gateway JWT and invalidate
// the router afterwards. While an admin is impersonating someone, the forms
// are disabled: these settings belong to the impersonated person.

import { useState, type ReactNode } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { createFileRoute, useRouter } from "@tanstack/react-router";
import {
  DownloadIcon,
  Loader2,
  ShieldCheckIcon,
  ShieldIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Page, PageTitle } from "~/components/shell/page-title";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "~/components/ui/form";
import { PasswordInput } from "~/components/password-input";
import { SecretRevealDialog } from "~/components/secret-reveal";
import { CopyButton } from "~/components/copyable";
import { QrCode } from "~/routes/-oauth/QrCode";
import { authClient } from "~/lib/auth/client";
import { useAppSession } from "~/lib/auth/context";
import { clearGatewayToken } from "~/lib/api/token-provider";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";

export const Route = createFileRoute("/_app/account")({
  head: () => titleHead("Account"),
  component: Account,
});

type AuthError = { status?: number; code?: string; message?: string } | null;

/** Turn a better-auth error into one plain sentence. */
function authErrorMessage(error: AuthError, fallback: string): string {
  if (!error) return fallback;
  switch (error.code) {
    case "INVALID_PASSWORD":
      return "That password is incorrect.";
    case "INVALID_CODE":
      return "That code is incorrect or expired. Wait for the next code and try again.";
    case "SESSION_NOT_FRESH":
      return "For your security, sign out and sign back in, then try again.";
    case "PASSWORD_TOO_SHORT":
      return "The new password is too short. Use at least 8 characters.";
    case "PASSWORD_TOO_LONG":
      return "The new password is too long. Use at most 128 characters.";
    case "CREDENTIAL_ACCOUNT_NOT_FOUND":
      return "This account doesn't sign in with a password.";
  }
  if (error.status === 429) return "Too many attempts. Wait a minute and try again.";
  return error.message || fallback;
}

function Account() {
  const session = useAppSession();
  const readOnly = session.impersonating;

  return (
    <Page width="3xl">
      <PageTitle
        title="Account"
        description="Your name, password and sign-in security. These settings are personal and apply in every organization you belong to."
      />

      {readOnly ? (
        <p className="flex items-start gap-2 rounded-xl bg-amber-500/10 px-4 py-3 text-sm text-amber-800 dark:text-amber-300">
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          You&apos;re impersonating {session.user.email}. Account settings are read-only
          until you stop impersonating.
        </p>
      ) : null}

      <ProfileCard readOnly={readOnly} />
      <PasswordCard readOnly={readOnly} />
      <TwoFactorCard readOnly={readOnly} />
    </Page>
  );
}

/* ----------------------------------------------------------------------------
 * Profile
 * ------------------------------------------------------------------------- */

const profileSchema = z.object({
  name: z.string().trim().min(1, "Enter your name").max(120, "Keep it under 120 characters"),
});
type ProfileValues = z.infer<typeof profileSchema>;

function ProfileCard({ readOnly }: { readOnly: boolean }) {
  const session = useAppSession();
  const router = useRouter();
  const form = useForm<ProfileValues>({
    resolver: zodResolver(profileSchema),
    defaultValues: { name: session.user.name },
  });

  const onSubmit = async (values: ProfileValues): Promise<void> => {
    const { error } = await authClient.updateUser({ name: values.name });
    if (error) {
      toast.error(authErrorMessage(error, "Could not save your profile"));
      return;
    }
    form.reset({ name: values.name });
    toast.success("Profile saved");
    // The sidebar account chip reads the name from the route context.
    await router.invalidate();
  };

  const submitting = form.formState.isSubmitting;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Profile</CardTitle>
        <CardDescription>How you appear to teammates in your organizations.</CardDescription>
      </CardHeader>
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="contents">
          <CardContent className="grid items-start gap-5 sm:grid-cols-2">
            <FormField
              control={form.control}
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Name</FormLabel>
                  <FormControl>
                    <Input autoComplete="name" disabled={readOnly} {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <div className="grid gap-2">
              <Label htmlFor="account-email">Email</Label>
              <Input id="account-email" value={session.user.email} readOnly disabled />
              <p className="text-xs text-muted-foreground">
                You sign in with this email. Ask your gateway operator to change it.
              </p>
            </div>
          </CardContent>
          <CardFooter className="justify-end">
            <Button
              type="submit"
              disabled={readOnly || submitting || !form.formState.isDirty}
            >
              {submitting && <Spinner />}
              Save
            </Button>
          </CardFooter>
        </form>
      </Form>
    </Card>
  );
}

/* ----------------------------------------------------------------------------
 * Password
 * ------------------------------------------------------------------------- */

const passwordSchema = z
  .object({
    currentPassword: z.string().min(1, "Enter your current password"),
    newPassword: z
      .string()
      .min(8, "Use at least 8 characters")
      .max(128, "Use at most 128 characters"),
    confirm: z.string().min(1, "Type the new password again"),
    revokeOtherSessions: z.boolean(),
  })
  .refine((v) => v.newPassword === v.confirm, {
    path: ["confirm"],
    message: "Passwords do not match",
  })
  .refine((v) => v.newPassword !== v.currentPassword, {
    path: ["newPassword"],
    message: "Choose a password different from the current one",
  });
type PasswordValues = z.infer<typeof passwordSchema>;

function PasswordCard({ readOnly }: { readOnly: boolean }) {
  const session = useAppSession();
  const router = useRouter();
  const form = useForm<PasswordValues>({
    resolver: zodResolver(passwordSchema),
    defaultValues: {
      currentPassword: "",
      newPassword: "",
      confirm: "",
      revokeOtherSessions: true,
    },
  });

  const onSubmit = async (values: PasswordValues): Promise<void> => {
    const { error } = await authClient.changePassword({
      currentPassword: values.currentPassword,
      newPassword: values.newPassword,
      revokeOtherSessions: values.revokeOtherSessions,
    });
    if (error) {
      if (error.code === "INVALID_PASSWORD") {
        form.setError("currentPassword", { message: "That password is incorrect." });
        form.setFocus("currentPassword");
        return;
      }
      toast.error(authErrorMessage(error, "Could not change your password"));
      return;
    }
    form.reset();
    toast.success(
      values.revokeOtherSessions
        ? "Password changed. Other devices were signed out."
        : "Password changed",
    );
    if (values.revokeOtherSessions) {
      // A fresh session token was issued for this browser.
      clearGatewayToken();
      await router.invalidate();
    }
  };

  const submitting = form.formState.isSubmitting;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Password</CardTitle>
        <CardDescription>Change the password you use to sign in.</CardDescription>
      </CardHeader>
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="contents">
          <CardContent className="grid gap-5">
            {/* Lets password managers attach the new password to this account. */}
            <input
              type="text"
              name="username"
              autoComplete="username"
              value={session.user.email}
              readOnly
              hidden
            />
            <FormField
              control={form.control}
              name="currentPassword"
              render={({ field }) => (
                <FormItem className="sm:max-w-[calc(50%-0.625rem)]">
                  <FormLabel>Current password</FormLabel>
                  <FormControl>
                    <PasswordInput
                      autoComplete="current-password"
                      disabled={readOnly}
                      {...field}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <div className="grid items-start gap-5 sm:grid-cols-2">
              <FormField
                control={form.control}
                name="newPassword"
                render={({ field, fieldState }) => (
                  <FormItem>
                    <FormLabel>New password</FormLabel>
                    <FormControl>
                      <PasswordInput
                        autoComplete="new-password"
                        disabled={readOnly}
                        {...field}
                      />
                    </FormControl>
                    {fieldState.error ? (
                      <FormMessage />
                    ) : (
                      <FormDescription className="text-xs">At least 8 characters.</FormDescription>
                    )}
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="confirm"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Confirm new password</FormLabel>
                    <FormControl>
                      <PasswordInput
                        autoComplete="new-password"
                        disabled={readOnly}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
            <FormField
              control={form.control}
              name="revokeOtherSessions"
              render={({ field }) => (
                <FormItem className="flex items-start gap-2 space-y-0">
                  <FormControl>
                    <input
                      type="checkbox"
                      checked={field.value}
                      onChange={(e) => field.onChange(e.target.checked)}
                      onBlur={field.onBlur}
                      ref={field.ref}
                      name={field.name}
                      disabled={readOnly}
                      className="mt-0.5 size-4 shrink-0 rounded border-input accent-primary focus-visible:ring-2 focus-visible:ring-ring"
                    />
                  </FormControl>
                  <div className="grid gap-0.5">
                    <FormLabel className="font-normal">Sign out of other devices</FormLabel>
                    <FormDescription className="text-xs">
                      Recommended if you think someone else knows your password. This
                      browser stays signed in.
                    </FormDescription>
                  </div>
                </FormItem>
              )}
            />
          </CardContent>
          <CardFooter className="justify-end">
            <Button type="submit" disabled={readOnly || submitting}>
              {submitting && <Spinner />}
              Change password
            </Button>
          </CardFooter>
        </form>
      </Form>
    </Card>
  );
}

/* ----------------------------------------------------------------------------
 * Two-factor authentication
 * ------------------------------------------------------------------------- */

type PasswordPrompt = {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  destructive?: boolean;
  /** Run the action; return an error message to keep the dialog open. */
  run: (password: string) => Promise<string | null>;
};

function TwoFactorCard({ readOnly }: { readOnly: boolean }) {
  const router = useRouter();
  const authSession = authClient.useSession();
  const enabled = Boolean(
    (authSession.data?.user as { twoFactorEnabled?: boolean | null } | undefined)
      ?.twoFactorEnabled,
  );

  const [setupOpen, setSetupOpen] = useState(false);
  const [prompt, setPrompt] = useState<PasswordPrompt | null>(null);
  const [backupCodes, setBackupCodes] = useState<string[] | null>(null);

  const regenerate = (): void =>
    setPrompt({
      title: "Create new backup codes",
      description:
        "Your old backup codes stop working as soon as the new ones are created. Enter your password to continue.",
      confirmLabel: "Create new codes",
      run: async (password) => {
        const { data, error } = await authClient.twoFactor.generateBackupCodes({ password });
        if (error) return authErrorMessage(error, "Could not create backup codes");
        setBackupCodes(data?.backupCodes ?? []);
        return null;
      },
    });

  const disable = (): void =>
    setPrompt({
      title: "Turn off two-factor authentication?",
      description:
        "You'll sign in with only your password, and your backup codes stop working. Enter your password to confirm.",
      confirmLabel: "Turn off",
      destructive: true,
      run: async (password) => {
        const { error } = await authClient.twoFactor.disable({ password });
        if (error) return authErrorMessage(error, "Could not turn off two-factor authentication");
        toast.success("Two-factor authentication is off");
        await router.invalidate();
        return null;
      },
    });

  return (
    <Card>
      <CardHeader>
        <CardTitle>Two-factor authentication</CardTitle>
        <CardDescription>
          Require a code from an authenticator app after your password, so a leaked
          password alone can&apos;t get into your account.
        </CardDescription>
        <CardAction>
          {authSession.isPending ? (
            <Skeleton className="h-5 w-12 rounded-full" />
          ) : (
            <TwoFactorBadge enabled={enabled} />
          )}
        </CardAction>
      </CardHeader>
      <CardContent>
        {authSession.isPending ? (
          <Skeleton className="h-14 w-full rounded-lg" />
        ) : (
          <div className="flex items-start gap-3 rounded-lg bg-muted/50 px-4 py-3">
            {enabled ? (
              <ShieldCheckIcon className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" aria-hidden />
            ) : (
              <ShieldIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
            )}
            <p className="text-sm text-pretty text-muted-foreground">
              {enabled
                ? "When you sign in on a new device you'll be asked for a 6-digit code from your authenticator app. If you lose the app, sign in with one of your backup codes."
                : "You'll need an authenticator app such as Google Authenticator, 1Password, Authy or Microsoft Authenticator. Setup takes about a minute."}
            </p>
          </div>
        )}
      </CardContent>
      <CardFooter className="flex-wrap justify-end gap-2">
        {authSession.isPending ? null : enabled ? (
          <>
            <Button variant="destructive" disabled={readOnly} onClick={disable}>
              Turn off
            </Button>
            <Button variant="outline" disabled={readOnly} onClick={regenerate}>
              New backup codes
            </Button>
          </>
        ) : (
          <Button disabled={readOnly} onClick={() => setSetupOpen(true)}>
            Set up two-factor
          </Button>
        )}
      </CardFooter>

      <TwoFactorSetupDialog
        open={setupOpen}
        onOpenChange={setSetupOpen}
        onEnabled={async (codes) => {
          setSetupOpen(false);
          setBackupCodes(codes);
          toast.success("Two-factor authentication is on");
          // Turning 2FA on rotates the session token.
          clearGatewayToken();
          await router.invalidate();
        }}
      />

      <PasswordPromptDialog prompt={prompt} onClose={() => setPrompt(null)} />

      <BackupCodesDialog codes={backupCodes} onClose={() => setBackupCodes(null)} />
    </Card>
  );
}

function TwoFactorBadge({ enabled }: { enabled: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium",
        enabled
          ? "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400"
          : "bg-muted text-muted-foreground",
      )}
    >
      <span
        aria-hidden
        className={cn("size-1.5 rounded-full", enabled ? "bg-emerald-500" : "bg-muted-foreground/50")}
      />
      {enabled ? "On" : "Off"}
    </span>
  );
}

/** Asks for the account password, then runs `prompt.run`. */
function PasswordPromptDialog({
  prompt,
  onClose,
}: {
  prompt: PasswordPrompt | null;
  onClose: () => void;
}) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const close = (): void => {
    if (busy) return;
    setPassword("");
    setError(null);
    onClose();
  };

  const submit = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    if (!prompt || busy) return;
    if (!password) {
      setError("Enter your password.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const failure = await prompt.run(password);
      if (failure) {
        setError(failure);
        return;
      }
      setPassword("");
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={prompt !== null} onOpenChange={(open) => !open && close()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={(e) => void submit(e)} className="grid gap-5" noValidate>
          <DialogHeader>
            <DialogTitle>{prompt?.title}</DialogTitle>
            <DialogDescription className="text-pretty">{prompt?.description}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="prompt-password">Password</Label>
            <PasswordInput
              id="prompt-password"
              autoComplete="current-password"
              autoFocus
              value={password}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? "prompt-password-error" : undefined}
              onChange={(e) => setPassword(e.target.value)}
            />
            {error ? (
              <p id="prompt-password-error" className="text-sm text-destructive">
                {error}
              </p>
            ) : null}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close} disabled={busy}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant={prompt?.destructive ? "destructive" : "default"}
              disabled={busy}
            >
              {busy && <Spinner />}
              {prompt?.confirmLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

type SetupState =
  | { step: "password" }
  | { step: "scan"; totpURI: string; backupCodes: string[] };

/**
 * Three steps in one dialog: confirm password -> scan the QR (or type the
 * key) -> enter a code to prove the app works. Only the last step turns 2FA
 * on, so closing midway leaves the account as it was.
 */
function TwoFactorSetupDialog({
  open,
  onOpenChange,
  onEnabled,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onEnabled: (backupCodes: string[]) => void | Promise<void>;
}) {
  const [state, setState] = useState<SetupState>({ step: "password" });
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reset = (): void => {
    setState({ step: "password" });
    setPassword("");
    setCode("");
    setError(null);
  };

  const changeOpen = (next: boolean): void => {
    if (busy) return;
    if (!next) reset();
    onOpenChange(next);
  };

  const startSetup = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    if (busy) return;
    if (!password) {
      setError("Enter your password.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { data, error: err } = await authClient.twoFactor.enable({ password });
      if (err || !data) {
        setError(authErrorMessage(err, "Could not start two-factor setup"));
        return;
      }
      setPassword("");
      setState({ step: "scan", totpURI: data.totpURI, backupCodes: data.backupCodes });
    } finally {
      setBusy(false);
    }
  };

  const verify = async (e?: React.FormEvent): Promise<void> => {
    e?.preventDefault();
    if (busy || state.step !== "scan") return;
    if (!/^\d{6}$/.test(code)) {
      setError("Enter the 6-digit code shown in your authenticator app.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { error: err } = await authClient.twoFactor.verifyTotp({ code });
      if (err) {
        setError(authErrorMessage(err, "Could not verify the code"));
        setCode("");
        return;
      }
      const codes = state.backupCodes;
      reset();
      await onEnabled(codes);
    } finally {
      setBusy(false);
    }
  };

  const secret = state.step === "scan" ? totpSecret(state.totpURI) : null;

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        className="sm:max-w-md"
        // Don't lose a half-finished setup to a stray click.
        onInteractOutside={(e) => e.preventDefault()}
      >
        {state.step === "password" ? (
          <form onSubmit={(e) => void startSetup(e)} className="grid gap-5" noValidate>
            <DialogHeader>
              <DialogTitle>Set up two-factor authentication</DialogTitle>
              <DialogDescription className="text-pretty">
                Step 1 of 2. Confirm your password to continue.
              </DialogDescription>
            </DialogHeader>
            <div className="grid gap-2">
              <Label htmlFor="setup-password">Password</Label>
              <PasswordInput
                id="setup-password"
                autoComplete="current-password"
                autoFocus
                value={password}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? "setup-error" : undefined}
                onChange={(e) => setPassword(e.target.value)}
              />
              {error ? (
                <p id="setup-error" className="text-sm text-destructive">
                  {error}
                </p>
              ) : null}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => changeOpen(false)} disabled={busy}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy}>
                {busy && <Spinner />}
                Continue
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <form onSubmit={(e) => void verify(e)} className="grid gap-5" noValidate>
            <DialogHeader>
              <DialogTitle>Scan with your authenticator app</DialogTitle>
              <DialogDescription className="text-pretty">
                Step 2 of 2. In your authenticator app, add an account and scan this
                code. Then enter the 6-digit code it shows.
              </DialogDescription>
            </DialogHeader>

            <div className="flex flex-col items-center gap-3">
              <QrCode value={state.totpURI} size={188} />
              {secret ? (
                <details className="group w-full rounded-lg bg-muted/50 px-3 py-2 text-sm">
                  <summary className="cursor-pointer text-center text-xs font-medium text-muted-foreground select-none hover:text-foreground">
                    Can&apos;t scan it? Enter a setup key instead
                  </summary>
                  <div className="mt-2 flex items-center gap-2">
                    <code className="min-w-0 flex-1 font-mono text-sm break-all select-all">
                      {secret.match(/.{1,4}/g)?.join(" ") ?? secret}
                    </code>
                    <CopyButton value={secret} iconOnly label="Copy setup key" />
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Choose &ldquo;time-based&rdquo; if your app asks.
                  </p>
                </details>
              ) : null}
            </div>

            <div className="grid gap-2">
              <Label htmlFor="setup-code">6-digit code</Label>
              <Input
                id="setup-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                placeholder="000000"
                value={code}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? "setup-code-error" : undefined}
                className="h-11 text-center font-mono text-xl tracking-[0.5em] placeholder:text-muted-foreground/50"
                onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
              />
              {error ? (
                <p id="setup-code-error" className="text-sm text-destructive">
                  {error}
                </p>
              ) : null}
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => changeOpen(false)} disabled={busy}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy || code.length !== 6}>
                {busy && <Spinner />}
                Turn on
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** The base32 secret inside an otpauth:// URI, for manual entry. */
function totpSecret(uri: string): string | null {
  try {
    return new URL(uri).searchParams.get("secret");
  } catch {
    return null;
  }
}

function BackupCodesDialog({
  codes,
  onClose,
}: {
  codes: string[] | null;
  onClose: () => void;
}) {
  const text = (codes ?? []).join("\n");

  const download = (): void => {
    const blob = new Blob(
      [`WA Gateway backup codes\nEach code works once.\n\n${text}\n`],
      { type: "text/plain" },
    );
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "wa-gateway-backup-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <SecretRevealDialog
      open={codes !== null}
      secret={text}
      title="Save your backup codes"
      description="If you lose your authenticator app, sign in with one of these codes instead. Each code works once. Keep them in a password manager or print them."
      secretLabel={`${codes?.length ?? 0} backup codes`}
      copyLabel="Copy codes"
      icon={<ShieldCheckIcon className="size-5 text-muted-foreground" aria-hidden />}
      className="sm:max-w-md"
      onClose={onClose}
    >
      <Button variant="outline" onClick={download}>
        <DownloadIcon aria-hidden />
        Download as .txt
      </Button>
    </SecretRevealDialog>
  );
}

function Spinner() {
  return <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />;
}
