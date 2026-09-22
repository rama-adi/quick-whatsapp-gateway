import { useStorageBuckets, useSessionStorage, useLinkStorage } from "~/lib/api/hooks/storage";
import { Card, CardHeader, CardTitle, CardContent } from "~/components/ui/card";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
export function SessionStorageCard({sessionId}:{sessionId:string}) {
  const buckets=useStorageBuckets(); const binding=useSessionStorage(sessionId); const link=useLinkStorage(sessionId);
  return <Card><CardHeader><CardTitle>Attachment storage</CardTitle></CardHeader><CardContent className="space-y-3">
    <p className="text-sm text-muted-foreground">Store new attachments in an organization bucket. Switching or unlinking keeps existing files on their original retention schedule.</p>
    {(buckets.error || binding.error) && <p role="alert">{buckets.error?.message ?? binding.error?.message}</p>}
    <label className="grid gap-2 text-sm">S3 connection<select className="rounded-md border bg-background p-2" value={binding.data?.bucketId ?? ""} disabled={buckets.isPending || binding.isPending || link.isPending || !!buckets.error || !!binding.error} onChange={e => link.mutate(e.target.value || null, {onSuccess:()=>toast.success("Session storage updated"),onError:e=>toast.error(e.message)})}>
      <option value="">Capture disabled</option>{buckets.data?.items.map(b=><option key={b.id} value={b.id}>{b.name} — {b.bucket}</option>)}
    </select></label><Link to="/user/storage" className="text-sm underline">Manage organization storage</Link>
  </CardContent></Card>;
}
