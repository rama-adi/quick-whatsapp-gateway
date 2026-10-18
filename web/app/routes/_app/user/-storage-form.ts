// Attachment storage form logic (pure, tested): provider presets, retention
// choices, validation and the request body. Used only by storage.tsx.

import type { StorageBucket, StorageInput } from "~/lib/api/hooks/storage";

// --- providers --------------------------------------------------------------

export type ProviderId = "aws" | "r2" | "minio" | "other";

export type Provider = {
  id: ProviderId;
  label: string;
  /** Region filled in when the preset is picked ("" = leave for the user). */
  region: string;
  regionPlaceholder: string;
  endpointPlaceholder: string;
  pathStyle: boolean;
  endpointHint: string;
};

export const PROVIDERS: Provider[] = [
  {
    id: "aws",
    label: "AWS S3",
    region: "",
    regionPlaceholder: "us-east-1",
    endpointPlaceholder: "https://s3.us-east-1.amazonaws.com",
    pathStyle: false,
    endpointHint: "Filled in from the region: https://s3.<region>.amazonaws.com.",
  },
  {
    id: "r2",
    label: "Cloudflare R2",
    region: "auto",
    regionPlaceholder: "auto",
    endpointPlaceholder: "https://<account-id>.r2.cloudflarestorage.com",
    pathStyle: false,
    endpointHint:
      "Your account ID is on the R2 overview page in Cloudflare. Use an R2 API token with Object Read & Write.",
  },
  {
    id: "minio",
    label: "MinIO",
    region: "us-east-1",
    regionPlaceholder: "us-east-1",
    endpointPlaceholder: "https://minio.example.com",
    pathStyle: true,
    endpointHint:
      "Plain http:// only works on trusted internal hostnames, such as *.internal or *.svc.",
  },
  {
    id: "other",
    label: "Other",
    region: "",
    regionPlaceholder: "us-east-1",
    endpointPlaceholder: "https://s3.example.com",
    pathStyle: false,
    endpointHint:
      "Your provider's S3 API endpoint. Public endpoints must use https://.",
  },
];

export function providerById(id: ProviderId): Provider {
  return PROVIDERS.find((p) => p.id === id) ?? PROVIDERS[3]!;
}

/** Best-effort provider name for an existing connection's endpoint. */
export function guessProvider(endpoint: string): ProviderId {
  let host = "";
  try {
    host = new URL(endpoint).hostname;
  } catch {
    return "other";
  }
  if (host.endsWith(".amazonaws.com")) return "aws";
  if (host.endsWith(".r2.cloudflarestorage.com")) return "r2";
  if (host.includes("minio")) return "minio";
  return "other";
}

export function awsEndpoint(region: string): string {
  return `https://s3.${region.trim() || "us-east-1"}.amazonaws.com`;
}

// --- retention ----------------------------------------------------------------

export type RetentionChoice = "7" | "30" | "90" | "365" | "forever" | "custom";

export const RETENTION_CHOICES: { value: RetentionChoice; label: string }[] = [
  { value: "7", label: "7 days" },
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
  { value: "365", label: "1 year" },
  { value: "forever", label: "Keep forever" },
  { value: "custom", label: "Custom…" },
];

export function retentionLabel(days: number | null): string {
  if (days === null) return "Kept forever";
  if (days === 365) return "1 year";
  return days === 1 ? "1 day" : `${days} days`;
}

// --- form state -----------------------------------------------------------------

export type StorageFormState = {
  provider: ProviderId;
  name: string;
  endpoint: string;
  /** Once the user edits the endpoint, the AWS preset stops deriving it. */
  endpointTouched: boolean;
  region: string;
  bucket: string;
  pathStyle: boolean;
  /** Edit only: false keeps the saved credentials. Always true on create. */
  replaceCredentials: boolean;
  accessKey: string;
  secretKey: string;
  retention: RetentionChoice;
  /** Raw text of the custom-days input (kept as a string so "" isn't 0). */
  retentionDays: string;
};

export type StorageFieldErrors = Partial<
  Record<
    "name" | "endpoint" | "region" | "bucket" | "accessKey" | "secretKey" | "retentionDays",
    string
  >
>;

export function emptyStorageForm(): StorageFormState {
  return {
    provider: "aws",
    name: "",
    endpoint: "",
    endpointTouched: false,
    region: "",
    bucket: "",
    pathStyle: false,
    replaceCredentials: true,
    accessKey: "",
    secretKey: "",
    retention: "90",
    retentionDays: "",
  };
}

