import { describe, expect, it } from "vitest";
import {
  applyProvider,
  emptyStorageForm,
  fieldForServerError,
  guessProvider,
  setRegion,
  storageFieldErrors,
  storageFormFromBucket,
  toStorageInput,
} from "./-storage-form";
import type { StorageBucket } from "~/lib/api/hooks/storage";

const bucket: StorageBucket = {
  id: "b1",
  name: "Media",
  endpoint: "https://s3.eu-west-1.amazonaws.com",
  region: "eu-west-1",
  bucket: "media",
  pathStyle: false,
  retentionDays: 45,
};

const filled = () => ({
  ...setRegion(emptyStorageForm(), "us-east-1"),
  name: "Prod",
  bucket: "media",
  accessKey: "AK",
  secretKey: "SK",
});

describe("storage form", () => {
  it("never sends 0 when the custom retention field is cleared", () => {
    const state = { ...filled(), retention: "custom" as const, retentionDays: "" };
    expect(storageFieldErrors(state, "create").retentionDays).toBeDefined();
    const ok = { ...state, retentionDays: "14" };
    expect(storageFieldErrors(ok, "create")).toEqual({});
    expect(toStorageInput(ok).retentionDays).toBe(14);
  });

  it("sends null for keep forever and the preset number otherwise", () => {
    expect(toStorageInput({ ...filled(), retention: "forever" }).retentionDays).toBeNull();
    expect(toStorageInput({ ...filled(), retention: "30" }).retentionDays).toBe(30);
  });

  it("derives the AWS endpoint from the region until the endpoint is edited", () => {
    const s = setRegion(emptyStorageForm(), "ap-southeast-1");
    expect(s.endpoint).toBe("https://s3.ap-southeast-1.amazonaws.com");
    const touched = { ...s, endpoint: "https://custom", endpointTouched: true };
    expect(setRegion(touched, "us-west-2").endpoint).toBe("https://custom");
  });

  it("applies provider presets", () => {
    const minio = applyProvider(emptyStorageForm(), "minio");
    expect(minio.pathStyle).toBe(true);
    expect(minio.region).toBe("us-east-1");
    const r2 = applyProvider(minio, "r2");
    expect(r2.region).toBe("auto");
    expect(r2.pathStyle).toBe(false);
  });

  it("keeps saved credentials and destination on edit", () => {
    const state = { ...storageFormFromBucket(bucket), name: "Renamed" };
    expect(state.retention).toBe("custom");
    expect(storageFieldErrors(state, "edit")).toEqual({});
    const body = toStorageInput({ ...state, bucket: "ignored" }, bucket);
    expect(body).toMatchObject({
      name: "Renamed",
      bucket: "media",
      accessKey: "",
      secretKey: "",
      retentionDays: 45,
    });
  });

  it("requires both keys when replacing credentials", () => {
    const state = { ...storageFormFromBucket(bucket), replaceCredentials: true };
    const errors = storageFieldErrors(state, "edit");
    expect(errors.accessKey).toBeDefined();
    expect(errors.secretKey).toBeDefined();
  });

  it("guesses providers and routes server errors to fields", () => {
    expect(guessProvider("https://abc.r2.cloudflarestorage.com")).toBe("r2");
    expect(guessProvider("nope")).toBe("other");
    expect(
      fieldForServerError("endpoint must use HTTPS, or HTTP on a trusted internal domain"),
    ).toBe("endpoint");
    expect(fieldForServerError("something else")).toBeNull();
  });
});
