// Minimal "coming soon" card for routes whose surface isn't built yet.

import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";

export function Placeholder({
  title,
  description,
}: {
  title: string;
  description?: string;
}) {
  return (
    <Card className="mx-auto max-w-xl">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent className="text-sm text-muted-foreground">
        {description ?? "Coming soon — this surface is being built."}
      </CardContent>
    </Card>
  );
}
