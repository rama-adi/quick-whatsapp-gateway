// Public auth layout (no app shell). Surface agent: auth.
// Branded, centered shell shared by login / register / 2fa.

import { MessageSquareText } from "lucide-react";
import { Outlet } from "react-router";

export default function AuthLayout() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-6 bg-muted/30 p-4">
      <div className="flex flex-col items-center gap-2 text-center">
        <div className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <MessageSquareText className="size-6" aria-hidden="true" />
        </div>
        <h1 className="text-lg font-semibold tracking-tight">WA Gateway</h1>
        <p className="text-sm text-muted-foreground">Realtime WhatsApp dashboard</p>
      </div>
      <div className="w-full max-w-sm">
        <Outlet />
      </div>
    </div>
  );
}
