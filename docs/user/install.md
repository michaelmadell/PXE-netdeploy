# Installing

netdeploy runs as a service on a Windows or Linux host that's on the network
your client machines boot from (the **deployment network**). It needs the
network ports it serves free: DHCP (67, and 4011 in proxy mode), TFTP (69),
HTTP (80) and HTTPS (443) by default, plus the admin UI's port (8443).

## Windows

Windows 10/11 or Windows Server 2019 and later, x64.

1. Download `netdeploy-<version>-setup.exe` from the
   [Releases page](https://github.com/michaelmadell/PXE-netdeploy/releases)
   and run it. It asks for elevation; SmartScreen warns because the
   installer isn't code-signed yet.
2. The installer asks for:
   - the **data folder** (default `C:\ProgramData\netdeploy`): the database,
     imported images, keys and logs live here; make sure it has room for your
     ISOs;
   - where the **admin UI** listens: this computer only (the default), or all
     networks, and its port (8443);
   - whether to add a **Windows Firewall rule** for the deployment services.
3. The last page shows a **setup code** and opens `https://localhost:8443`.
   The browser warns about the certificate: it's the server's own
   self-signed one (you can replace it later in Settings → Admin).
4. Enter the setup code and create the first admin account.

The service is `netdeploy` (Automatic start, runs as LocalSystem so it can
use DISM). The log is `C:\ProgramData\netdeploy\logs\netdeployd.log`.

## Ubuntu

Ubuntu 24.04, amd64 or arm64.

```bash
sudo apt install ./netdeploy_<version>_amd64.deb
```

The package asks where the admin UI listens and prints the setup code when
it starts the service. It runs as a `netdeploy` system account with only the
two capabilities it needs (binding low ports and raw sockets for the DHCP
ping check). Data lives in `/var/lib/netdeploy`; the log is
`journalctl -u netdeploy`.

```bash
systemctl status netdeploy
sudo dpkg-reconfigure netdeploy     # change where the admin UI listens
```

Extra options (a content store on another disk, for example) go in
`/etc/default/netdeploy`, which explains them.

## First sign-in

Open the admin UI, enter the setup code (also in the log, and in
`setup-code.txt` in the data folder) and create your account. Lost the
password later? Stop the service and run
`netdeployd admin reset-password -data <data folder> -user <name>`
on the host.

The **Overview** page lists what's still needed before a machine can be
deployed. Next: [Setting up](setup.md).

## Upgrading

Install the newer version over the old one (run the new installer, or
`sudo apt install` the new package). The service restarts, your settings and
data are kept, and the database is copied to
`netdeploy.db.bak-v<N>-<time>` in the data folder before any change to it.
Reload the admin UI afterwards.

## Removing

- **Windows:** Settings → Apps → netdeploy → Uninstall. It removes the
  service and firewall rule, then asks whether to remove the data; if you say
  yes, the data folder is moved aside to `netdeploy.removed-<time>` rather
  than deleted.
- **Ubuntu:** `sudo apt remove netdeploy` keeps the data;
  `sudo apt purge netdeploy` moves it to `/var/lib/netdeploy.purged-<time>`.

The secrets key isn't in backups. Export it once on the host and keep the
file somewhere safe, or encrypted secrets can't be restored on another
server:

```
netdeployd key export -data <data folder> -out <file>
```
