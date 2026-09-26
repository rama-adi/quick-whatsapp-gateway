import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Card, CardHeader, CardTitle, CardContent } from "~/components/ui/card";
import { useStorageBuckets, useSaveStorage, useDeleteStorage, type StorageInput, type StorageBucket } from "~/lib/api/hooks/storage";

export const Route = createFileRoute("/_app/user/storage")({ component: Storage });
const empty: StorageInput = { name: "", endpoint: "", region: "", bucket: "", pathStyle: false, retentionDays: null, accessKey: "", secretKey: "" };
function Storage() {
  const buckets = useStorageBuckets();
  const save = useSaveStorage();
  const remove = useDeleteStorage();
  const [editing, setEditing] = useState<string>();
  const [form, setForm] = useState<StorageInput>({ ...empty });
  function edit(bucket: StorageBucket) { setEditing(bucket.id); setForm({ ...bucket, accessKey: "", secretKey: "" }); }
  return <div className="space-y-6 p-6">
    <div><h1 className="text-2xl font-semibold">Attachment storage</h1><p className="text-muted-foreground">Connect S3-compatible buckets for the active organization. Choose a bucket in each session’s overview.</p></div>
    {buckets.error && <p role="alert">{buckets.error.message}</p>}
    {buckets.isLoading && <p>Loading storage connections…</p>}
    <div className="grid gap-4 md:grid-cols-2">{buckets.data?.items.map(bucket => <Card key={bucket.id}><CardHeader><CardTitle>{bucket.name}</CardTitle></CardHeader><CardContent className="space-y-3">
      <p>{bucket.bucket}</p><p className="break-all text-sm text-muted-foreground">{bucket.endpoint}</p>
      <p>{bucket.retentionDays === null ? "Keep indefinitely" : `Keep for ${bucket.retentionDays} days`}</p>
      <div className="flex gap-2"><Button variant="outline" onClick={() => edit(bucket)}>Edit</Button><Button variant="outline" disabled={remove.isPending} onClick={() => remove.mutate(bucket.id, { onSuccess: () => toast.success("Storage connection removed"), onError: e => toast.error(e.message) })}>Remove unused connection</Button></div>
    </CardContent></Card>)}</div>
    <Card><CardHeader><CardTitle>{editing ? "Update storage connection" : "Connect a bucket"}</CardTitle></CardHeader><CardContent>
      <form className="grid gap-4 md:grid-cols-2" onSubmit={e => { e.preventDefault(); save.mutate({ id: editing, body: form }, { onSuccess: () => { toast.success("Storage connection saved"); setEditing(undefined); setForm({ ...empty }); }, onError: e => toast.error(e.message) }); }}>
        {([ ["name", "Connection name", "text"], ["endpoint", "S3 endpoint", "url"], ["region", "Region", "text"], ["bucket", "Bucket name", "text"], ["accessKey", "Access key", "password"], ["secretKey", "Secret key", "password"] ] as const).map(([key, label, type]) => <label className="grid gap-2 text-sm" key={key}>{label}<Input type={type} value={form[key]} autoComplete={type === "password" ? "new-password" : "off"} disabled={!!editing && ["endpoint", "region", "bucket"].includes(key)} required={!editing || !["accessKey", "secretKey"].includes(key)} onChange={e => setForm({ ...form, [key]: e.target.value })} /></label>)}
        <label className="flex items-center gap-2"><input type="checkbox" checked={form.pathStyle} disabled={!!editing} onChange={e => setForm({ ...form, pathStyle: e.target.checked })} />Use path-style S3 requests</label>
        <label className="flex items-center gap-2"><input type="checkbox" checked={form.retentionDays === null} onChange={e => setForm({ ...form, retentionDays: e.target.checked ? null : 1 })} />Keep attachments indefinitely</label>
        {form.retentionDays !== null && <label className="grid gap-2 text-sm">Retention in days<Input type="number" min={1} step={1} required value={form.retentionDays} onChange={e => setForm({ ...form, retentionDays: Number(e.target.value) })} /></label>}
        <p className="text-sm text-muted-foreground md:col-span-2">Retention starts when an attachment is captured. Updates apply to new attachments. Existing files keep their original expiry and bucket. For credential rotation, enter both keys.</p>
        <div className="flex gap-2"><Button disabled={save.isPending} type="submit">{save.isPending ? "Saving…" : "Save connection"}</Button>{editing && <Button type="button" variant="outline" onClick={() => { setEditing(undefined); setForm({ ...empty }); }}>Cancel</Button>}</div>
      </form>
    </CardContent></Card>
  </div>;
}
