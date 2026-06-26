// Minimal "coming soon" placeholder so the build is green before surface agents
// expand each route. FROZEN — owned by the foundation agent. Surface agents
// REPLACE the route module body; this component is only the initial filler.

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
