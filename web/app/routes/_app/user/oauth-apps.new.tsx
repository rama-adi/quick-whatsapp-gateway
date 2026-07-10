import { useState } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon, ArrowRightIcon, CheckIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Card, CardContent } from "~/components/ui/card";
import { isApiError } from "~/lib/api/envelope";
import { useCreateOAuthApp } from "~/lib/api/hooks/oauth";
import { cn } from "~/lib/utils";
import { ConsentCard } from "~/routes/-oauth/ConsentCard";
import { OAuthAppForm, emptyFormState, isFormValid, toRequestBody } from "./-oauth/OAuthAppForm";
import { buildPreviewSnapshot } from "./-oauth/preview";
import { SecretDialog } from "./-oauth/ui";

export const Route = createFileRoute("/_app/user/oauth-apps/new")({ component: NewOAuthApp });

function NewOAuthApp() {
  const navigate = useNavigate();
  const [step, setStep] = useState<1 | 2>(1);
  const [state, setState] = useState(emptyFormState);
  const [secret, setSecret] = useState<string | null>(null);
  const create = useCreateOAuthApp();

  const submit = () => create.mutate(toRequestBody(state), {
    onError: (error) => toast.error(isApiError(error) ? error.message : "Failed to create app"),
    onSuccess: (app) => {
      toast.success("App created");
      if (app.clientSecret) setSecret(app.clientSecret);
      else void navigate({ to: "/user/oauth-apps/$appId", params: { appId: app.id } });
    },
  });

  return (
    <div className="mx-auto w-full max-w-6xl pb-12">
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <Button asChild variant="ghost" size="sm" className="-ml-3 mb-3 text-muted-foreground">
            <Link to="/user/oauth-apps"><ArrowLeftIcon /> Back to apps</Link>
          </Button>
          <p className="mb-2 text-xs font-semibold uppercase tracking-[0.18em] text-emerald-600">Sign in with WhatsApp</p>
          <h1 className="text-3xl font-semibold tracking-tight">Create a sign-in app</h1>
          <p className="mt-2 max-w-2xl text-muted-foreground">Connect your product to a WhatsApp number, then review the exact sign-in experience before publishing.</p>
        </div>
        <ShieldCheckIcon className="mt-10 hidden size-10 text-emerald-600/70 sm:block" aria-hidden />
      </div>

      <ol className="mb-8 grid grid-cols-2 gap-2" aria-label="Setup progress">
        <Step active={step === 1} done={step > 1} number={1} label="Configure" />
        <Step active={step === 2} done={false} number={2} label="Review & create" />
      </ol>

      {step === 1 ? (
        <Card><CardContent className="p-6 sm:p-8"><OAuthAppForm state={state} onChange={setState} idPrefix="create" showPreview={false} /></CardContent></Card>
      ) : (
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_26rem]">
          <Card><CardContent className="space-y-6 p-6 sm:p-8">
            <div><p className="text-sm font-medium">Ready to create</p><h2 className="mt-1 text-2xl font-semibold">{state.name}</h2><p className="mt-2 text-sm text-muted-foreground">Review the customer-facing consent screen. You can go back without losing your setup.</p></div>
            <dl className="grid gap-4 rounded-xl bg-muted/40 p-5 sm:grid-cols-2">
              <Summary label="Verification" value={state.modes.join(" + ")} />
              <Summary label="Scopes" value={`${state.scopes.length} permissions`} />
              <Summary label="Redirects" value={`${state.redirectUris.filter(Boolean).length} URI${state.redirectUris.filter(Boolean).length === 1 ? "" : "s"}`} />
              <Summary label="Client" value={state.clientType} />
            </dl>
          </CardContent></Card>
          <div><p className="mb-3 text-xs font-semibold uppercase tracking-widest text-muted-foreground">Consent preview</p><div className="rounded-2xl border bg-background p-5 shadow-xl shadow-black/5"><ConsentCard snapshot={buildPreviewSnapshot({ name: state.name, botName: state.botName, logoUrl: state.logoUrl, loginCommand: state.loginCommand, scopes: state.scopes, modes: state.modes })} reconnecting={false} onCancel={() => {}} cancelling={false} /></div></div>
        </div>
      )}

      <div className="mt-6 flex items-center justify-between border-t pt-6">
        <Button variant="outline" onClick={() => step === 1 ? void navigate({ to: "/user/oauth-apps" }) : setStep(1)}>{step === 1 ? "Cancel" : "Back"}</Button>
        {step === 1 ? <Button onClick={() => isFormValid(state) ? setStep(2) : toast.error("Complete the required fields first.")} disabled={!isFormValid(state)}>Review <ArrowRightIcon /></Button> : <Button onClick={submit} disabled={create.isPending}>{create.isPending ? "Creating…" : "Create app"}</Button>}
      </div>
      <SecretDialog secret={secret} onClose={() => { setSecret(null); void navigate({ to: "/user/oauth-apps" }); }} />
    </div>
  );
}

function Step({ active, done, number, label }: { active: boolean; done: boolean; number: number; label: string }) {
  return <li className={cn("flex items-center gap-3 rounded-xl border px-4 py-3 text-sm", active ? "border-emerald-500/50 bg-emerald-500/5" : "text-muted-foreground")}><span className={cn("grid size-7 place-items-center rounded-full text-xs font-semibold", active || done ? "bg-emerald-600 text-white" : "bg-muted")}>{done ? <CheckIcon className="size-4" /> : number}</span><span className="font-medium">{label}</span></li>;
}

function Summary({ label, value }: { label: string; value: string }) { return <div><dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt><dd className="mt-1 capitalize font-medium">{value}</dd></div>; }
