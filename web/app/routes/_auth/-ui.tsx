// Surface-private UI for the auth forms (login / register / 2fa). The leading
// "-" keeps this file out of the route tree.
//
// AuthCard: the one card shape every auth form uses. The submit button lives
// inside the card body; the secondary "Don't have an account?" style line sits
// UNDER the card as plain text, not in a tinted footer strip (the strip made
// the last field look clipped).

import type { ReactNode } from "react";
import { CircleAlertIcon } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";

export function AuthCard({
  title,
  description,
  children,
  footer,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  /** Plain text line under the card (links to the other auth pages). */
  footer?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-5">
      <Card className="gap-6 py-6 shadow-[0_1px_2px_rgb(0_0_0/0.04),0_12px_32px_-20px_rgb(0_0_0/0.18)]">
        <CardHeader className="px-6">
          <CardTitle className="text-xl">{title}</CardTitle>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </CardHeader>
        <CardContent className="px-6">{children}</CardContent>
      </Card>
      {footer ? (
        <div className="text-center text-sm text-pretty text-muted-foreground">{footer}</div>
      ) : null}
    </div>
  );
}

/** Form-level error (wrong password, rate limit …), announced to screen readers. */
export function FormAlert({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg bg-destructive/10 px-3 py-2.5 text-sm text-destructive animate-in fade-in-0 duration-200 ease-soft"
    >
      <CircleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="min-w-0">{children}</span>
    </div>
  );
}

/** Inline text link styling used under the auth cards. */
export const authLinkClass =
  "font-medium text-foreground underline-offset-4 hover:underline";
