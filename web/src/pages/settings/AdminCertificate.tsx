import { FileUp, RotateCcw, ShieldCheck } from "lucide-react";
import { useRef, useState } from "react";
import { type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Textarea } from "@/components/ui/form";
import { Alert, Badge } from "@/components/ui/misc";
import { formatDateTime } from "@/lib/format";

type Admin = Schemas["Settings"]["admin"];
type Info = Schemas["CertificateInfo"];

const DAY = 24 * 60 * 60 * 1000;

/** "ab12cd…" → "AB:12:CD:…", the way browsers show fingerprints. */
const colonHex = (h: string) => h.toUpperCase().match(/.{2}/g)?.join(":") ?? h;

/** Split a file that may hold a certificate chain, a key, or both. */
function pemBlocks(text: string) {
  const blocks = text.match(/-----BEGIN ([A-Z ]+)-----[\s\S]*?-----END \1-----/g) ?? [];
  const certs = blocks.filter((b) => b.startsWith("-----BEGIN CERTIFICATE-----"));
  const keys = blocks.filter((b) => /^-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(b));
  return { cert: certs.length ? certs.join("\n") + "\n" : "", key: keys.length ? keys[0] + "\n" : "" };
}

function covers(info: Info, address: string): boolean {
  if (address === "0.0.0.0") return true; // reached by any address; can't tell which
  return info.ipAddresses.includes(address) || (address === "127.0.0.1" && info.dnsNames.includes("localhost"));
}

/**
 * The admin listener's certificate: what is served now, and replacing it
 * (pasted or from files) or going back to the generated one. Edits go into
 * the settings draft; nothing changes until Save.
 */
