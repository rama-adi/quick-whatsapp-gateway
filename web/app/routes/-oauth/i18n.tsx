// Client-side locale for the end-user consent surface (English / Indonesian).
// Deliberately NOT persisted server-side (oauth.md §6.1 keeps this page
// stateless): the choice lives in localStorage and defaults from
// `navigator.language`. The dashboard preview mounts its own non-persisting
// provider so a developer can flip languages without touching the end-user
// default on their own browser.

import * as React from "react";
import { GlobeIcon } from "lucide-react";
import { cn } from "~/lib/utils";

export type ConsentLocale = "en" | "id";

const STORAGE_KEY = "wa-consent-locale";

/** Every end-user-visible string on the consent surface, per locale. Functions
 * take the highlighted fragments (already styled by the caller) so word order
 * can differ between languages. */
export interface ConsentMessages {
  languageName: string;
  secureSignIn: string;
  footerNote: string;
  loading: string;
  continueTo: (app: React.ReactNode) => React.ReactNode;
  withDm: string;
  withGroup: string;
  willReceive: (app: React.ReactNode) => React.ReactNode;
  privacyTitle: string;
  privacyBody: string;
  confirmTitle: string;
  confirmSubtitle: string;
  sendTo: (number: React.ReactNode, bot: React.ReactNode | null) => React.ReactNode;
  groupMention: (group: React.ReactNode, bot: React.ReactNode) => React.ReactNode;
  groupMentionFallback: (group: React.ReactNode) => React.ReactNode;
  copyHintMention: string;
  copyHintDm: string;
  copy: string;
  copied: string;
  copiedLive: string;
  openWhatsApp: string;
  qrCaption: string;
  groupNote: string;
  codeExpired: string;
  expiresIn: (time: React.ReactNode) => React.ReactNode;
  reconnecting: string;
  cancelSignIn: string;
  cancelling: string;
  numberFallback: string;
  states: {
    verified: { title: string; message: string };
    finalizing: { title: string; message: string };
    denied: { title: string; message: string };
    expired: { title: string; message: string };
    notFound: { title: string; message: string };
    error: { title: string; message: string };
  };
}

