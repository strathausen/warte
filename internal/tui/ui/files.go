package ui

import "strings"

// riskyExts are file extensions that the platform openers (open,
// xdg-open, start) hand to something that runs code or follows a link
// instead of showing a document: shell/terminal launchers, installers,
// script hosts, Java, desktop entries and URL/file shortcuts. Attachments
// come from other people, so warte never opens these (saving them is
// still allowed).
var riskyExts = map[string]bool{
	// macOS
	".command": true, ".tool": true, ".terminal": true, ".app": true, ".pkg": true, ".mpkg": true,
	".workflow": true, ".action": true, ".definition": true, ".scpt": true, ".scptd": true,
	".applescript": true, ".osax": true, ".prefpane": true, ".saver": true, ".kext": true,
	".webloc": true, ".inetloc": true, ".fileloc": true, ".afploc": true, ".shortcut": true,
	".dmg": true, ".mobileconfig": true, ".plugin": true, ".bundle": true,
	// Linux / Unix
	".desktop": true, ".sh": true, ".bash": true, ".zsh": true, ".csh": true, ".ksh": true,
	".run": true, ".bin": true, ".appimage": true, ".deb": true, ".rpm": true, ".flatpakref": true,
	".py": true, ".pl": true, ".rb": true, ".elf": true, ".so": true,
	// Java and cross-platform
	".jar": true, ".jnlp": true, ".class": true,
	// Windows
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".scr": true, ".pif": true,
	".msi": true, ".msp": true, ".msix": true, ".appx": true, ".lnk": true, ".url": true,
	".hta": true, ".cpl": true, ".reg": true, ".ps1": true, ".psm1": true, ".vbs": true,
	".vbe": true, ".js": true, ".jse": true, ".wsf": true, ".wsh": true, ".wsc": true,
	".inf": true, ".dll": true, ".scf": true, ".application": true, ".gadget": true,
	".settingcontent-ms": true, ".library-ms": true, ".search-ms": true,
}

// RiskyExtension reports whether a file with extension ext (".jar",
// case-insensitive) must not be handed to an external opener: the default
// openers would run it or follow it rather than display it.
func RiskyExtension(ext string) bool {
	return riskyExts[strings.ToLower(ext)]
}

// RiskyOpenText is the toast shown when opening such a file is refused.
const RiskyOpenText = "This file type can run code, so warte won't open it. Save it instead."
