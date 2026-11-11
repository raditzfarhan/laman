; laman's Windows setup program (Inno Setup 6).
;
; Per-user: no admin prompt. The setup only carries laman.exe and lets it
; install itself (`laman self-install`), exactly as a manual install does,
; so the setup program and `laman update` never disagree about where laman
; lives. Build with:
;
;   ISCC /DVersion=0.2.0 /DExe=..\dist\laman.exe installer\laman.iss

#ifndef Version
  #define Version "0.0.0"
#endif
#ifndef Exe
  #define Exe "..\dist\laman.exe"
#endif

; Where `laman self-install` puts laman (LAMAN_HOME, default ~\.laman).
#define LamanHome "{%LAMAN_HOME|{%USERPROFILE}\.laman}"
#define LamanBin LamanHome + "\bin"

[Setup]
AppId={{6F1E5A4B-8C2D-4E7A-9B3F-2A1D0C9E8B71}
AppName=laman
AppVersion={#Version}
AppVerName=laman {#Version}
AppPublisher=Raditz Farhan
AppPublisherURL=https://github.com/raditzfarhan/laman
AppSupportURL=https://github.com/raditzfarhan/laman/issues
AppUpdatesURL=https://github.com/raditzfarhan/laman/releases
VersionInfoVersion={#Version}
PrivilegesRequired=lowest
DefaultDirName={localappdata}\Programs\laman
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir=..\dist
OutputBaseFilename=laman-setup-{#Version}
SetupIconFile=..\winres\laman.ico
UninstallDisplayIcon={#LamanBin}\laman.exe
UninstallDisplayName=laman
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
; Environment changes (PATH) are broadcast by laman itself.
ChangesEnvironment=no
CloseApplications=no

[Messages]
WelcomeLabel2=This installs laman {#Version}, a local PHP development environment: PHP, MariaDB and HTTPS .test sites, with nothing to configure.%n%nNo administrator rights are needed. laman lives in your user folder (%USERPROFILE%\.laman).

[Files]
Source: "{#Exe}"; DestDir: "{tmp}"; DestName: "laman.exe"; Flags: deleteafterinstall ignoreversion

[Icons]
Name: "{userprograms}\laman"; Filename: "{#LamanBin}\lamanw.exe"; WorkingDir: "{%USERPROFILE}"; Comment: "Show laman in the notification area"
Name: "{userprograms}\laman dashboard"; Filename: "{#LamanBin}\lamanw.exe"; Parameters: "dashboard"; WorkingDir: "{%USERPROFILE}"; IconFilename: "{#LamanBin}\laman.exe"; Comment: "Open the laman dashboard"

[Run]
; Copies laman into LAMAN_HOME\bin, makes lamanw.exe, adds it to PATH, and
; restarts laman if it was running (an upgrade).
Filename: "{tmp}\laman.exe"; Parameters: "self-install"; WorkingDir: "{%USERPROFILE}"; Flags: runhidden waituntilterminated; StatusMsg: "Installing laman…"
; Finish page choices.
Filename: "{cmd}"; Parameters: "/c """"{#LamanBin}\laman.exe"" setup & echo. & pause"""; Description: "Download and set up PHP, MariaDB, Composer and Node (about 300 MB)"; Flags: postinstall waituntilterminated skipifsilent; Check: NotSetUp
Filename: "{#LamanBin}\laman.exe"; Parameters: "start"; Description: "Start laman and show it in the notification area"; Flags: postinstall runhidden waituntilterminated
Filename: "{#LamanBin}\lamanw.exe"; Flags: postinstall nowait skipifdoesntexist skipifsilent; Description: "Show the laman icon in the notification area"
Filename: "{#LamanBin}\lamanw.exe"; Parameters: "dashboard"; Description: "Open the dashboard"; Flags: postinstall nowait skipifdoesntexist skipifsilent

[UninstallRun]
; Stops laman, removes the sign-in entry, the PATH entry and laman's CA.
Filename: "{#LamanBin}\laman.exe"; Parameters: "self-uninstall --quiet"; Flags: runhidden waituntilterminated; RunOnceId: "SelfUninstall"

[UninstallDelete]
Type: files; Name: "{#LamanBin}\laman.exe"
Type: files; Name: "{#LamanBin}\lamanw.exe"
Type: files; Name: "{#LamanBin}\laman.exe.old"
Type: files; Name: "{#LamanBin}\laman.exe.new"

[Code]
function LamanHome(): String;
begin
  Result := ExpandConstant('{#LamanHome}');
end;

// No config yet: a first install, so offer `laman setup`.
function NotSetUp(): Boolean;
begin
  Result := not FileExists(LamanHome() + '\config.json');
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if (CurUninstallStep = usPostUninstall) and not UninstallSilent() and DirExists(LamanHome()) then
  begin
    if MsgBox('Also delete everything laman stored in ' + LamanHome() + '?' + #13#10#13#10 +
              'That is your MariaDB and PostgreSQL databases, the installed PHP versions, ' +
              'certificates and settings. Your project folders are not touched.' + #13#10#13#10 +
              'Choose No to keep them for a later reinstall.',
              mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
      DelTree(LamanHome(), True, True, True);
  end;
end;
