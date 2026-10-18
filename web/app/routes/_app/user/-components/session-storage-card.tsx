// Per-session attachment storage: pick which organization bucket stores this
// session's new media, or none.

import { Link } from "@tanstack/react-router";
import { ArrowRightIcon, HardDriveIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";
import { useStorageBuckets, useSessionStorage, useLinkStorage } from "~/lib/api/hooks/storage";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card";
import { Button } from "~/components/ui/button";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { useConfirm } from "~/components/confirm-dialog";

const NONE = "__none__";

export function SessionStorageCard({ sessionId }: { sessionId: string }) {
  const buckets = useStorageBuckets();
  const binding = useSessionStorage(sessionId);
  const link = useLinkStorage(sessionId);
  const confirm = useConfirm();

  const items = buckets.data?.items ?? [];
  const current = binding.data?.bucketId ?? null;
  const loading = buckets.isPending || binding.isPending;
  const error = buckets.error ?? binding.error;

  const change = async (value: string): Promise<void> => {
    const next = value === NONE ? null : value;
    if (next === current) return;
    if (next === null) {
      const ok = await confirm({
        title: "Stop storing attachments?",
        description:
          "New media for this session won't be saved and shows as “not downloaded”. Files already stored stay until their retention ends.",
        confirmLabel: "Stop storing",
        destructive: true,
      });
      if (!ok) return;
    }
    const name = items.find((b) => b.id === next)?.name;
    link.mutate(next, {
      onSuccess: () =>
        toast.success(next ? `Attachments now go to ${name ?? "the bucket"}` : "Attachment storage turned off"),
      onError: (e) => toast.error(e.message),
    });
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Attachment storage</CardTitle>
        <CardDescription>
          Where this session saves photos, videos and documents it receives.
          Switching keeps existing files on their original retention schedule.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {error ? (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2.5 text-sm text-destructive"
          >
            <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{error.message || "Couldn't load storage settings."}</span>
          </div>
        ) : loading ? (
          <Skeleton className="h-8 w-full max-w-sm" />
        ) : items.length === 0 ? (
          <div className="flex flex-col gap-3 rounded-lg border border-dashed px-4 py-3.5 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-start gap-3">
              <HardDriveIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
              <p className="text-sm text-muted-foreground">
                <span className="font-medium text-foreground">No storage connected.</span>{" "}
                Without it, media shows as “not downloaded”.
              </p>
            </div>
            <Button asChild size="sm" variant="outline" className="shrink-0 self-start sm:self-auto">
              <Link to="/user/storage">
                Connect a bucket
                <ArrowRightIcon aria-hidden data-icon="inline-end" />
              </Link>
            </Button>
          </div>
        ) : (
          <div className="grid gap-1.5">
            <Label htmlFor={`storage-${sessionId}`}>Store attachments in</Label>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              <Select
                value={current ?? NONE}
                disabled={link.isPending}
                onValueChange={(v) => void change(v)}
              >
                <SelectTrigger id={`storage-${sessionId}`} className="w-full sm:w-80">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>Don't store attachments</SelectItem>
                  {items.map((b) => (
                    <SelectItem key={b.id} value={b.id}>
                      {b.name}
                      <span className="text-muted-foreground">({b.bucket})</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Link
                to="/user/storage"
                className="text-sm font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
              >
                Manage storage
              </Link>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
