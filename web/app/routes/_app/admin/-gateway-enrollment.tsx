import { ServerIcon } from "lucide-react";
import { SecretRevealDialog } from "~/components/secret-reveal";
import { formatDateTime, formatRelative } from "~/lib/format";
import type { components } from "~/lib/api/schema";

export type Enrollment = components["schemas"]["GatewayEnrollmentResult"];

/**
 * One-time reveal of a gateway enrollment token as the env block the gateway
 * process reads. The token lives only in the caller's mutation state: it is
 * never written to a URL or the query cache, and the dialog can only be closed
 * with "I've saved it".
 */
export function GatewayEnrollmentDialog({
  result,
  title = "Enroll your gateway",
  onClose,
}: {
  result: Enrollment | null;
  title?: string;
  onClose: () => void;
}) {
  const config = result
    ? `GATEWAY_ID=${result.gatewayId}\nGATEWAY_ENROLLMENT_TOKEN=${result.token}`
    : "";
  return (
    <SecretRevealDialog
      open={Boolean(result)}
      secret={config}
      title={title}
      icon={<ServerIcon className="size-5 text-muted-foreground" aria-hidden />}
      secretLabel="Gateway environment"
      copyLabel="Copy environment"
      description={
        result ? (
          <>
            The enrollment token is single-use and expires{" "}
            <span title={formatDateTime(result.expiresAt, { seconds: true })} className="font-medium text-foreground">
              {formatRelative(result.expiresAt)}
            </span>
            . Copy it into the gateway's environment before you close this.
          </>
        ) : undefined
      }
      onClose={onClose}
    >
      <div className="grid gap-2 text-sm">
        <div className="font-medium">Next steps</div>
        <ol className="grid list-decimal gap-1.5 pl-5 text-muted-foreground marker:text-muted-foreground/70">
          <li>
            Add both lines to the gateway's environment (for the bundled compose
            files, <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">deploy/.env</code>).
          </li>
          <li>
            Start or restart the gateway. It redeems the token on its first
            connection and receives its certificate.
          </li>
          <li>
            When this page shows the gateway as <span className="font-medium text-foreground">Active</span>,
            remove <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">GATEWAY_ENROLLMENT_TOKEN</code>.
            It can't be reused.
          </li>
        </ol>
      </div>
    </SecretRevealDialog>
  );
}
