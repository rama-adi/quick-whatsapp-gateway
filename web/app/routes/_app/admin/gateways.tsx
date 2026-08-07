import { useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { PlusIcon, RefreshCwIcon } from "lucide-react";
import { useAdminGateways, useCreateAdminGateway } from "~/lib/api/hooks/admin";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Textarea } from "~/components/ui/textarea";
import { Badge } from "~/components/ui/badge";
import { GatewayEnrollmentResult } from "./-gateway-enrollment";

export const Route = createFileRoute("/_app/admin/gateways")({ component: Gateways });

function Gateways() {
  const gateways = useAdminGateways();
  const create = useCreateAdminGateway();
  const [label, setLabel] = useState(""); const [capacity, setCapacity] = useState(""); const [notes, setNotes] = useState("");
  const [result, setResult] = useState<Parameters<typeof GatewayEnrollmentResult>[0]["result"] | null>(null);
  const submit = (event: React.FormEvent) => { event.preventDefault(); create.mutate({ label: label || undefined, notes: notes || undefined, capacity: capacity ? Number(capacity) : undefined }, { onSuccess: setResult }); };
  if (result) return <div className="space-y-4"><GatewayEnrollmentResult result={result} onDismiss={() => setResult(null)} /><Button asChild variant="outline"><Link to="/admin/gateways">Back to gateways</Link></Button></div>;
  return <div className="space-y-4">
    <div className="flex items-center justify-between"><div><h1 className="text-xl font-semibold">Gateways</h1><p className="text-sm text-muted-foreground">Platform gateway inventory and lifecycle control.</p></div><Button variant="outline" size="sm" onClick={() => void gateways.refetch()}><RefreshCwIcon className="mr-2 size-4" />Refresh</Button></div>
    <Card><CardHeader><CardTitle className="text-base">Add gateway</CardTitle><CardDescription>Only supported control-plane metadata is collected here.</CardDescription></CardHeader><CardContent><form className="grid gap-3 sm:grid-cols-2" onSubmit={submit}><div><Label htmlFor="gateway-label">Display name</Label><Input id="gateway-label" value={label} onChange={(e) => setLabel(e.target.value)} /></div><div><Label htmlFor="gateway-capacity">Capacity</Label><Input id="gateway-capacity" type="number" min="0" value={capacity} onChange={(e) => setCapacity(e.target.value)} /></div><div className="sm:col-span-2"><Label htmlFor="gateway-notes">Operator notes</Label><Textarea id="gateway-notes" value={notes} onChange={(e) => setNotes(e.target.value)} /></div><div className="sm:col-span-2"><Button disabled={create.isPending}><PlusIcon className="mr-2 size-4" />{create.isPending ? "Creating…" : "Create gateway"}</Button>{create.isError && <p className="mt-2 text-sm text-destructive">{isApiError(create.error) ? create.error.message : "Could not create gateway"}</p>}</div></form></CardContent></Card>
    <Card><CardHeader><CardTitle className="text-base">Inventory</CardTitle></CardHeader><CardContent>{gateways.isError ? <p className="text-sm text-destructive">{isApiError(gateways.error) ? gateways.error.message : "Could not load gateways"}</p> : gateways.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : gateways.data?.length ? <div className="space-y-2">{gateways.data.map((gateway) => <Link key={gateway.id} to="/admin/gateways/$gatewayId" params={{ gatewayId: gateway.id }} className="flex items-center justify-between rounded-md border p-3 hover:bg-muted"><div><p className="font-medium">{gateway.label || gateway.id}</p><p className="font-mono text-xs text-muted-foreground">{gateway.id}</p></div><Badge variant="secondary">{gateway.status}</Badge></Link>)}</div> : <p className="py-5 text-sm text-muted-foreground">No gateways yet.</p>}</CardContent></Card>
  </div>;
}
