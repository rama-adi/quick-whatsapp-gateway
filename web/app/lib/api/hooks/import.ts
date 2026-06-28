// Backup-import hooks: upload a WhatsApp msgstore.db.crypt15 to backfill a
// session's history, and poll the import job's status. Mirrors the admin backfill
// hooks but is user-facing (manage capability) and uses multipart upload.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ApiError, isApiError } from "../envelope";
import type { BackfillImport } from "../types";
import { fetchFormData } from "../client";
import { apiUrl, fetchJSON } from "./_shared";

const backfillImportKey = (sessionId: string) =>
  ["sessions", sessionId, "backfill"] as const;

/**
 * Poll the latest backup import for a session. Returns null when none exists yet
 * (the gateway 404s), and auto-refreshes every 1.5s while one is running.
 */
export function useBackupImportStatus(sessionId: string) {
  return useQuery<BackfillImport | null, ApiError>({
    queryKey: backfillImportKey(sessionId),
    enabled: Boolean(sessionId),
    refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false),
    retry: false,
    queryFn: async () => {
      try {
        return await fetchJSON<BackfillImport>(
          apiUrl(`/sessions/${encodeURIComponent(sessionId)}/backfill`),
        );
      } catch (e) {
        if (isApiError(e) && e.status === 404) return null; // no import yet
        throw e;
      }
    },
  });
}

/** Upload a .crypt15 backup + key to start an import job for the session. */
export function useImportBackup(sessionId: string) {
  const qc = useQueryClient();
  return useMutation<BackfillImport, ApiError, { file: File; key: string }>({
    mutationFn: ({ file, key }) => {
      const form = new FormData();
      form.append("file", file);
      form.append("key", key);
      return fetchFormData<BackfillImport>(
        apiUrl(`/sessions/${encodeURIComponent(sessionId)}/backfill`),
        form,
      );
    },
    onSuccess: (job) => {
      qc.setQueryData(backfillImportKey(sessionId), job);
    },
  });
}
