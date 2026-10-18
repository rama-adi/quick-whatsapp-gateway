// Header control shown while a platform admin is impersonating a user: says
// who is being viewed and ends the impersonation in one click.
//
// Stopping swaps the session cookie back to the admin, so (like an org
// switch) the cached gateway JWT and every query cache now belong to the wrong
// identity: clear both, then navigate to the admin Users page, whose _app
// beforeLoad re-resolves the AppSession as the admin.

import { useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { VenetianMaskIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { authClient } from "~/lib/auth/client";
import { clearGatewayToken } from "~/lib/api/token-provider";
import type { AppSession } from "~/lib/auth/session";

/** "Viewing as <user> · Stop impersonating" button for the top bar. */
export function ImpersonationButton({ session }: { session: AppSession }) {
  const router = useRouter();
  const qc = useQueryClient();
  const [stopping, setStopping] = useState(false);
  const who = session.user.name || session.user.email || "this user";

  const stop = async (): Promise<void> => {
    if (stopping) return;
    setStopping(true);
    try {
      const { error } = await authClient.admin.stopImpersonating();
      if (error) {
        toast.error(error.message ?? "Could not stop impersonating");
        return;
      }
      clearGatewayToken();
      qc.clear();
      await router.navigate({ to: "/admin/tenants", replace: true });
      toast.success(`Stopped impersonating ${who}`);
    } catch {
      toast.error("Could not stop impersonating");
    } finally {
      setStopping(false);
    }
  };

  return (
    <div
      className="flex h-8 max-w-80 shrink-0 items-center gap-2 rounded-lg bg-destructive/10 pr-0.5 pl-2.5 text-xs text-destructive dark:bg-destructive/20"
    >
      <VenetianMaskIcon className="size-3.5 shrink-0" aria-hidden />
      <span className="min-w-0 truncate max-sm:hidden">
        Viewing as <span className="font-semibold">{who}</span>
      </span>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={stopping}
        onClick={() => void stop()}
        title={`Return to your admin account (you are signed in as ${who})`}
        className="h-7 border-destructive/30 bg-background text-destructive hover:bg-destructive/5 hover:text-destructive"
      >
        {stopping ? "Stopping…" : "Stop impersonating"}
      </Button>
    </div>
  );
}
