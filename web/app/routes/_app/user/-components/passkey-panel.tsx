// Passkey verification during pairing.
//
// WhatsApp asks accounts that have a passkey to sign a WebAuthn challenge
// before it links a new device. Browsers only allow that signature on a page
// from WhatsApp's own domain (the relying party is whatsapp.com), so this
// dashboard cannot ask for the passkey itself. Instead it hands the user a
// short script to run on web.whatsapp.com. The script adds a button there;
// clicking it asks for the passkey and copies the signed result, which the
// user pastes back here. WhatsApp may then show a code on the phone to compare
// with the one here.

import { useEffect, useState } from "react";
import { ArrowUpRightIcon, KeyRoundIcon } from "lucide-react";
import { toast } from "sonner";
import {
  useConfirmPasskey,
  useSessionPasskey,
  useSubmitPasskey,
} from "~/lib/api/hooks/sessions";
import { isApiError } from "~/lib/api/envelope";
import type { PasskeyStep } from "~/lib/api/types";
import { Button } from "~/components/ui/button";
import { Textarea } from "~/components/ui/textarea";
import { CopyButton } from "~/components/copyable";

/**
 * The script the user runs in the console on web.whatsapp.com. It adds a
 * button because browsers only allow a passkey prompt from a user click on a
 * focused page; running navigator.credentials.get() straight from the console
 * fails with NotAllowedError while the console has focus.
 */
export function passkeyScript(publicKey: Record<string, unknown>): string {
  return `(() => {
  const o = ${JSON.stringify(publicKey)};
  const dec = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));
  const enc = (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\\+/g, "-").replace(/\\//g, "_").replace(/=+$/, "");
  const btn = document.createElement("button");
  btn.textContent = "Verify with passkey";
  btn.style.cssText = "position:fixed;top:16px;right:16px;z-index:2147483647;padding:12px 18px;font:600 15px system-ui,sans-serif;background:#25d366;color:#fff;border:0;border-radius:8px;cursor:pointer;box-shadow:0 2px 8px rgba(0,0,0,.25)";
  btn.onclick = async () => {
    try {
      const cred = await navigator.credentials.get({ publicKey: {
        challenge: dec(o.challenge),
        timeout: o.timeout || 120000,
        rpId: o.rpId,
        userVerification: o.userVerification || "preferred",
        allowCredentials: (o.allowCredentials || []).map((c) => ({ id: dec(c.id), type: c.type || "public-key", transports: c.transports || undefined })),
      } });
      const r = cred.response;
      const out = JSON.stringify({ id: cred.id, rawId: enc(cred.rawId), type: cred.type, response: {
        clientDataJSON: enc(r.clientDataJSON), authenticatorData: enc(r.authenticatorData),
        signature: enc(r.signature), userHandle: r.userHandle ? enc(r.userHandle) : null } });
      try {
        await navigator.clipboard.writeText(out);
        btn.textContent = "Copied. Paste it into the dashboard.";
      } catch {
        window.prompt("Copy this and paste it into the dashboard:", out);
      }
    } catch (e) {
      btn.textContent = "Failed: " + (e && e.message ? e.message : e) + " (click to retry)";
    }
  };
  document.body.appendChild(btn);
})();`;
}

/** Seconds left until expiresAt, ticking every second. */
function useSecondsLeft(expiresAt: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  return Math.max(0, Math.round((expiresAt - now) / 1000));
}

function formatLeft(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}

/** Renders the pending passkey step for a session, or nothing. */
export function PasskeyPanel({ sessionId }: { sessionId: string }) {
  const step = useSessionPasskey(sessionId);
  if (!step) return null;
  return step.step === "confirm" ? (
    <ConfirmStep sessionId={sessionId} step={step} />
  ) : (
    <VerifyStep sessionId={sessionId} step={step} />
  );
}

