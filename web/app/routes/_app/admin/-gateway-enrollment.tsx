import { useState } from "react";
import { CopyIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card";
import type { components } from "~/lib/api/schema";

type Enrollment = components["schemas"]["GatewayEnrollmentResult"];

/** This component receives a token only from mutation state; it never writes it to a URL or cache. */
export function GatewayEnrollmentResult({ result, onDismiss }: { result: Enrollment; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);
  const config = `GATEWAY_ID=${result.gatewayId}\nGATEWAY_ENROLLMENT_TOKEN=${result.token}`;
  const copy = async () => {
    await navigator.clipboard.writeText(config);
    setCopied(true);
  };
  return <Card className="border-amber-500/50">
    <CardHeader><CardTitle>Enrollment token — copy now</CardTitle><CardDescription>This single-use token is shown only in this response. Leaving this view permanently hides it.</CardDescription></CardHeader>
    <CardContent className="space-y-3">
      <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs"><code>{config}</code></pre>
      <p className="text-sm text-muted-foreground">Expires {new Date(result.expiresAt).toLocaleString()}.</p>
      <div className="flex gap-2"><Button type="button" onClick={() => void copy()}><CopyIcon className="mr-2 size-4" />{copied ? "Copied" : "Copy configuration"}</Button><Button type="button" variant="outline" onClick={onDismiss}>I copied it</Button></div>
    </CardContent>
  </Card>;
}
