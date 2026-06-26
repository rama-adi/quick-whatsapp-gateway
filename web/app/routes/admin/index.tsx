// /admin landing → redirect to all-sessions. Surface agent: admin.
import { redirect } from "react-router";

export function clientLoader() {
  throw redirect("/admin/sessions");
}

export default function AdminIndex() {
  return null;
}
