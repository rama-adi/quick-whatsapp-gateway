import { useState } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Card, CardContent } from "~/components/ui/card";
import { isApiError } from "~/lib/api/envelope";
import { useCreateOAuthApp } from "~/lib/api/hooks/oauth";
import {
  OAuthAppForm,
  emptyFormState,
  isFormValid,
  toRequestBody,
} from "./-oauth/OAuthAppForm";
import { SecretDialog } from "./-oauth/ui";

export const Route = createFileRoute("/_app/user/oauth-apps/new")({ component: NewOAuthApp });

function NewOAuthApp() {
  const navigate = useNavigate();
  const [state, setState] = useState(emptyFormState);
  const [secret, setSecret] = useState<string | null>(null);
  const create = useCreateOAuthApp();

  const submit = () => {
    if (!isFormValid(state)) {
      toast.error("Complete the required fields first.");
      return;
    }
    create.mutate(toRequestBody(state), {
      onError: (error) =>
        toast.error(
          isApiError(error) ? error.message : "Failed to create app",
        ),
      onSuccess: (app) => {
        toast.success("App created");
        if (app.clientSecret) setSecret(app.clientSecret);
        else
          void navigate({
            to: "/user/oauth-apps/$appId",
            params: { appId: app.id },
          });
      },
    });
  };

  return (
    <div className="mx-auto w-full max-w-6xl pb-12">
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <Button asChild variant="ghost" size="sm" className="-ml-3 mb-3 text-muted-foreground">
            <Link to="/user/oauth-apps"><ArrowLeftIcon /> Back to apps</Link>
          </Button>
          <p className="mb-2 text-xs font-semibold uppercase tracking-[0.18em] text-emerald-600">Sign in with WhatsApp</p>
          <h1 className="text-3xl font-semibold tracking-tight">Create a sign-in app</h1>
          <p className="mt-2 max-w-2xl text-muted-foreground">
            Configure your app while the customer-facing sign-in experience
            updates beside it.
          </p>
        </div>
        <ShieldCheckIcon className="mt-10 hidden size-10 text-emerald-600/70 sm:block" aria-hidden />
      </div>

      <Card>
        <CardContent className="p-6 sm:p-8">
          <OAuthAppForm
            state={state}
            onChange={setState}
            idPrefix="create"
          />
        </CardContent>
      </Card>

      <div className="mt-6 flex items-center justify-between border-t pt-6">
        <Button
          variant="outline"
          onClick={() => void navigate({ to: "/user/oauth-apps" })}
        >
          Cancel
        </Button>
        <Button
          onClick={submit}
          disabled={create.isPending || !isFormValid(state)}
        >
          {create.isPending ? "Creating…" : "Create app"}
        </Button>
      </div>
      <SecretDialog secret={secret} onClose={() => { setSecret(null); void navigate({ to: "/user/oauth-apps" }); }} />
    </div>
  );
}
