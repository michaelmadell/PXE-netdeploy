import { useMutation } from "@tanstack/react-query";
import { LogIn, ShieldCheck } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, FieldErrors, Input } from "@/components/ui/form";
import { Alert } from "@/components/ui/misc";
import { ApiError, errorMessage, unwrap } from "@/lib/problem";
import { sessionChanged } from "@/lib/session";

const MIN_PASSWORD = 8;

function message(e: unknown): string {
  if (!(e instanceof ApiError)) return errorMessage(e);
  switch (e.code) {
    case "bad_credentials":
      return "Wrong username or password.";
    case "too_many_attempts":
      return `Too many failed attempts. ${e.message}`;
    case "bad_setup_code":
      return "That isn’t the setup code. It is in the server’s log and in setup-code.txt in its data directory.";
    case "already_set_up":
      return "An account already exists. Reload the page and sign in.";
    case "bad_origin":
      return "The server refused a request from this address. Open the web UI at the server’s own address.";
  }
  return e.message;
}

function Card({ title, description, children }: { title: string; description: ReactNode; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-muted/40 px-4 py-10">
      <div className="w-full max-w-sm rounded-xl border bg-card p-7 shadow-sm">
        <p className="text-sm font-semibold tracking-tight text-muted-foreground">netdeploy</p>
        <h1 className="mt-1 text-xl font-semibold tracking-tight">{title}</h1>
        <p className="mt-1.5 text-sm text-muted-foreground">{description}</p>
        <div className="mt-6">{children}</div>
      </div>
    </main>
  );
}

/** Sign in with a local account; the page the person was on shows afterwards. */
export function SignInPage() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const signIn = useMutation({
    mutationFn: async () => unwrap(await api.POST("/auth/sign-in", { body: { username: username.trim(), password } })),
    onSuccess: sessionChanged,
  });
  return (
    <Card title="Sign in" description="Use your netdeploy account.">
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (username.trim() && password) signIn.mutate();
        }}
      >
        <Field label="Username">
          {(p) => <Input {...p} autoComplete="username" autoFocus value={username} onChange={(e) => setUsername(e.target.value)} />}
        </Field>
        <Field label="Password">
          {(p) => <Input {...p} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
        </Field>
        {signIn.isError && <Alert tone="error">{message(signIn.error)}</Alert>}
        <Button type="submit" variant="primary" className="w-full" disabled={signIn.isPending || !username.trim() || !password}>
          <LogIn aria-hidden /> {signIn.isPending ? "Signing in…" : "Sign in"}
        </Button>
        <p className="text-xs text-muted-foreground">
          Locked out? Stop the server and run <code className="font-mono">netdeployd admin reset-password -user &lt;name&gt;</code>.
        </p>
      </form>
    </Card>
  );
}

/** The first account, with the setup code the server wrote to its log. */
export function SetupPage() {
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const short = password.length > 0 && password.length < MIN_PASSWORD;
  const mismatch = confirm.length > 0 && confirm !== password;
  const valid = code.trim() && username.trim() && password.length >= MIN_PASSWORD && confirm === password;
  const setup = useMutation({
    mutationFn: async () =>
      unwrap(await api.POST("/auth/setup", { body: { setupCode: code.trim(), username: username.trim(), password } })),
    onSuccess: sessionChanged,
  });
  return (
    <Card
      title="Create the first admin account"
      description="This server has no accounts yet. The setup code is in its log and in setup-code.txt in its data directory."
    >
      <FieldErrors value={setup.error instanceof ApiError ? setup.error.fields : []}>
        <form
          className="space-y-4"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) setup.mutate();
          }}
        >
          <Field label="Setup code" path="setupCode" hint="Ten characters, e.g. 7KQ2M-X9PRT.">
            {(p) => <Input {...p} autoComplete="off" autoFocus className="font-mono uppercase" value={code} onChange={(e) => setCode(e.target.value)} />}
          </Field>
          <Field label="Username" path="username" hint="Letters, digits, dots, dashes and underscores.">
            {(p) => <Input {...p} autoComplete="username" maxLength={64} value={username} onChange={(e) => setUsername(e.target.value)} />}
          </Field>
          <Field label="Password" path="password" hint={short ? undefined : `At least ${MIN_PASSWORD} characters.`}>
            {(p) => (
              <Input {...p} type="password" autoComplete="new-password" aria-invalid={short || undefined} value={password} onChange={(e) => setPassword(e.target.value)} />
            )}
          </Field>
          {short && <p className="-mt-2 text-sm text-destructive">At least {MIN_PASSWORD} characters.</p>}
          <Field label="Password again">
            {(p) => (
              <Input {...p} type="password" autoComplete="new-password" aria-invalid={mismatch || undefined} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            )}
          </Field>
          {mismatch && <p className="-mt-2 text-sm text-destructive">The passwords don’t match.</p>}
          {setup.isError && <Alert tone="error">{message(setup.error)}</Alert>}
          <Button type="submit" variant="primary" className="w-full" disabled={setup.isPending || !valid}>
            <ShieldCheck aria-hidden /> {setup.isPending ? "Creating…" : "Create account and sign in"}
          </Button>
        </form>
      </FieldErrors>
    </Card>
  );
}
