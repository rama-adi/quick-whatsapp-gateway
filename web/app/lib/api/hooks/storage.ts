import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiUrl, fetchJSON } from "../client";

export type StorageBucket = {
  id: string; name: string; endpoint: string; region: string; bucket: string;
  pathStyle: boolean; retentionDays: number | null;
};
export type StorageInput = Omit<StorageBucket, "id"> & { accessKey: string; secretKey: string };
export function useStorageBuckets() {
  return useQuery({ queryKey: ["storage-buckets"], queryFn: () => fetchJSON<{items: StorageBucket[]}>(apiUrl("/storage/buckets")) });
}
export function useSaveStorage() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: ({ id, body }: { id?: string; body: StorageInput }) =>
    fetchJSON<StorageBucket>(apiUrl(`/storage/buckets${id ? `/${encodeURIComponent(id)}` : ""}`), { method: id ? "PUT" : "POST", body: JSON.stringify(body) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["storage-buckets"] }),
  });
}
export function useDeleteStorage() {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (id: string) => fetchJSON(apiUrl(`/storage/buckets/${encodeURIComponent(id)}`), { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["storage-buckets"] }),
  });
}
export function useSessionStorage(session: string) {
  return useQuery({ queryKey: ["sessions", session, "storage"], queryFn: () => fetchJSON<{bucketId: string | null}>(apiUrl(`/sessions/${encodeURIComponent(session)}/storage`)) });
}
export function useLinkStorage(session: string) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: (bucketId: string | null) => fetchJSON(apiUrl(`/sessions/${encodeURIComponent(session)}/storage`), { method: "PUT", body: JSON.stringify({bucketId}) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions", session, "storage"] }),
  });
}
