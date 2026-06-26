// Shared layout options for the docs site (nav title shown in the docs header).
import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: "WhatsApp Gateway",
    },
  };
}
