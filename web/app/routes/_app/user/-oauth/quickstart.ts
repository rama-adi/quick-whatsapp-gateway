// Generated relying-app quickstart snippets for the integration-guide tab
// (oauth.md §6.2). Aim: read like a Stripe/Auth0 quickstart — copy, paste, run.
// Pure string builders so the exact output is testable.

import { discoveryUrl } from "./validation";

export interface QuickstartInput {
  issuer: string; // router origin
  clientId: string;
  clientType: "confidential" | "public";
  redirectUri: string;
  scopes: string[];
}

/** A runnable Node script using the standard `openid-client` library. Confidential
 * clients read the secret from an env var (never inlined); public clients rely on
 * PKCE alone (openid-client generates the verifier/challenge). */
export function nodeQuickstart(input: QuickstartInput): string {
  const { issuer, clientId, clientType, redirectUri, scopes } = input;
  const isConfidential = clientType === "confidential";
  const scopeStr = scopes.join(" ");
  const secretLine = isConfidential
    ? `  client_secret: process.env.WA_CLIENT_SECRET, // shown once on create / rotate`
    : `  token_endpoint_auth_method: "none", // public client — PKCE only, no secret`;

  return `// npm install openid-client express
import express from "express";
import * as client from "openid-client";

// 1. Discover the provider (endpoints, JWKS, supported features).
const config = await client.discovery(
  new URL(${q(discoveryUrl(issuer))}),
  ${q(clientId)},
  {
${secretLine}
  },
);

const app = express();
const redirect_uri = ${q(redirectUri)};

// 2. Kick off login: PKCE + state, then redirect to WhatsApp sign-in.
app.get("/login", async (req, res) => {
  const code_verifier = client.randomPKCECodeVerifier();
  const code_challenge = await client.calculatePKCECodeChallenge(code_verifier);
  const state = client.randomState();
  req.session = { code_verifier, state }; // persist for the callback

  const url = client.buildAuthorizationUrl(config, {
    redirect_uri,
    scope: ${q(scopeStr)},
    code_challenge,
    code_challenge_method: "S256",
    state,
  });
  res.redirect(url.href);
});

// 3. Handle the callback: exchange the code, read the claims.
app.get(${q(callbackPath(redirectUri))}, async (req, res) => {
  const { code_verifier, state } = req.session;
  const tokens = await client.authorizationCodeGrant(
    config,
    new URL(req.url, ${q(trimSlash(redirectUri))}),
    { pkceCodeVerifier: code_verifier, expectedState: state },
  );
  const claims = tokens.claims(); // sub, acr, amr, auth_time, ...
  const userinfo = await client.fetchUserInfo(config, tokens.access_token, claims.sub);
  res.json({ claims, userinfo });
});

app.listen(3000, () => console.log("Sign in with WhatsApp: http://localhost:3000/login"));
`;
}

/** A runnable Flask app using Authlib's standard OIDC client. Confidential
 * clients read the secret from an env var; public clients send no secret and
 * rely on PKCE (Authlib generates the verifier/challenge). */
export function pythonQuickstart(input: QuickstartInput): string {
  const { issuer, clientId, clientType, redirectUri, scopes } = input;
  const isConfidential = clientType === "confidential";
  const secretLine = isConfidential
    ? `    client_secret=os.environ["WA_CLIENT_SECRET"],  # shown once on create / rotate\n`
    : "";
  const authMethodLine = isConfidential
    ? ""
    : `        "token_endpoint_auth_method": "none",  # public client: PKCE only, no secret\n`;

  return `# pip install flask authlib requests
import os
from flask import Flask, jsonify
from authlib.integrations.flask_client import OAuth

app = Flask(__name__)
app.secret_key = os.environ["FLASK_SECRET_KEY"]  # signs the session cookie

# 1. Register the provider from its discovery document.
oauth = OAuth(app)
oauth.register(
    name="whatsapp",
    server_metadata_url=${q(discoveryUrl(issuer))},
    client_id=${q(clientId)},
${secretLine}    client_kwargs={
        "scope": ${q(scopes.join(" "))},
        "code_challenge_method": "S256",  # PKCE (required)
${authMethodLine}    },
)

# 2. Kick off login: redirects to the WhatsApp sign-in page.
@app.route("/login")
def login():
    return oauth.whatsapp.authorize_redirect(${q(redirectUri)})

# 3. Handle the callback: exchange the code, read the claims.
@app.route(${q(callbackPath(redirectUri))})
def callback():
    token = oauth.whatsapp.authorize_access_token()
    return jsonify(token["userinfo"])  # sub, name, phone_number, ...
`;
}

/** A curl call that fetches the discovery document (endpoints + capabilities). */
export function discoveryCurl(issuer: string): string {
  return `curl ${discoveryUrl(issuer)}`;
}

function q(s: string): string {
  return JSON.stringify(s);
}

/** The path part of the redirect URI, used as the callback route. */
function callbackPath(redirectUri: string): string {
  try {
    return new URL(redirectUri).pathname || "/callback";
  } catch {
    return "/callback";
  }
}

function trimSlash(s: string): string {
  // Best-effort origin for the callback URL parse; the path is taken from req.url.
  try {
    const u = new URL(s);
    return u.origin;
  } catch {
    return s.replace(/\/+$/, "");
  }
}
