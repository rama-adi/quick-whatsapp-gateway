import { useEffect, useRef, useState } from "react";
import { DatabaseIcon, UploadIcon } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { useBackupImportStatus, useImportBackup } from "~/lib/api/hooks/import";
import { isApiError } from "~/lib/api/envelope";
import type { BackfillImport } from "~/lib/api/types";
import { qk } from "~/lib/query";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Badge } from "~/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { formatTimestamp } from "./user-ui";

// A 64-char hex key is the common case; raw/serialized keys also work server-side.
const HEX_KEY = /^[0-9a-fA-F]{64}$/;

export function BackupImportCard({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const status = useBackupImportStatus(sessionId);
  const importBackup = useImportBackup(sessionId);

  const [file, setFile] = useState<File | null>(null);
  const [key, setKey] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);

  const job = status.data ?? null;
  const running = job?.status === "running" || importBackup.isPending;

  // When an import finishes, refresh the session's chat list so the freshly
  // imported history shows up in the Chats tab without a manual reload.
  const lastDone = job?.status === "succeeded" ? job.finishedAt : undefined;
  useEffect(() => {
    if (!lastDone) return;
    void qc.invalidateQueries({ queryKey: qk.chats(sessionId) });
  }, [lastDone, qc, sessionId]);

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!file) {
      toast.error("Choose your msgstore.db.crypt15 file.");
      return;
    }
    const trimmedKey = key.trim();
    if (!trimmedKey) {
      toast.error("Paste your backup decryption key.");
      return;
    }
    if (!HEX_KEY.test(trimmedKey)) {
      toast.warning("That doesn't look like a 64-character key — trying anyway.");
    }
    importBackup.mutate(
      { file, key: trimmedKey },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Import failed to start"),
        onSuccess: () => {
          toast.success("Import started — this can take a while for large backups.");
          setFile(null);
          setKey("");
          if (fileRef.current) fileRef.current.value = "";
        },
      },
    );
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DatabaseIcon className="size-4" aria-hidden />
          Backup import
        </CardTitle>
        <CardDescription>
          Backfill this session's history from a WhatsApp end-to-end encrypted
          backup. Once per day per session.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="backup-file">Backup file (.crypt15)</Label>
            <Input
              id="backup-file"
              ref={fileRef}
              type="file"
              accept=".crypt15"
              disabled={running}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="backup-key">Decryption key</Label>
            <Input
              id="backup-key"
              value={key}
              disabled={running}
              onChange={(e) => setKey(e.target.value)}
              placeholder="64-character key"
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground">
              From WhatsApp {"->"} Settings {"->"} Chats {"->"} Chat backup {"->"}{" "}
              End-to-end encrypted backup.
            </p>
          </div>
          <Button type="submit" disabled={running} className="gap-1.5">
            <UploadIcon className="size-4" aria-hidden />
            {running ? "Importing..." : "Import"}
          </Button>
        </form>

        <ImportStatus job={job} loading={status.isLoading} />
      </CardContent>
    </Card>
  );
}

function ImportStatus({
  job,
  loading,
}: {
  job: BackfillImport | null;
  loading: boolean;
}) {
  if (loading || !job) return null;

  return (
    <div className="space-y-2 rounded-md border bg-muted/40 p-3 text-sm">
      <div className="flex items-center justify-between gap-2">
        <span className="text-muted-foreground">Last import</span>
        <StatusBadge status={job.status} />
      </div>
      {job.status === "failed" && job.error ? (
        <p className="text-xs text-destructive break-words">{job.error}</p>
      ) : (
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>{job.chats} chats</span>
          <span>{job.messages} messages</span>
          <span>{job.identities} contacts</span>
          <span>{job.groups} groups</span>
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        {job.status === "running"
          ? "Running..."
          : `Finished ${formatTimestamp(job.finishedAt)}`}
      </p>
    </div>
  );
}

function StatusBadge({ status }: { status: BackfillImport["status"] }) {
  const variant =
    status === "succeeded"
      ? "default"
      : status === "failed"
        ? "destructive"
        : "secondary";
  return <Badge variant={variant}>{status}</Badge>;
}