function VerifyStep({ sessionId, step }: { sessionId: string; step: PasskeyStep }) {
  const submit = useSubmitPasskey();
  const [pasted, setPasted] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const left = useSecondsLeft(step.expiresAt);
  const script = passkeyScript(step.publicKey ?? {});

  const send = (): void => {
    let response: unknown;
    try {
      response = JSON.parse(pasted.trim());
    } catch {
      toast.error("That isn't the result the script copied. Paste it again.");
      return;
    }
    submit.mutate(
      { sessionId, response },
      {
        onSuccess: () => setSubmitted(true),
        onError: (err) =>
          toast.error(
            isApiError(err)
              ? `WhatsApp didn't accept the passkey: ${err.message}`
              : "Couldn't send the passkey result",
          ),
      },
    );
  };

  return (
    <section className="grid gap-4 rounded-xl border bg-background p-5">
      <header className="flex items-start gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          <KeyRoundIcon className="size-4" aria-hidden />
        </span>
        <div className="grid gap-1">
          <h3 className="text-sm font-medium">Verify with your passkey</h3>
          <p className="text-sm text-pretty text-muted-foreground">
            WhatsApp asks for this account's passkey before it links a new device. Your browser
            only shows the passkey prompt on web.whatsapp.com, so you approve it there and paste
            the result back here. {left > 0 ? `Time left: ${formatLeft(left)}.` : "Time is up."}
          </p>
        </div>
      </header>

      {submitted ? (
        <p className="text-sm text-muted-foreground">
          Sent. Waiting for WhatsApp… This page updates by itself, and may ask you to compare a code
          with your phone.
        </p>
      ) : (
        <ol className="grid gap-3 text-sm">
          <li className="grid gap-2">
            <span>
              <b className="font-medium">1.</b> Copy this script.
            </span>
            <CopyButton value={script} label="Copy script" className="justify-self-start" />
          </li>
          <li>
            <b className="font-medium">2.</b> Open{" "}
            <a
              href="https://web.whatsapp.com"
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-0.5 font-medium underline underline-offset-4"
            >
              web.whatsapp.com
              <ArrowUpRightIcon className="size-3.5" aria-hidden />
            </a>{" "}
            in this browser. You don't need to log in there. Open the developer console (F12, or
            Cmd+Option+J on a Mac), paste the script and press Enter. Chrome may ask you to type{" "}
            <code className="font-mono">allow pasting</code> first.
          </li>
          <li>
            <b className="font-medium">3.</b> Click the green{" "}
            <b className="font-medium">Verify with passkey</b> button that appears at the top right
            and approve with your passkey. The result is copied for you.
          </li>
          <li className="grid gap-2">
            <span>
              <b className="font-medium">4.</b> Paste the result here.
            </span>
            <Textarea
              value={pasted}
              onChange={(e) => setPasted(e.target.value)}
              placeholder='{"id":"…","rawId":"…","type":"public-key","response":{…}}'
              className="min-h-20 font-mono text-xs"
              aria-label="Passkey result"
            />
            <Button
              onClick={send}
              disabled={!pasted.trim() || submit.isPending || left === 0}
              className="justify-self-start"
            >
              {submit.isPending ? "Sending…" : "Send to WhatsApp"}
            </Button>
          </li>
        </ol>
      )}
    </section>
  );
}

function ConfirmStep({ sessionId, step }: { sessionId: string; step: PasskeyStep }) {
  const confirm = useConfirmPasskey();
  const left = useSecondsLeft(step.expiresAt);
  return (
    <section className="grid gap-3 rounded-xl border bg-background p-5 text-center">
      <h3 className="text-sm font-medium">Check the code on your phone</h3>
      <p className="text-sm text-pretty text-muted-foreground">
        Your phone shows a code. Confirm only if it is the same as this one.
        {left > 0 ? ` Time left: ${formatLeft(left)}.` : " Time is up."}
      </p>
      <code className="font-mono text-2xl font-semibold tracking-[0.2em]">{step.code}</code>
      <Button
        className="justify-self-center"
        disabled={confirm.isPending || confirm.isSuccess || left === 0}
        onClick={() =>
          confirm.mutate(
            { sessionId },
            {
              onError: (err) =>
                toast.error(isApiError(err) ? err.message : "Couldn't confirm the code"),
            },
          )
        }
      >
        {confirm.isSuccess ? "Confirmed. Linking…" : confirm.isPending ? "Confirming…" : "The codes match"}
      </Button>
    </section>
  );
}

/** What a failed pairing tells the user, by failureReason. */
export function failureMessage(reason: string | undefined): string | undefined {
  switch (reason) {
    case "passkey_failed":
      return "The passkey check wasn't completed in time or WhatsApp rejected it. Start the session to try again.";
    case "qr_timeout":
      return "The QR code expired before it was scanned. Start the session to get a new one.";
    case "pairing_error":
      return "WhatsApp stopped the linking. Start the session to try again.";
    case "client_outdated":
      return "WhatsApp rejected this gateway's version. Ask your operator to update the gateway.";
    case "stream_replaced":
      return "Another device took over this connection.";
    case "temporary_ban":
      return "WhatsApp has temporarily banned this account.";
    case "connect_rejected":
      return "WhatsApp refused the connection.";
    case "connect_error":
      return "The gateway couldn't reach WhatsApp. Start the session to try again.";
    default:
      return undefined;
  }
}
