// MDX components available to every docs page. Beyond Fumadocs' defaults we
// register APIPage, which the generated OpenAPI reference pages render (they
// look it up by name on props.components), plus Tabs/Tab and Accordion(s) so
// any page can use them without a per-file import.
import { Accordion, Accordions } from "fumadocs-ui/components/accordion";
import { Tab, Tabs } from "fumadocs-ui/components/tabs";
import defaultMdxComponents from "fumadocs-ui/mdx";
import type { MDXComponents } from "mdx/types";
import { APIPage } from "./openapi-page";

export function getMDXComponents(components?: MDXComponents) {
  return {
    ...defaultMdxComponents,
    APIPage,
    Tabs,
    Tab,
    Accordion,
    Accordions,
    ...components,
  } satisfies MDXComponents;
}

export const useMDXComponents = getMDXComponents;

declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
