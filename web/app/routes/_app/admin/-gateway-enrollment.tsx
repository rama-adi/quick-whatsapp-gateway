import { Button } from "~/components/ui/button";
import { CopyButton } from "~/components/copyable";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card";
import type { components } from "~/lib/api/schema";

type Enrollment = components["schemas"]["GatewayEnrollmentResult"];

/** This component receives a token only from mutation state; it never writes it to a URL or cache. */
export function GatewayEnrollmentResult({ result, onDismiss }: { result: Enrollment; onDismiss: () => void }) {
  const config = `GATEWAY_ID=${result.gatewayId}\nGATEWAY_ENROLLMENT_TOKEN=${result.token}`;
  return <Card className="border-amber-500/50">
    <CardHeader><CardTitle>Enrollment token — copy now</CardTitle><CardDescription>This single-use token is shown only in this response. Leaving this view permanently hides it.</CardDescription></CardHeader>
    <CardContent className="space-y-3">
      <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs"><code>{config}</code></pre>
      <p className="text-sm text-muted-foreground">Expires {new Date(result.expiresAt).toLocaleString()}.</p>
      <div className="flex gap-2"><CopyButton value={config} label="Copy configuration" variant="default" size="default" /><Button type="button" variant="outline" onClick={onDismiss}>I copied it</Button></div>
    </CardContent>
  </Card>;
}
