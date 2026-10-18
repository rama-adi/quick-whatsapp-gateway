// Ready-to-run request snippets (curl / JavaScript / Python) for an API call,
// rendered by <CodeSnippet tabs={…}>. Pure string builders, no dependencies.

/** One language tab of a code snippet. */
export interface SnippetTab {
  id: string;
  label: string;
  code: string;
}

export interface ApiRequestSnippetInput {
  /** HTTP method. Default "GET". */
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  /** Absolute URL, e.g. `${usePublicApiUrl()}/sessions`. */
  url: string;
  /** API key (or a placeholder such as "$WA_API_KEY"). Sent as a Bearer token. */
  token: string;
  /** JSON body; omitted for GET/DELETE when undefined. */
  body?: unknown;
}

const shellQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`;

/** Build curl / JavaScript (fetch) / Python (requests) snippets for one API request. */
export function apiRequestSnippets({
  method = "GET",
  url,
  token,
  body,
}: ApiRequestSnippetInput): SnippetTab[] {
  const hasBody = body !== undefined;
  const json = hasBody ? JSON.stringify(body, null, 2) : "";
  const compactJson = hasBody ? JSON.stringify(body) : "";

  const curl = [
    `curl${method === "GET" ? "" : ` -X ${method}`} ${shellQuote(url)}`,
    `  -H ${shellQuote(`Authorization: Bearer ${token}`)}`,
    ...(hasBody
      ? [`  -H 'Content-Type: application/json'`, `  -d ${shellQuote(compactJson)}`]
      : []),
  ].join(" \\\n");

  const jsHeaders = hasBody
    ? `{\n    Authorization: ${JSON.stringify(`Bearer ${token}`)},\n    "Content-Type": "application/json",\n  }`
    : `{ Authorization: ${JSON.stringify(`Bearer ${token}`)} }`;
  const js = [
    `const res = await fetch(${JSON.stringify(url)}, {`,
    `  method: ${JSON.stringify(method)},`,
    `  headers: ${jsHeaders},`,
    ...(hasBody ? [`  body: JSON.stringify(${json.replace(/\n/g, "\n  ")}),`] : []),
    `});`,
    `console.log(await res.json());`,
  ].join("\n");

  const py = [
    `import requests`,
    ``,
    `res = requests.${method.toLowerCase()}(`,
    `    ${JSON.stringify(url)},`,
    `    headers={"Authorization": ${JSON.stringify(`Bearer ${token}`)}},`,
    ...(hasBody ? [`    json=${toPython(json).replace(/\n/g, "\n    ")},`] : []),
    `)`,
    `print(res.json())`,
  ].join("\n");

  return [
    { id: "curl", label: "curl", code: curl },
    { id: "js", label: "JavaScript", code: js },
    { id: "python", label: "Python", code: py },
  ];
}

// JSON literals → Python literals (true/false/null outside strings).
function toPython(json: string): string {
  return json.replace(/"(?:[^"\\]|\\.)*"|\btrue\b|\bfalse\b|\bnull\b/g, (m) =>
    m === "true" ? "True" : m === "false" ? "False" : m === "null" ? "None" : m,
  );
}