export const CONSENT_MESSAGES: Record<ConsentLocale, ConsentMessages> = {
  en: {
    languageName: "English",
    secureSignIn: "Secure sign-in",
    footerNote:
      "Your sign-in code is short-lived and is only confirmed after you send it from WhatsApp.",
    loading: "Loading",
    continueTo: (app) => <>Continue to {app}</>,
    withDm: "with your WhatsApp number",
    withGroup: "with your WhatsApp group membership",
    willReceive: (app) => <>What {app} will receive</>,
    privacyTitle: "Your WhatsApp account stays private",
    privacyBody:
      "We won't receive your WhatsApp login information, password, or verification codes. Only continue if you started this sign-in yourself, and never send this code anywhere else.",
    confirmTitle: "Confirm in WhatsApp",
    confirmSubtitle: "Send one message to prove it is you.",
    sendTo: (number, bot) => (
      <>
        Send this message to {number}
        {bot ? <> ({bot})</> : null} on WhatsApp:
      </>
    ),
    groupMention: (group, bot) => (
      <>
        In {group}, type <code className="font-mono">@</code> and pick {bot}{" "}
        from the suggestions, then send:
      </>
    ),
    groupMentionFallback: (group) => (
      <>In the group {group}, mention the bot with this message:</>
    ),
    copyHintMention:
      "The @mention can't be copied — pick the bot in WhatsApp, then add:",
    copyHintDm: "Copy and send it exactly as shown.",
    copy: "Copy",
    copied: "Copied",
    copiedLive: "Copied to clipboard",
    openWhatsApp: "Open WhatsApp",
    qrCaption: "Scan with your phone to open the prepared message",
    groupNote:
      "You must already be a member of the group. Sending this from inside the group proves your membership.",
    codeExpired: "Code expired",
    expiresIn: (time) => <>Expires in {time}</>,
    reconnecting: "Reconnecting",
    cancelSignIn: "Cancel sign-in",
    cancelling: "Cancelling…",
    numberFallback: "the app's number",
    states: {
      verified: { title: "Signed in", message: "Taking you back to the app…" },
      finalizing: { title: "Verified", message: "Finishing sign-in…" },
      denied: {
        title: "Sign-in cancelled",
        message:
          "This sign-in was cancelled. You can safely close this tab. If you were trying to sign in, start again from the app.",
      },
      expired: {
        title: "Sign-in expired",
        message:
          "This code timed out. Return to the app and start signing in again to get a new one.",
      },
      notFound: {
        title: "Invalid or expired link",
        message:
          "This sign-in link is no longer valid. Go back to the app and start again.",
      },
      error: {
        title: "Something went wrong",
        message:
          "We couldn't finish signing you in. Return to the app and try again.",
      },
    },
  },
  id: {
    languageName: "Bahasa Indonesia",
    secureSignIn: "Masuk aman",
    footerNote:
      "Kode masuk Anda berlaku singkat dan hanya dikonfirmasi setelah Anda mengirimnya dari WhatsApp.",
    loading: "Memuat",
    continueTo: (app) => <>Lanjutkan ke {app}</>,
    withDm: "dengan nomor WhatsApp Anda",
    withGroup: "dengan keanggotaan grup WhatsApp Anda",
    willReceive: (app) => <>Yang akan diterima {app}</>,
    privacyTitle: "Akun WhatsApp Anda tetap privat",
    privacyBody:
      "Kami tidak akan menerima info login, kata sandi, atau kode verifikasi WhatsApp Anda. Lanjutkan hanya jika Anda sendiri yang memulai proses masuk ini, dan jangan pernah kirim kode ini ke pihak lain.",
    confirmTitle: "Konfirmasi di WhatsApp",
    confirmSubtitle: "Kirim satu pesan untuk membuktikan bahwa ini Anda.",
    sendTo: (number, bot) => (
      <>
        Kirim pesan ini ke {number}
        {bot ? <> ({bot})</> : null} di WhatsApp:
      </>
    ),
    groupMention: (group, bot) => (
      <>
        Di {group}, ketik <code className="font-mono">@</code> lalu pilih {bot}{" "}
        dari saran yang muncul, kemudian kirim:
      </>
    ),
    groupMentionFallback: (group) => (
      <>Di grup {group}, sebut (mention) bot dengan pesan ini:</>
    ),
    copyHintMention:
      "Mention @ tidak bisa disalin — pilih bot di WhatsApp, lalu tambahkan:",
    copyHintDm: "Salin dan kirim persis seperti yang ditampilkan.",
    copy: "Salin",
    copied: "Tersalin",
    copiedLive: "Tersalin ke papan klip",
    openWhatsApp: "Buka WhatsApp",
    qrCaption: "Pindai dengan ponsel Anda untuk membuka pesan yang sudah disiapkan",
    groupNote:
      "Anda harus sudah menjadi anggota grup. Mengirim pesan ini dari dalam grup membuktikan keanggotaan Anda.",
    codeExpired: "Kode kedaluwarsa",
    expiresIn: (time) => <>Berakhir dalam {time}</>,
    reconnecting: "Menyambung ulang",
    cancelSignIn: "Batalkan masuk",
    cancelling: "Membatalkan…",
    numberFallback: "nomor aplikasi ini",
    states: {
      verified: {
        title: "Berhasil masuk",
        message: "Mengembalikan Anda ke aplikasi…",
      },
      finalizing: { title: "Terverifikasi", message: "Menyelesaikan proses masuk…" },
      denied: {
        title: "Proses masuk dibatalkan",
        message:
          "Proses masuk ini dibatalkan. Anda dapat menutup tab ini dengan aman. Jika Anda tadi ingin masuk, mulai lagi dari aplikasinya.",
      },
      expired: {
        title: "Proses masuk kedaluwarsa",
        message:
          "Kode ini kehabisan waktu. Kembali ke aplikasi dan mulai proses masuk lagi untuk mendapatkan kode baru.",
      },
      notFound: {
        title: "Tautan tidak valid atau kedaluwarsa",
        message:
          "Tautan masuk ini sudah tidak berlaku. Kembali ke aplikasi dan mulai lagi.",
      },
      error: {
        title: "Terjadi kesalahan",
        message:
          "Kami tidak dapat menyelesaikan proses masuk Anda. Kembali ke aplikasi dan coba lagi.",
      },
    },
  },
};

