// Local API hooks for contact endpoints that have NO shared hook yet:
// on-WhatsApp check, profile picture, about text, and block/unblock actions.
// These wrap the FROZEN shared client (fetchJSON/apiUrl) only — no new transport.
// sharedGap: candidates to hoist into ~/lib/api/hooks/contacts.ts in verify.

import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { qk } from "~/lib/query";
import { fetchJSON, apiUrl } from "~/lib/api/client";
import type { ApiError } from "~/lib/api/envelope";
import type { OnWhatsApp, ProfilePicture } from "~/lib/api/types";

type About = { about?: string };

/** GET /api/v1/sessions/{id}/contacts/check?phone= — on-WhatsApp probe. */
export function useCheckContact(
  s: string,
  phone: string,
  enabled: boolean,
): UseQueryResult<OnWhatsApp, ApiError> {
  return useQuery({
    queryKey: ["sessions", s, "contacts", "check", phone] as const,
    enabled: Boolean(s && phone && enabled),
    retry: false,
    queryFn: () =>
      fetchJSON<OnWhatsApp>(
        apiUrl(
          `/sessions/${encodeURIComponent(s)}/contacts/check?phone=${encodeURIComponent(phone)}`,
        ),
      ),
  });
}

/** GET /api/v1/sessions/{id}/contacts/{jid}/picture — may be 501 in v1. */
export function useContactPicture(
  s: string,
  jid: string,
  enabled: boolean,
): UseQueryResult<ProfilePicture, ApiError> {
  return useQuery({
    queryKey: ["sessions", s, "contacts", jid, "picture"] as const,
    enabled: Boolean(s && jid && enabled),
    retry: false,
    queryFn: () =>
      fetchJSON<ProfilePicture>(
        apiUrl(
          `/sessions/${encodeURIComponent(s)}/contacts/${encodeURIComponent(jid)}/picture`,
        ),
      ),
  });
}

/** GET /api/v1/sessions/{id}/contacts/{jid}/about — may be 501 in v1. */
export function useContactAbout(
  s: string,
  jid: string,
  enabled: boolean,
): UseQueryResult<About, ApiError> {
  return useQuery({
    queryKey: ["sessions", s, "contacts", jid, "about"] as const,
    enabled: Boolean(s && jid && enabled),
    retry: false,
    queryFn: () =>
      fetchJSON<About>(
        apiUrl(
          `/sessions/${encodeURIComponent(s)}/contacts/${encodeURIComponent(jid)}/about`,
        ),
      ),
  });
}

/** POST .../{jid}/block | .../{jid}/unblock — may be 501 in v1. */
export function useBlockContact(
  s: string,
): UseMutationResult<void, ApiError, { jid: string; block: boolean }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ jid, block }) =>
      fetchJSON<void>(
        apiUrl(
          `/sessions/${encodeURIComponent(s)}/contacts/${encodeURIComponent(jid)}/${block ? "block" : "unblock"}`,
        ),
        { method: "POST" },
      ),
    onSuccess: (_data, { jid }) => {
      void qc.invalidateQueries({ queryKey: qk.contact(s, jid) });
      void qc.invalidateQueries({ queryKey: ["sessions", s, "contacts"] });
    },
  });
}
