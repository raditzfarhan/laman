//go:build ignore

// gen writes rsrc_windows_amd64.syso: the Windows resources Go links into
// laman.exe (and so into lamanw.exe, a patched copy). It holds the app icon
// from logo/kit, version info (Task Manager shows FileDescription as the
// process name) and a manifest that makes the process DPI aware, so the tray
// icon is drawn at native size instead of being stretched.
//
// Run from the module root:  go run winres/gen.go [version]
// Release builds pass their version (0.2.0); without one it is 0.0.0.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf16"
)

var version = "0.0.0"

const (
	icoPath = "winres/laman.ico"
	outPath = "rsrc_windows_amd64.syso"
)

func manifest() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="laman" version="` + version + `.0" processorArchitecture="amd64"/>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application><supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/></application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
    </windowsSettings>
  </application>
  <dependency><dependentAssembly>
    <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
  </dependentAssembly></dependency>
</assembly>
`
}

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
	langEnUS    = 0x0409
)

type resource struct {
	typ, id uint16
	data    []byte
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		version = strings.TrimPrefix(os.Args[1], "v")
		var a, b, c int
		if n, _ := fmt.Sscanf(version, "%d.%d.%d", &a, &b, &c); n != 3 {
			return fmt.Errorf("version %q is not X.Y.Z", os.Args[1])
		}
	}
	ico, err := os.ReadFile(icoPath)
	if err != nil {
		return err
	}
	res, err := iconResources(ico)
	if err != nil {
		return err
	}
	res = append(res,
		resource{rtVersion, 1, versionInfo()},
		resource{rtManifest, 1, []byte(manifest())},
	)
	if err := os.WriteFile(outPath, coff(res), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d resources)\n", outPath, len(res))
	return nil
}

// iconResources splits an .ico into one RT_ICON per image plus the
// RT_GROUP_ICON directory that points at them by ID.
func iconResources(ico []byte) ([]resource, error) {
	le := binary.LittleEndian
	if len(ico) < 6 || le.Uint16(ico[2:]) != 1 {
		return nil, fmt.Errorf("%s is not an .ico", icoPath)
	}
	n := int(le.Uint16(ico[4:]))
	var group bytes.Buffer
	binary.Write(&group, le, [3]uint16{0, 1, uint16(n)})
	var out []resource
	for i := 0; i < n; i++ {
		e := ico[6+16*i : 22+16*i]
		size, off := le.Uint32(e[8:]), le.Uint32(e[12:])
		if int(off+size) > len(ico) {
			return nil, fmt.Errorf("%s: image %d is truncated", icoPath, i)
		}
		id := uint16(i + 1)
		out = append(out, resource{rtIcon, id, ico[off : off+size]})
		group.Write(e[:12]) // width, height, colours, reserved, planes, bit count, size
		binary.Write(&group, le, id)
	}
	return append(out, resource{rtGroupIcon, 1, group.Bytes()}), nil
}

// versionInfo builds a VS_VERSIONINFO block.
func versionInfo() []byte {
	var maj, min, pat uint32
	fmt.Sscanf(version, "%d.%d.%d", &maj, &min, &pat)
	ms, ls := maj<<16|min, pat<<16
	var fixed bytes.Buffer
	binary.Write(&fixed, binary.LittleEndian, [13]uint32{
		0xFEEF04BD, 0x00010000, // signature, struct version
		ms, ls, ms, ls, // file version, product version
		0x3F, 0, // flags mask, flags
		0x00040004, 1, 0, // VOS_NT_WINDOWS32, VFT_APP, subtype
		0, 0, // date
	})
	str := func(k, v string) []byte {
		val := utf16z(v)
		return node(k, 1, val, uint16(len(val)/2))
	}
	table := node("040904B0", 1, nil, 0,
		str("CompanyName", "laman"),
		str("FileDescription", "laman"),
		str("FileVersion", version),
		str("InternalName", "laman"),
		str("LegalCopyright", "Raditz Farhan"),
		str("OriginalFilename", "laman.exe"),
		str("ProductName", "laman"),
		str("ProductVersion", version),
	)
	translation := make([]byte, 4)
	binary.LittleEndian.PutUint32(translation, 0x04B00409) // en-US, Unicode
	return node("VS_VERSION_INFO", 0, fixed.Bytes(), uint16(fixed.Len()),
		node("StringFileInfo", 1, nil, 0, table),
		node("VarFileInfo", 1, nil, 0, node("Translation", 0, translation, 4)),
	)
}

// node encodes one version-info structure: header, key, value, children,
// each starting on a 4-byte boundary.
func node(key string, typ uint16, value []byte, valueLen uint16, children ...[]byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, valueLen, typ})
	b.Write(utf16z(key))
	pad4(&b)
	b.Write(value)
	for _, c := range children {
		pad4(&b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	return out
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func utf16z(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// coff lays out a three-level resource tree (type / id / language) in a
// single .rsrc section. Data entries hold section-relative RVAs, so each
// gets an IMAGE_REL_AMD64_ADDR32NB relocation that the linker resolves.
func coff(res []resource) []byte {
	le := binary.LittleEndian
	sort.Slice(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	var types []uint16
	byType := map[uint16][]resource{}
	for _, r := range res {
		if len(byType[r.typ]) == 0 {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], r)
	}

	const dirHdr, entry, dataEntry = 16, 8, 16
	// Sizes of each region, so every offset is known before writing.
	rootSize := dirHdr + entry*len(types)
	idDirsSize, langDirsSize := 0, 0
	for _, t := range types {
		idDirsSize += dirHdr + entry*len(byType[t])
		langDirsSize += (dirHdr + entry) * len(byType[t])
	}
	dataEntriesOff := rootSize + idDirsSize + langDirsSize
	blobsOff := dataEntriesOff + dataEntry*len(res)

	var sec bytes.Buffer
	dir := func(n int) { binary.Write(&sec, le, [4]uint32{0, 0, 0, uint32(n) << 16}) } // 0 named, n id entries
	ent := func(id uint16, off int, subdir bool) {
		v := uint32(off)
		if subdir {
			v |= 0x80000000
		}
		binary.Write(&sec, le, [2]uint32{uint32(id), v})
	}

	// Root: one entry per type, pointing at that type's id directory.
	dir(len(types))
	off := rootSize
	for _, t := range types {
		ent(t, off, true)
		off += dirHdr + entry*len(byType[t])
	}
	// Id directories, each pointing at a language directory.
	langOff := rootSize + idDirsSize
	for _, t := range types {
		dir(len(byType[t]))
		for _, r := range byType[t] {
			ent(r.id, langOff, true)
			langOff += dirHdr + entry
		}
	}
	// Language directories, each pointing at a data entry (not a subdir).
	for i := range res {
		dir(1)
		ent(langEnUS, dataEntriesOff+dataEntry*i, false)
	}
	// Data entries, then the blobs, 8-byte aligned.
	var relocs []uint32
	blobOff := blobsOff
	for _, r := range res {
		relocs = append(relocs, uint32(sec.Len()))
		binary.Write(&sec, le, [4]uint32{uint32(blobOff), uint32(len(r.data)), 0, 0})
		blobOff += (len(r.data) + 7) &^ 7
	}
	for _, r := range res {
		sec.Write(r.data)
		for sec.Len()%8 != 0 {
			sec.WriteByte(0)
		}
	}

	const fileHdr, secHdr, relocSize = 20, 40, 10
	rawOff := fileHdr + secHdr
	relocOff := rawOff + sec.Len()
	symOff := relocOff + relocSize*len(relocs)

	var out bytes.Buffer
	binary.Write(&out, le, struct {
		Machine, Sections        uint16
		Time, SymPtr, SymCount   uint32
		OptHdrSize, Characterist uint16
	}{0x8664, 1, 0, uint32(symOff), 1, 0, 0})
	binary.Write(&out, le, struct {
		Name                                  [8]byte
		VSize, VAddr, RawSize, RawPtr, RelPtr uint32
		LinePtr                               uint32
		RelCount, LineCount                   uint16
		Characteristics                       uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(sec.Len()), uint32(rawOff), uint32(relocOff), 0, uint16(len(relocs)), 0,
		0x40000040}) // initialized data, readable
	out.Write(sec.Bytes())
	for _, r := range relocs {
		binary.Write(&out, le, r)         // offset of the RVA field
		binary.Write(&out, le, uint32(0)) // symbol 0: the section
		binary.Write(&out, le, uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	// Symbol table: the .rsrc section symbol, then an empty string table.
	binary.Write(&out, le, struct {
		Name          [8]byte
		Value         uint32
		Section       int16
		Type          uint16
		Class, NumAux uint8
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 1, 0, 3, 0})
	binary.Write(&out, le, uint32(4))
	return out.Bytes()
}
