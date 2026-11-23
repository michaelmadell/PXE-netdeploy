# Troubleshooting

## Where to look

- **Server log:** `C:\ProgramData\netdeploy\logs\netdeployd.log` (Windows) or
  `journalctl -u netdeploy` (Linux). Every DHCP exchange (`dhcp rx`/`dhcp
  tx`, with the reason when a client gets no answer), boot file and content
  request, and deployment change is there.
- **A deployment's log:** its page in Deployments (Download for the whole
  thing), from the Windows agent or the Ubuntu installer.
- **Overview** and each profile's **Check**: what's missing or wrong before
  a deployment starts.

## The machine doesn't get a boot answer

- Look for the client's MAC in the server log. `dhcp no reply` says why:
  its client type is turned off (Settings → Network → Client types), its MAC
  isn't in the interface's prefixes, or the profile can't be served (a
  Linux profile to a PXE client, a boot image that was never built).
- No `dhcp rx` lines at all: the request isn't reaching the server. Check
  the interface, the VLAN, the Windows Firewall rule, and that another
  DHCP server isn't answering first (proxy mode is for networks that have
  one).
- `client selected server <address>`: another DHCP server answered; in full
  mode there should be only one.

## It boots, but the wrong thing (or nothing armed) happens

- A machine with nothing armed and no key pressed starts from its disk:
  that's the re-image protection. Arm a deployment, or use the boot prompt.
- An armed deployment only runs on the client it was armed for, by MAC; a
  machine with several NICs is identified by the one it boots from.

## Secure Boot refuses to start something

- Windows: the boot image's boot manager must be signed by a CA the machine
  trusts. A boot image can use the 2011- or the 2023-signed boot manager from
  the ISO; machines that have had the 2011 CA revoked need the 2023 one.
- Ubuntu: "Verification failed" printed once after the kernel loads is
  normal (iPXE checks the kernel against the firmware first, then hands it to
  Ubuntu's shim). A real refusal stops there; Ubuntu's shim needs the 2011
  UEFI CA in the machine's database.

## An Ubuntu install stops

- "Unable to find a live file system on the network": the installer couldn't
  download the ISO. The interface must serve plain **HTTP**.
- The installer asks questions in an automatic install: the template is
  missing a section; run the profile's **Check** and the template preview.
- The deployment stays at a step and later fails with "no word from the
  machine": the installer stopped reporting. Look at the machine's screen
  and `/var/log/installer`.

## The admin UI shows errors after an upgrade

Reload the page (Ctrl+Shift+R) so the browser picks up the new version.

## Reporting a bug

See [CONTRIBUTING.md](../../CONTRIBUTING.md): the version, host, client and
the server log from the time it happened.
