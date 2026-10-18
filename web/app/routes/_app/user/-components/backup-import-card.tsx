// Backfill a session's history from a WhatsApp end-to-end encrypted backup
// (msgstore.db.crypt15 + its 64-hex-digit key). One successful import per
// session per 24 hours (platform admins are exempt).

import { useEffect, useRef, useState } from "react";
import {
  ArrowUpRightIcon,
  CheckCircle2Icon,
  EyeIcon,
  EyeOffIcon,
  TriangleAlertIcon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { useBackupImportStatus, useImportBackup } from "~/lib/api/hooks/import";
import { isApiError } from "~/lib/api/envelope";
import type { BackfillImport } from "~/lib/api/types";
import { useAppSession } from "~/lib/auth/context";
import { DOCS } from "~/lib/docs-links";
import { qk } from "~/lib/query";
import { cn } from "~/lib/utils";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Spinner } from "~/components/ui/spinner";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { Time, useNow } from "~/components/time";

// A 64-char hex key is the common case; raw/serialized keys also work server-side.
const HEX_KEY = /^[0-9a-fA-F]{64}$/;
const QUOTA_MS = 24 * 60 * 60 * 1000;

export function BackupImportCard({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const app = useAppSession();
  const status = useBackupImportStatus(sessionId);
  const importBackup = useImportBackup(sessionId);
  const now = useNow();

  const [file, setFile] = useState<File | null>(null);
  const [key, setKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [touched, setTouched] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const job = status.data ?? null;
  const running = job?.status === "running" || importBackup.isPending;

  // The quota counts from the last *successful* import's start (admins exempt).
  const nextAllowedAt =
    job?.status === "succeeded" && !app.user.roles.includes("super_admin")
      ? job.createdAt + QUOTA_MS
      : undefined;
  const coolingDown = Boolean(nextAllowedAt && now && now < nextAllowedAt);

  // When an import finishes, refresh the session's chat list so the freshly
  // imported history shows up in the Chats tab without a manual reload.
  const lastDone = job?.status === "succeeded" ? job.finishedAt : undefined;
  useEffect(() => {
    if (!lastDone) return;
    void qc.invalidateQueries({ queryKey: qk.chats(sessionId) });
  }, [lastDone, qc, sessionId]);

  const cleanKey = key.replace(/\s+/g, "");
  const keyLooksOff = cleanKey.length > 0 && !HEX_KEY.test(cleanKey);
  const fileMissing = touched && !file;
  const keyMissing = touched && cleanKey.length === 0;

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    setTouched(true);
    if (!file || !cleanKey) return;
    importBackup.mutate(
      { file, key: cleanKey },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "The import didn't start"),
        onSuccess: () => {
          toast.success("Import started. Large backups can take a few minutes.");
          setFile(null);
          setKey("");
          setTouched(false);
          if (fileRef.current) fileRef.current.value = "";
        },
      },
    );
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Import chat history</CardTitle>
        <CardDescription>
          Load older chats and messages from your phone's end-to-end encrypted
          WhatsApp backup. One import per day.{" "}
          <a
            href={DOCS.backfillHistory}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-0.5 font-medium whitespace-nowrap text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
          >
            How to get your backup file and key
            <ArrowUpRightIcon className="size-3.5" aria-hidden />
          </a>
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <ImportStatus job={job} loading={status.isLoading} />

        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid content-start gap-1.5">
              <Label htmlFor="backup-file">Backup file</Label>
              <Input
                id="backup-file"
                ref={fileRef}
                type="file"
                accept=".crypt15"
                disabled={running}
                aria-invalid={fileMissing || undefined}
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
                className="text-muted-foreground file:mr-2 file:rounded-md file:bg-muted file:px-2 file:text-foreground"
              />
              <p className={cn("text-xs", fileMissing ? "text-destructive" : "text-muted-foreground")}>
                {fileMissing ? "Choose your backup file." : (
                  <>
                    Usually <span className="font-mono">msgstore.db.crypt15</span>.
                  </>
                )}
              </p>
            </div>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="backup-key">Encryption key</Label>
              <div className="relative">
                <Input
                  id="backup-key"
                  type={showKey ? "text" : "password"}
                  value={key}
                  disabled={running}
                  aria-invalid={keyMissing || undefined}
                  onChange={(e) => setKey(e.target.value)}
                  placeholder="64-digit key"
                  autoComplete="off"
                  spellCheck={false}
                  className="pr-9 font-mono text-xs"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
                  onClick={() => setShowKey((v) => !v)}
                  aria-label={showKey ? "Hide key" : "Show key"}
                >
                  {showKey ? <EyeOffIcon aria-hidden /> : <EyeIcon aria-hidden />}
                </Button>
              </div>
              <p
                className={cn(
                  "text-xs",
                  keyMissing ? "text-destructive" : keyLooksOff ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground",
                )}
              >
                {keyMissing
                  ? "Paste the key you saved when turning on encrypted backup."
                  : keyLooksOff
                    ? `Usually 64 characters (0–9, a–f); this one has ${cleanKey.length}. We'll try it anyway.`
                    : "Spaces are ignored. WhatsApp → Settings → Chats → Chat backup."}
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={running || coolingDown}>
              {running ? <Spinner /> : <UploadIcon aria-hidden data-icon="inline-start" />}
              {running ? "Importing…" : "Import backup"}
            </Button>
            {coolingDown && nextAllowedAt && (
              <span className="text-xs text-muted-foreground">
                Next import available <Time value={nextAllowedAt} format="absolute" />
              </span>
            )}
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

const STATUS_LABEL: Record<BackfillImport["status"], string> = {
  running: "Importing",
  succeeded: "Imported",
  failed: "Failed",
};

function ImportStatus({ job, loading }: { job: BackfillImport | null; loading: boolean }) {
  if (loading || !job) return null;
  const failed = job.status === "failed";
  return (
    <div
      className={cn(
        "grid gap-1.5 rounded-lg border px-3.5 py-3 text-sm",
        failed ? "border-destructive/30 bg-destructive/5" : "bg-muted/40",
      )}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {job.status === "running" ? (
          <Spinner className="text-muted-foreground" />
        ) : failed ? (
          <TriangleAlertIcon className="size-4 text-destructive" aria-hidden />
        ) : (
          <CheckCircle2Icon className="size-4 text-emerald-600 dark:text-emerald-400" aria-hidden />
        )}
        <span className="font-medium">Last import: {STATUS_LABEL[job.status]}</span>
        <span className="text-muted-foreground">
          {job.status === "running" ? (
            <>
              started <Time value={job.createdAt} />
            </>
          ) : (
            <Time value={job.finishedAt ?? job.createdAt} />
          )}
        </span>
      </div>
      {failed && job.error ? (
        <p className="text-xs break-words text-destructive">{job.error}</p>
      ) : (
        <p className="text-xs text-muted-foreground tabular-nums">
          {job.chats.toLocaleString()} chats · {job.messages.toLocaleString()} messages ·{" "}
          {job.identities.toLocaleString()} contacts · {job.groups.toLocaleString()} groups
        </p>
      )}
    </div>
  );
}