export function AdminCertificate({
  draft,
  base,
  edit,
}: {
  draft: Admin;
  base: Admin;
  edit: (fn: (a: Admin) => void) => void;
}) {
  const info = base.certificateInfo;
  const [editing, setEditing] = useState(false);
  const [readError, setReadError] = useState<string | null>(null);
  // What the certificate box shows: kept here, because the draft can't tell
  // "nothing entered" from "the same certificate as now" (both equal the base).
  const [certText, setCertText] = useState("");
  const certFile = useRef<HTMLInputElement>(null);
  const keyFile = useRef<HTMLInputElement>(null);

  const basePem = base.certificatePem ?? null;
  const reverting = basePem !== null && draft.certificatePem === null;
  const replacing = !!draft.privateKeyPem || (draft.certificatePem ?? null) !== basePem;
  // An empty certificate box means "no change", never "remove": null would go back to the generated one.
  const setCert = (text: string) => {
    setCertText(text);
    edit((a) => void (a.certificatePem = text.trim() ? text : basePem));
  };
  const sameAsNow = !!certText.trim() && !!basePem && certText.trim() === basePem.trim();
  const setKey = (text: string) =>
    edit((a) => {
      if (text.trim()) a.privateKeyPem = text;
      else delete a.privateKeyPem;
    });
  const cancel = () => {
    setEditing(false);
    setReadError(null);
    setCertText("");
    edit((a) => {
      a.certificatePem = basePem;
      delete a.privateKeyPem;
    });
  };
  const load = async (file: File | undefined, want: "cert" | "key") => {
    setReadError(null);
    if (!file) return;
    if (file.size > 256 * 1024) return setReadError(`${file.name} is too big for a PEM file.`);
    const { cert, key } = pemBlocks(await file.text());
    if (!cert && !key) return setReadError(`${file.name} has no PEM certificate or private key. Convert a .pfx or DER file to PEM first.`);
    // A combined file fills both boxes, whichever button was used.
    if (cert) setCert(cert);
    if (key) setKey(key);
    if (want === "cert" && !cert) setReadError(`${file.name} has a private key but no certificate.`);
    if (want === "key" && !key) setReadError(`${file.name} has a certificate but no private key.`);
  };

  const left = info ? new Date(info.notAfter).getTime() - Date.now() : 0;
  const names = info ? [...info.dnsNames, ...info.ipAddresses] : [];

  return (
    <div className="mt-6 border-t pt-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Certificate</h3>
          <p className="mt-1 max-w-prose text-sm text-muted-foreground">
            What browsers see when they open this page. The generated one is self-signed, so browsers warn until it is trusted; use your
            own from your organisation’s certificate authority to avoid that.
          </p>
        </div>
        {!editing && !reverting && (
          <div className="flex gap-2">
            {info?.source === "custom" && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  edit((a) => {
                    a.certificatePem = null;
                    delete a.privateKeyPem;
                  })
                }
              >
                <RotateCcw aria-hidden /> Use the generated certificate
              </Button>
            )}
            <Button size="sm" onClick={() => setEditing(true)}>
              <ShieldCheck aria-hidden /> {info?.source === "custom" ? "Replace certificate" : "Use my own certificate"}
            </Button>
          </div>
        )}
      </div>

      {info ? (
        <dl className="mt-4 grid gap-x-6 gap-y-4 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">In use</dt>
            <dd className="mt-0.5 flex flex-wrap items-center gap-2">
              {info.source === "generated" ? "Generated (self-signed)" : "Your own"}
              {left <= 0 ? <Badge tone="error">Expired</Badge> : left < 30 * DAY ? <Badge tone="warning">Expires soon</Badge> : null}
            </dd>
          </div>
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
          <div>
            <dt className="text-muted-foreground">Issued by</dt>
            <dd className="mt-0.5 break-words">{!info.issuer || info.issuer === info.subject ? "Itself (self-signed)" : info.issuer}</dd>
          </div>
          <div className="sm:col-span-2">
            <dt className="text-muted-foreground">Names it covers</dt>
            <dd className="mt-0.5 font-mono text-xs">{names.length ? names.join(", ") : "None"}</dd>
          </div>
          <div className="sm:col-span-2">
            <dt className="text-muted-foreground">SHA-256 fingerprint</dt>
            <dd className="mt-0.5 font-mono text-xs break-all">{colonHex(info.sha256)}</dd>
          </div>
        </dl>
      ) : (
        <p className="mt-4 text-sm text-muted-foreground">The server didn’t describe its certificate.</p>
      )}

      {info && !editing && !reverting && !covers(info, base.bindAddress) && (
        <Alert tone="warning" className="mt-4" title={`It doesn’t cover ${base.bindAddress}`}>
          Browsers opening https://{base.bindAddress}:{base.port} will warn even once the certificate is trusted. Use a certificate that
          names this address, or open the page by a name it covers.
        </Alert>
      )}

      {reverting && (
        <Alert tone="info" className="mt-4" title="The generated certificate will be used again">
          When you save. Your browser will warn about it until it is trusted.{" "}
          <button type="button" className="font-medium text-primary underline-offset-4 hover:underline" onClick={cancel}>
            Keep your own
          </button>
        </Alert>
      )}

      {editing && (
        <div className="mt-5 space-y-4 rounded-md border p-4">
          <p className="text-sm text-muted-foreground">
            Paste PEM text or choose files. A file holding both the certificate and its key fills both boxes. Put any intermediate
            certificates after the server certificate.
          </p>
          <Field
            label="Certificate (PEM)"
            path="admin.certificatePem"
            hint={sameAsNow ? "The same certificate as the one in use: saving changes only the key." : undefined}
          >
            {(p) => (
              <Textarea
                {...p}
                rows={6}
                spellCheck={false}
                className="font-mono text-xs"
                placeholder="-----BEGIN CERTIFICATE-----"
                value={certText}
                onChange={(e) => setCert(e.target.value)}
              />
            )}
          </Field>
          <Field label="Private key (PEM)" path="admin.privateKeyPem" hint="Sent once and stored on the server; never shown again.">
            {(p) => (
              <Textarea
                {...p}
                rows={5}
                spellCheck={false}
                autoComplete="off"
                className="font-mono text-xs"
                placeholder="-----BEGIN PRIVATE KEY-----"
                value={draft.privateKeyPem ?? ""}
                onChange={(e) => setKey(e.target.value)}
              />
            )}
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={() => certFile.current?.click()}>
              <FileUp aria-hidden /> Choose certificate file…
            </Button>
            <Button size="sm" onClick={() => keyFile.current?.click()}>
              <FileUp aria-hidden /> Choose key file…
            </Button>
            <span className="flex-1" />
            <Button size="sm" variant="ghost" onClick={cancel}>
              Cancel
            </Button>
            <input
              ref={certFile}
              type="file"
              accept=".pem,.crt,.cer"
              className="hidden"
              onChange={(e) => {
                void load(e.target.files?.[0], "cert");
                e.target.value = "";
              }}
            />
            <input
              ref={keyFile}
              type="file"
              accept=".pem,.key"
              className="hidden"
              onChange={(e) => {
                void load(e.target.files?.[0], "key");
                e.target.value = "";
              }}
            />
          </div>
          {readError && <Alert tone="error">{readError}</Alert>}
          {replacing && (
            <Alert tone="info">
              When you save, the server checks that the certificate and key match and switches to it at once. Your browser may then
              ask you to accept the new certificate; reload the page.
            </Alert>
          )}
        </div>
      )}
    </div>
  );
}