export function storageFormFromBucket(b: StorageBucket): StorageFormState {
  const preset = RETENTION_CHOICES.find(
    (c) => b.retentionDays !== null && c.value === String(b.retentionDays),
  );
  return {
    provider: guessProvider(b.endpoint),
    name: b.name,
    endpoint: b.endpoint,
    endpointTouched: true,
    region: b.region,
    bucket: b.bucket,
    pathStyle: b.pathStyle,
    replaceCredentials: false,
    accessKey: "",
    secretKey: "",
    retention:
      b.retentionDays === null ? "forever" : preset ? preset.value : "custom",
    retentionDays: b.retentionDays === null ? "" : String(b.retentionDays),
  };
}

/** Pick a provider preset: fills region, path-style and (AWS) the endpoint. */
export function applyProvider(s: StorageFormState, id: ProviderId): StorageFormState {
  const p = providerById(id);
  const region = p.region || (s.provider === "r2" || s.provider === "minio" ? "" : s.region);
  return {
    ...s,
    provider: id,
    region,
    pathStyle: p.pathStyle,
    endpoint: id === "aws" ? (region ? awsEndpoint(region) : "") : s.endpointTouched ? s.endpoint : "",
    endpointTouched: id === "aws" ? false : s.endpointTouched,
  };
}

/** Set the region; the AWS preset keeps the endpoint in step until edited. */
export function setRegion(s: StorageFormState, region: string): StorageFormState {
  const follow = s.provider === "aws" && !s.endpointTouched;
  return {
    ...s,
    region,
    endpoint: follow ? (region.trim() ? awsEndpoint(region) : "") : s.endpoint,
  };
}

/** Retention in days for the request, or null to keep forever. NaN = invalid. */
export function retentionDaysValue(s: StorageFormState): number | null {
  if (s.retention === "forever") return null;
  if (s.retention !== "custom") return Number(s.retention);
  const raw = s.retentionDays.trim();
  if (!/^\d+$/.test(raw)) return Number.NaN;
  return Number(raw);
}

export function storageFieldErrors(
  s: StorageFormState,
  mode: "create" | "edit",
): StorageFieldErrors {
  const errors: StorageFieldErrors = {};
  if (!s.name.trim()) errors.name = "Give the connection a name.";
  if (mode === "create") {
    const endpoint = s.endpoint.trim();
    if (!endpoint) {
      errors.endpoint = "Enter the S3 endpoint URL.";
    } else {
      try {
        const u = new URL(endpoint);
        if (u.protocol !== "https:" && u.protocol !== "http:") {
          errors.endpoint = "The endpoint must start with https://.";
        } else if (u.search || u.hash || u.username) {
          errors.endpoint = "Use just the endpoint, without a query, fragment or login.";
        }
      } catch {
        errors.endpoint = "Enter a full URL, like https://s3.us-east-1.amazonaws.com.";
      }
    }
    if (!s.region.trim()) errors.region = "Enter the bucket's region.";
    const bucket = s.bucket.trim();
    if (!bucket) errors.bucket = "Enter the bucket name.";
    else if (/[/\\]/.test(bucket)) errors.bucket = "Enter only the bucket name, without slashes.";
  }
  if (mode === "create" || s.replaceCredentials) {
    if (!s.accessKey.trim()) errors.accessKey = "Enter the access key ID.";
    if (!s.secretKey) errors.secretKey = "Enter the secret access key.";
  }
  const days = retentionDaysValue(s);
  if (days !== null && (!Number.isInteger(days) || days < 1)) {
    errors.retentionDays = "Enter a whole number of days, 1 or more.";
  }
  return errors;
}

/** The PUT/POST body. On edit, the destination comes from the saved bucket
 * (the API refuses to change it) and blank keys keep the saved credentials. */
export function toStorageInput(
  s: StorageFormState,
  original?: StorageBucket,
): StorageInput {
  const sendKeys = !original || s.replaceCredentials;
  return {
    name: s.name.trim(),
    endpoint: original ? original.endpoint : s.endpoint.trim(),
    region: original ? original.region : s.region.trim(),
    bucket: original ? original.bucket : s.bucket.trim(),
    pathStyle: original ? original.pathStyle : s.pathStyle,
    retentionDays: retentionDaysValue(s),
    accessKey: sendKeys ? s.accessKey.trim() : "",
    secretKey: sendKeys ? s.secretKey : "",
  };
}

/** Route an API validation message to the field it's about, if any. */
export function fieldForServerError(message: string): keyof StorageFieldErrors | null {
  const m = message.toLowerCase();
  if (m.startsWith("endpoint")) return "endpoint";
  if (m.startsWith("bucket must")) return "bucket";
  if (m.includes("accesskey") || m.includes("secretkey")) return "accessKey";
  if (m.startsWith("retentiondays")) return "retentionDays";
  return null;
}
