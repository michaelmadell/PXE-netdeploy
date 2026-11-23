package autoinstall

import "github.com/michaelmadell/pxe-netdeploy/internal/unattend"

// Builtin is the shipped autoinstall template (ADR 0035).
func Builtin() unattend.TemplateSpec {
	return unattend.TemplateSpec{
		ID: BuiltinID, Name: "Built-in (Ubuntu autoinstall)", BuiltIn: true, Format: Format, Content: builtinContent,
		Description:   "The autoinstall user-data netdeploy ships: locale, keyboard, time zone, the account, SSH keys, the disk layout, extra packages and the boot order. Copy it to customise.",
		IdentityModes: []string{},
	}
}

// builtinContent: every {{ }} writes a YAML value (strings quoted). The
// late-commands are block scalars, so the shell in them isn't YAML.
const builtinContent = `#cloud-config
# netdeploy's built-in autoinstall template (ADR 0035).
autoinstall:
  version: 1
  # Progress for netdeploy (ADR 0036): every installer event, and hooks for
  # the start, the end and a failure.
  reporting:
    netdeploy:
      type: webhook
      endpoint: {{ .Report.Webhook }}
  early-commands:
    - curl -fsS -m 10 -X POST {{ .Report.Started }} || true
  error-commands:
    - curl -fsS -m 10 -X POST {{ .Report.Failed }} || true
  locale: {{ .Locale }}
  keyboard:
    layout: {{ .KeyboardLayout }}
  timezone: {{ .Timezone }}
  identity:
    hostname: {{ .Hostname }}
    username: {{ .Account.Username }}
    realname: {{ .Account.RealName }}
{{- if .Account.PasswordHash }}
    password: {{ .Account.PasswordHash }}
{{- else }}
    # No password: the account is locked for password sign-in (SSH keys only).
    password: "!"
{{- end }}
  ssh:
    install-server: {{ if .Account.SSHAuthorizedKeys }}true{{ else }}false{{ end }}
    authorized-keys: {{ .Account.SSHAuthorizedKeys }}
    allow-pw: {{ .Account.SSHPasswordAuth }}
  storage:
    layout:
      name: {{ .StorageLayout }}
      match:
        size: largest
  packages: {{ .Packages }}
  late-commands:
{{- if not .Account.PasswordHash }}
    # Without a password, sudo can't ask for one.
    - |
      echo {{ raw (shquote (printf "%s ALL=(ALL) NOPASSWD:ALL" .Account.Username)) }} > /target/etc/sudoers.d/90-netdeploy
      chmod 0440 /target/etc/sudoers.d/90-netdeploy
{{- end }}
{{- if eq .BootOrderAfterDeploy "disk-first" }}
    # Boot the installed system first from now on (curtin leaves the
    # network first); re-deploy from the firmware's boot menu or by arming.
    - |
      curtin in-target -- sh -c 'n=$(efibootmgr | sed -n "s/^Boot\([0-9A-Fa-f]\{4\}\)\*\{0,1\} ubuntu.*/\1/p" | head -n 1); [ -n "$n" ] || exit 0; o=$(efibootmgr | sed -n "s/^BootOrder: //p" | tr , "\n" | grep -vix "$n" | paste -sd, -); efibootmgr -o "$n${o:+,$o}"'
{{- end }}
    # Last: tells netdeploy the install succeeded.
    - curl -fsS -m 10 -X POST {{ .Report.Finished }} || true
`
