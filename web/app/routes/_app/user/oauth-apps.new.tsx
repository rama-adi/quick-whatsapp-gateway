// User: create a "Sign in with WhatsApp" app (oauth.md §6.2). The shared
// OAuthAppForm with its live consent preview; on create, a confidential app's
// client secret is revealed once, then the user lands on the new app's
// Integration tab (client ID + ready-to-run snippet).

import { useRef, useState } from "react";
import {
  Link,
  createFileRoute,
  useBlocker,
  useNavigate,
} from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { isApiError } from "~/lib/api/envelope";
import { useCreateOAuthApp } from "~/lib/api/hooks/oauth";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import {
  OAuthAppForm,
  describeErrors,
  emptyFormState,
  focusFirstError,
  formFingerprint,
  toRequestBody,
  validateForm,
} from "./-oauth/OAuthAppForm";
import { SecretDialog } from "./-oauth/ui";

export const Route = createFileRoute("/_app/user/oauth-apps/new")({
  head: () => titleHead("New sign-in app", "Sign in with WhatsApp"),
  component: NewOAuthApp,
});

const PRISTINE = formFingerprint(emptyFormState());
const ID_PREFIX = "create";

function NewOAuthApp() {
  const navigate = useNavigate();
  const confirm = useConfirm();
  const [state, setState] = useState(emptyFormState);
  const [submitted, setSubmitted] = useState(false);
  const [created, setCreated] = useState<{ id: string; clientId: string; secret: string } | null>(null);
  const create = useCreateOAuthApp();
  // Set once the app exists so leaving the page isn't treated as losing work.
  const done = useRef(false);

  const dirty = formFingerprint(state) !== PRISTINE;
  useBlocker({
    shouldBlockFn: async () => {
      if (done.current || !dirty) return false;
      const leave = await confirm({
        title: "Discard this new app?",
        description: "You haven't created the app yet. Your changes will be lost.",
        confirmLabel: "Discard",
        cancelLabel: "Keep editing",
        destructive: true,
      });
      return !leave;
    },
    enableBeforeUnload: () => dirty && !done.current,
  });

  const errors = validateForm(state);
  const missing = submitted ? describeErrors(errors) : "";

  const goToIntegration = (appId: string) =>
    void navigate({
      to: "/user/oauth-apps/$appId",
      params: { appId },
      search: { tab: "integration" },
    });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(errors).length > 0) {
      focusFirstError(ID_PREFIX, errors);
      return;
    }
    create.mutate(toRequestBody(state), {
      onError: (error) =>
        toast.error(isApiError(error) ? error.message : "Couldn't create the app"),
      onSuccess: (app) => {
        done.current = true;
        toast.success(`Created “${app.name}”`);
        if (app.clientSecret) {
          setCreated({ id: app.id, clientId: app.clientId, secret: app.clientSecret });
        } else {
          goToIntegration(app.id);
        }
      },
    });
  };

  return (
    <Page className="max-w-6xl">
      <PageTitle
        eyebrow={
          <Link
            to="/user/oauth-apps"
            className="inline-flex items-center gap-1 transition-colors duration-150 ease-soft hover:text-foreground"
          >
            <ArrowLeftIcon className="size-3.5" aria-hidden />
            Sign in with WhatsApp
          </Link>
        }
        title="New sign-in app"
        description="Set up how people log in to your site with WhatsApp. The preview shows what they'll see as you go."
        docsHref={DOCS.signInWithWhatsApp}
      />

      <form onSubmit={submit} noValidate className="grid gap-6">
        <OAuthAppForm
          state={state}
          onChange={setState}
          idPrefix={ID_PREFIX}
          showErrors={submitted}
          autoFocus
        />

        <div className="flex flex-col-reverse gap-3 border-t pt-6 sm:flex-row sm:items-center sm:justify-between">
          <Button type="button" variant="outline" asChild>
            <Link to="/user/oauth-apps">Cancel</Link>
          </Button>
          <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center">
            {missing ? (
              <p className="text-sm text-destructive sm:text-right" role="status">
                Still needed: {missing}
              </p>
            ) : null}
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create app"}
            </Button>
          </div>
        </div>
      </form>

      <SecretDialog
        secret={created?.secret ?? null}
        clientId={created?.clientId}
        onClose={() => {
          const id = created?.id;
          setCreated(null);
          if (id) goToIntegration(id);
        }}
      />
    </Page>
  );
}
