; netdeploy Windows installer (ADR 0031). Built by scripts/package.ps1:
;   ISCC /DAppVersion=0.6.0 /DFileVersion=0.6.0.3 /DDist=<repo>\dist /DWimlib=<wimlib dir> /DWimlibVersion=1.14.5 netdeploy.iss
; Optional /DSign: signs with the "netdeploy" sign tool configured in ISCC.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef FileVersion
  #define FileVersion "0.0.0.0"
#endif
#ifndef Dist
  #define Dist "..\..\dist"
#endif
#ifndef Wimlib
  #define Wimlib "..\..\.tmp\wimlib"
#endif
#ifndef WimlibVersion
  #define WimlibVersion "1.14.5"
#endif

[Setup]
; Never change AppId: it is how an upgrade finds the installed copy.
AppId={{317DF503-8FD1-45E3-A848-9F0B37955FC1}
AppName=netdeploy
AppVersion={#AppVersion}
AppVerName=netdeploy {#AppVersion}
VersionInfoVersion={#FileVersion}
AppPublisher=netdeploy
AppPublisherURL=https://github.com/michaelmadell/pxe-netdeploy
AppCopyright=Copyright 2026 Michael Madell. Apache License 2.0.
LicenseFile={#Dist}\..\LICENSE
DefaultDirName={autopf}\netdeploy
DisableProgramGroupPage=yes
DisableDirPage=auto
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
PrivilegesRequired=admin
OutputDir={#Dist}
OutputBaseFilename=netdeploy-{#AppVersion}-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\netdeployd.exe
UninstallDisplayName=netdeploy
; The service holds netdeployd.exe open; PrepareToInstall stops it instead.
CloseApplications=no
SetupLogging=yes
#ifdef Sign
SignTool=netdeploy
SignedUninstaller=yes
#endif

[Files]
Source: "{#Dist}\netdeployd-windows-amd64.exe"; DestDir: "{app}"; DestName: "netdeployd.exe"; Flags: ignoreversion
Source: "{#Dist}\..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion
Source: "{#Dist}\..\NOTICE"; DestDir: "{app}"; DestName: "NOTICE.txt"; Flags: ignoreversion
; wimlib, unmodified (GPLv3+ program, LGPLv2.1+ library; see wimlib\SOURCE.txt).
Source: "{#Wimlib}\wimlib-imagex.exe"; DestDir: "{app}\wimlib"; Flags: ignoreversion
Source: "{#Wimlib}\libwim-15.dll"; DestDir: "{app}\wimlib"; Flags: ignoreversion
Source: "{#Wimlib}\COPYING*.txt"; DestDir: "{app}\wimlib"; Flags: ignoreversion
Source: "{#Wimlib}\README.txt"; DestDir: "{app}\wimlib"; DestName: "README-wimlib.txt"; Flags: ignoreversion
Source: "wimlib-SOURCE.txt"; DestDir: "{app}\wimlib"; DestName: "SOURCE.txt"; Flags: ignoreversion
; The signed iPXE chain for HTTP(S) Boot (ADR 0032; scripts/fetch-ipxe.ps1, see ipxe\SOURCE.txt).
Source: "{#Dist}\ipxe\*"; DestDir: "{app}\ipxe"; Flags: ignoreversion recursesubdirs

[Tasks]
Name: "firewall"; Description: "Allow netdeployd through Windows Firewall (DHCP, TFTP, HTTP(S) and the admin web UI)"

[Run]
; Fresh install only: the admin address the wizard asked for (ADR 0031).
Filename: "{app}\netdeployd.exe"; Parameters: "admin set-address -data ""{code:DataDir}"" {code:AdminAddress}"; \
  StatusMsg: "Storing the admin address..."; Flags: runhidden waituntilterminated; Check: IsFreshInstall
; Register, or on upgrade update, the service.
Filename: "{app}\netdeployd.exe"; Parameters: "service install -data ""{code:DataDir}"""; \
  StatusMsg: "Registering the netdeploy service..."; Flags: runhidden waituntilterminated
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""netdeploy"""; \
  Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""netdeploy"" dir=in action=allow program=""{app}\netdeployd.exe"" enable=yes profile=any"; \
  StatusMsg: "Adding the firewall rule..."; Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{app}\netdeployd.exe"; Parameters: "service start"; \
  StatusMsg: "Starting netdeploy..."; Flags: runhidden waituntilterminated
Filename: "{code:AdminURL}"; Description: "Open the netdeploy web UI"; Flags: postinstall shellexec nowait skipifsilent

[UninstallRun]
Filename: "{app}\netdeployd.exe"; Parameters: "service uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "RemoveService"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""netdeploy"""; Flags: runhidden waituntilterminated; RunOnceId: "RemoveFirewall"

[Code]
const
  RegKey = 'Software\netdeploy';

var
  DataPage: TInputDirWizardPage;
  AccessPage: TInputOptionWizardPage;
  PortPage: TInputQueryWizardPage;
  SetupCode: String;
  Fresh: Boolean;

function InstalledDataDir(): String;
begin
  Result := '';
  RegQueryStringValue(HKLM64, RegKey, 'DataDir', Result);
end;

// Decided once at start: the registry is written during installation.
function IsFreshInstall(): Boolean;
begin
  Result := Fresh;
end;

function InitializeSetup(): Boolean;
begin
  Fresh := InstalledDataDir() = '';
  Result := True;
end;

function DataDir(Param: String): String;
begin
  if IsFreshInstall() then
    Result := RemoveBackslashUnlessRoot(DataPage.Values[0])
  else
    Result := InstalledDataDir();
end;

function AdminPort(): String;
var
  P: Integer;
begin
  P := StrToIntDef(Trim(PortPage.Values[0]), 0);
  if (P < 1) or (P > 65535) then
    P := 8443;
  Result := IntToStr(P);
end;

function AdminAddress(Param: String): String;
begin
  if AccessPage.SelectedValueIndex = 1 then
    Result := '0.0.0.0:' + AdminPort()
  else
    Result := '127.0.0.1:' + AdminPort();
end;

function AdminURL(Param: String): String;
var
  Port: String;
begin
  if IsFreshInstall() then
    Port := AdminPort()
  else
  begin
    Port := '8443';
    RegQueryStringValue(HKLM64, RegKey, 'AdminPort', Port);
  end;
  Result := 'https://localhost:' + Port + '/';
end;

procedure InitializeWizard();
begin
  DataPage := CreateInputDirPage(wpSelectDir,
    'Data folder', 'Where should netdeploy keep its data?',
    'The database, keys, imported images and built boot images go here. One Windows ISO needs about 20 GB, ' +
    'so choose a drive with room. Only SYSTEM and Administrators will be able to read it.',
    False, '');
  DataPage.Add('');
  DataPage.Values[0] := ExpandConstant('{commonappdata}\netdeploy');

  AccessPage := CreateInputOptionPage(DataPage.ID,
    'Admin web UI', 'Where should the admin web UI listen?',
    'The web UI and API need a signed-in account; the first one is created with the setup code shown at the end. ' +
    'You can change this later in Settings.',
    True, False);
  AccessPage.Add('This computer only (https://localhost)');
  AccessPage.Add('All networks (other computers can reach the sign-in page)');
  AccessPage.SelectedValueIndex := 0;

  PortPage := CreateInputQueryPage(AccessPage.ID,
    'Admin web UI', 'Which port should the admin web UI use?', '');
  PortPage.Add('HTTPS port:', False);
  PortPage.Values[0] := '8443';
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  // An upgrade keeps the data folder and the stored admin address.
  Result := (not IsFreshInstall()) and
    ((PageID = DataPage.ID) or (PageID = AccessPage.ID) or (PageID = PortPage.ID));
end;

// Stop the running service so its files can be replaced.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Result := '';
  Exec(ExpandConstant('{sys}\net.exe'), 'stop netdeploy', '', SW_HIDE, ewWaitUntilTerminated, Code);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Lines: TArrayOfString;
  I: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    if IsFreshInstall() then
    begin
      RegWriteStringValue(HKLM64, RegKey, 'DataDir', DataDir(''));
      RegWriteStringValue(HKLM64, RegKey, 'AdminPort', AdminPort());
    end;
    // The service writes the setup code on its first start.
    for I := 1 to 30 do
    begin
      if LoadStringsFromFile(AddBackslash(DataDir('')) + 'setup-code.txt', Lines) and (GetArrayLength(Lines) >= 2) then
      begin
        SetupCode := Trim(Lines[1]);
        Break;
      end;
      Sleep(500);
    end;
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpFinished) and (SetupCode <> '') then
    WizardForm.FinishedLabel.Caption :=
      'netdeploy is installed and running.' + #13#10#13#10 +
      'Open ' + AdminURL('') + ' and create the first account with this setup code:' + #13#10#13#10 +
      '    ' + SetupCode + #13#10#13#10 +
      'It is also in ' + AddBackslash(DataDir('')) + 'setup-code.txt. ' +
      'The browser will warn about the certificate: netdeploy made its own.';
end;

// Uninstall keeps the data unless asked; even then it is moved aside, not deleted.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Dir, Kept: String;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    Dir := InstalledDataDir();
    if (Dir <> '') and DirExists(Dir) and (not UninstallSilent()) then
      if MsgBox('Also remove netdeploy''s data (images, boot images, settings, the secrets key)?' + #13#10#13#10 +
                'It will be moved to a "' + ExtractFileName(Dir) + '.removed-<date>" folder next to it, not deleted. ' +
                'Choose No to keep it where it is for a later reinstall.',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
      begin
        Kept := Dir + '.removed-' + GetDateTimeString('yyyymmdd"T"hhnnss', #0, #0);
        if RenameFile(Dir, Kept) then
          MsgBox('The data was moved to ' + Kept + '. Delete that folder to free the space.', mbInformation, MB_OK)
        else
          MsgBox('The data could not be moved; it is still in ' + Dir + '.', mbError, MB_OK);
      end;
    RegDeleteKeyIncludingSubkeys(HKLM64, RegKey);
  end;
end;