interface ConsentI18n {
  locale: ConsentLocale;
  setLocale: (locale: ConsentLocale) => void;
  m: ConsentMessages;
}

const ConsentI18nContext = React.createContext<ConsentI18n | null>(null);

export function ConsentI18nProvider({
  children,
  defaultLocale = "en",
  persist = true,
}: {
  children: React.ReactNode;
  defaultLocale?: ConsentLocale;
  /** Store the choice in localStorage and seed from the browser language.
   * The dashboard preview turns this off. */
  persist?: boolean;
}) {
  // Start deterministic ("en" unless told otherwise) and sync from the browser
  // after mount, so SSR markup never mismatches hydration.
  const [locale, setLocaleState] = React.useState<ConsentLocale>(defaultLocale);

  React.useEffect(() => {
    if (!persist) return;
    let stored: string | null = null;
    try {
      stored = window.localStorage.getItem(STORAGE_KEY);
    } catch {
      // Storage can be unavailable (private mode); the browser language still applies.
    }
    if (stored === "en" || stored === "id") setLocaleState(stored);
    else if (navigator.language?.toLowerCase().startsWith("id"))
      setLocaleState("id");
  }, [persist]);

  const setLocale = React.useCallback(
    (next: ConsentLocale) => {
      setLocaleState(next);
      if (!persist) return;
      try {
        window.localStorage.setItem(STORAGE_KEY, next);
      } catch {
        // Best effort only.
      }
    },
    [persist],
  );

  const value = React.useMemo(
    () => ({ locale, setLocale, m: CONSENT_MESSAGES[locale] }),
    [locale, setLocale],
  );

  return (
    <ConsentI18nContext.Provider value={value}>
      {children}
    </ConsentI18nContext.Provider>
  );
}

/** Falls back to English outside a provider so shared components stay usable. */
export function useConsentI18n(): ConsentI18n {
  const ctx = React.useContext(ConsentI18nContext);
  const fallback = React.useMemo<ConsentI18n>(
    () => ({ locale: "en", setLocale: () => {}, m: CONSENT_MESSAGES.en }),
    [],
  );
  return ctx ?? fallback;
}

/** Compact EN | ID segmented toggle. Labels are the locales' own names, so it
 * stays legible whichever language is active. */
export function LanguageToggle({ className }: { className?: string }) {
  const { locale, setLocale } = useConsentI18n();
  return (
    <div
      role="group"
      aria-label="Language / Bahasa"
      className={cn(
        "inline-flex items-center gap-0.5 rounded-full border bg-background/80 p-0.5 shadow-xs backdrop-blur",
        className,
      )}
    >
      <GlobeIcon
        className="ml-1.5 size-3.5 shrink-0 text-muted-foreground"
        aria-hidden
      />
      {(["en", "id"] as const).map((l) => (
        <button
          key={l}
          type="button"
          onClick={() => setLocale(l)}
          aria-pressed={locale === l}
          aria-label={CONSENT_MESSAGES[l].languageName}
          className={cn(
            "rounded-full px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide transition-colors",
            locale === l
              ? "bg-emerald-600 text-white shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {l}
        </button>
      ))}
    </div>
  );
}
