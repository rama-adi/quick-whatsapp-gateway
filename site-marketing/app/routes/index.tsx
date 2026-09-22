import { createFileRoute } from '@tanstack/react-router';
import { ArrowUpRight, Cable, MessageSquare, Radio } from 'lucide-react';

export const Route = createFileRoute('/')({ component: Landing });

function Landing() {
  return (
    <div className="marketing-shell">
      <header className="marketing-nav">
        <a className="brand" href="/" aria-label="WhatsApp Gateway home"><span className="brand-mark"><MessageSquare size={18} /></span>WhatsApp Gateway</a>
        <a href="/docs">Documentation <ArrowUpRight size={15} /></a>
      </header>
      <main className="landing">
        <section className="hero">
          <p className="eyebrow"><span /> SELF-HOSTED MESSAGING INFRASTRUCTURE</p>
          <h1>Your applications.<br />Your infrastructure.<br /><span>Connected to WhatsApp.</span></h1>
          <p className="hero-copy">Bring WhatsApp messaging into your workflows with a REST API, live events, and a web dashboard. Pair your devices and run the gateway on infrastructure you control.</p>
          <a className="primary-link" href="/docs">Explore the documentation <ArrowUpRight size={18} /></a>
        </section>
        <section className="features" aria-label="Gateway capabilities">
          <article><Cable size={22} /><h2>A clear API</h2><p>Send messages and media, manage sessions, and work with contacts and groups through documented endpoints.</p></article>
          <article><Radio size={22} /><h2>Events as they happen</h2><p>Connect incoming messages and session changes to your application using webhooks and a live event stream.</p></article>
          <article><MessageSquare size={22} /><h2>Built to operate yourself</h2><p>Understand the architecture, configure your deployment, and manage your installation with complete operator guides.</p></article>
        </section>
        <aside className="project-note">Powered by whatsmeow. See protocol compatibility and account requirements in the documentation.</aside>
      </main>
      <footer className="marketing-footer"><span>WhatsApp Gateway</span><a href="/docs">Guides, API reference &amp; architecture <ArrowUpRight size={14} /></a></footer>
    </div>
  );
}
