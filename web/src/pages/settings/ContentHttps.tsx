import { Download } from "lucide-react";
import { useState } from "react";
import { type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Alert, Badge, Section } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";
import { colonHex } from "./AdminCertificate";

type Settings = Schemas["Settings"];
type Info = Schemas["CertificateInfo"];

const DAY = 24 * 60 * 60 * 1000;

/** Saves the content CA as PEM, or DER for firmware enrolment. */
async function download(der: boolean) {
  const res = await fetch("/api/v1/settings/content-ca", {
    headers: { Accept: der ? "application/pkix-cert" : "application/x-pem-file" },
  });
  if (!res.ok) throw new Error(`The server answered ${res.status}.`);
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = der ? "netdeploy-content-ca.cer" : "netdeploy-content-ca.pem";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function Facts({ info, names }: { info: Info; names?: boolean }) {
  const all = [...info.ipAddresses, ...info.dnsNames];
  return (
    <dl className="mt-3 grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
      <div>
        <dt className="text-muted-foreground">Valid</dt>
        <dd className="mt-0.5">
          {formatDateTime(info.notBefore)} to {formatDateTime(info.notAfter)}
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">Subject</dt>
        <dd className="mt-0.5 break-words">{info.subject || "–"}</dd>
      </div>
      {names && (
        <div className="sm:col-span-2">
          <dt className="text-muted-foreground">Names it covers</dt>
          <dd className="mt-0.5 font-mono text-xs">{all.length ? all.join(", ") : "None"}</dd>
        </div>
      )}
      <div className="sm:col-span-2">
        <dt className="text-muted-foreground">SHA-256 fingerprint</dt>
        <dd className="mt-0.5 font-mono text-xs break-all">{colonHex(info.sha256)}</dd>
      </div>
    </dl>
  );
}

/**
 * Content HTTPS (ADR 0028): the internal CA that boot images with https://
 * server addresses trust, and the certificate the HTTPS listeners serve.
 * Read-only: the CA is made when an interface first turns HTTPS on.
 */
export function ContentHttps({ settings }: { settings: Settings }) {
  const ca = settings.contentHttps?.ca ?? null;
  const cert = settings.contentHttps?.certificate ?? null;
  const [error, setError] = useState<string | null>(null);
  const left = ca ? new Date(ca.notAfter).getTime() - Date.now() : Infinity;

  const save = (der: boolean) => {
    setError(null);
    download(der).catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  };

  return (
    <Section
      title="Content HTTPS"
      description="Deployment interfaces with HTTPS on serve boot content and the agent over TLS, with certificates from this server’s own content CA. Boot images with https:// server addresses carry the CA and trust nothing else."
    >
      {ca ? (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
              Content CA
              {left <= 0 ? <Badge tone="error">Expired</Badge> : left < 365 * DAY ? <Badge tone="warning">Expires within a year</Badge> : null}
            </p>
            <div className="flex gap-2">
              <Button size="sm" variant="ghost" onClick={() => save(false)}>
                <Download aria-hidden /> PEM
              </Button>
              <Button size="sm" variant="ghost" onClick={() => save(true)}>
                <Download aria-hidden /> DER (for firmware)
              </Button>
            </div>
          </div>
          <Facts info={ca} />
          {error && (
            <Alert tone="error" className="mt-3" title="Couldn’t download the CA">
              {error}
            </Alert>
          )}
          <p className="mt-5 text-sm font-medium">Certificate in use</p>
          {cert ? (
            <>
              <p className="mt-1 text-sm text-muted-foreground">
                Issued from the CA and renewed automatically when the server’s addresses change. Changing it never needs a boot image rebuild.
              </p>
              <Facts info={cert} names />
            </>
          ) : (
            <p className="mt-1 text-sm text-muted-foreground">None: no interface has HTTPS on.</p>
          )}
          <p className="mt-5 text-sm text-muted-foreground">
            The CA’s key is protected with the secrets key. Run <code>netdeployd key export</code> once: a backup restored without it gets a
            new CA, and boot images with https:// addresses then need rebuilding.
          </p>
        </>
      ) : (
        <p className="text-sm text-muted-foreground">
          No content CA yet. It is made when you turn HTTPS on for a deployment interface (Network tab) and save.
        </p>
      )}
    </Section>
  );
}
