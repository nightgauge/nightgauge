// Command release-dwarf-check proves that every Go binary a release ships
// carries DWARF debug information, uncompressed.
//
// Release binaries ship unstripped (docs/RELEASE_BINARIES.md): Microsoft
// Defender's model flagged the stripped Go ELF. DWARF is the best available
// lever, not a guarantee: v0.8.4 was flagged with DWARF present. The release
// builds also keep DWARF uncompressed (-compressdwarf=false): the Linux ELF sits
// at that model's threshold, and uncompressed DWARF on go1.27.3 was its
// lowest-rate build (#3192), and the signed universal darwin file was measured
// the same way (#3161). This tool reads the packaged bytes, not the linker
// flags, so it also catches a binary stripped or rebuilt by any later step.
//
//	release-dwarf-check PATH...
//
// Each PATH is a .tar.gz/.tgz archive, a .vsix/.zip archive, or a raw binary.
// Inside an archive, every regular entry that is an ELF or Mach-O Go binary is
// checked; other entries are ignored. A fat Mach-O must carry DWARF in every
// architecture slice. Compressed DWARF is an ELF .debug_* section flagged
// SHF_COMPRESSED (or a legacy .zdebug_* section), or a Mach-O __zdebug_*
// section.
//
// Exit codes:
//
//	0  every Go binary found carries uncompressed DWARF
//	1  at least one Go binary lacks DWARF or carries it compressed
//	2  nothing was asserted: no arguments, an unreadable path, a path that is
//	   not a Go binary, or an archive holding no Go binary (a HARNESS ERROR line
//	   says which)
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"debug/buildinfo"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const record = "docs/RELEASE_BINARIES.md"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// verdict is the outcome of checking one path.
type verdict int

const (
	verdictOK verdict = iota
	verdictMissing
	verdictCompressed
	verdictHarness
)

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "HARNESS ERROR: usage: release-dwarf-check PATH... (no paths given)")
		return 2
	}
	worst := verdictOK
	for _, path := range args {
		v := checkPath(path, stdout, stderr)
		if v > worst {
			worst = v
		}
	}
	// Exit 2 outranks exit 1: an unchecked path means the run asserted less
	// than it was asked to. Both fail the workflow step.
	switch worst {
	case verdictOK:
		return 0
	case verdictMissing, verdictCompressed:
		return 1
	default:
		return 2
	}
}

func checkPath(path string, stdout, stderr io.Writer) verdict {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return checkTarGz(path, stdout, stderr)
	case strings.HasSuffix(lower, ".vsix"), strings.HasSuffix(lower, ".zip"):
		return checkZip(path, stdout, stderr)
	default:
		return checkRaw(path, stdout, stderr)
	}
}

func checkRaw(path string, stdout, stderr io.Writer) verdict {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", path, err)
		return verdictHarness
	}
	defer f.Close()
	found, v := checkEntry(path, f, stdout, stderr)
	if !found {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s is not a Go ELF or Mach-O binary\n", path)
		return verdictHarness
	}
	return v
}

func checkTarGz(path string, stdout, stderr io.Writer) verdict {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", path, err)
		return verdictHarness
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", path, err)
		return verdictHarness
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	worst, binaries := verdictOK, 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", path, err)
			return verdictHarness
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		found, v := checkEntry(path+"!"+hdr.Name, tr, stdout, stderr)
		if found {
			binaries++
		}
		if v > worst {
			worst = v
		}
	}
	return archiveVerdict(path, binaries, worst, stderr)
}

func checkZip(path string, stdout, stderr io.Writer) verdict {
	zr, err := zip.OpenReader(path)
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", path, err)
		return verdictHarness
	}
	defer zr.Close()
	worst, binaries := verdictOK, 0
	for _, zf := range zr.File {
		if !zf.Mode().IsRegular() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			fmt.Fprintf(stderr, "HARNESS ERROR: %s!%s: %v\n", path, zf.Name, err)
			worst = verdictHarness
			continue
		}
		found, v := checkEntry(path+"!"+zf.Name, rc, stdout, stderr)
		rc.Close()
		if found {
			binaries++
		}
		if v > worst {
			worst = v
		}
	}
	return archiveVerdict(path, binaries, worst, stderr)
}

func archiveVerdict(path string, binaries int, worst verdict, stderr io.Writer) verdict {
	if binaries == 0 && worst == verdictOK {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s holds no Go binary; nothing was checked\n", path)
		return verdictHarness
	}
	return worst
}

// checkEntry reports whether r is a Go executable and, if so, whether it
// carries DWARF. Non-binary content returns found=false with verdictOK.
func checkEntry(label string, r io.Reader, stdout, stderr io.Writer) (bool, verdict) {
	head := make([]byte, 4)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return false, verdictHarness
	}
	kind := magicKind(head[:n])
	if kind == kindNone {
		return false, verdictOK
	}
	// debug/elf and debug/macho need random access, so spool the entry.
	tmp, err := os.CreateTemp("", "release-dwarf-check-*")
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return false, verdictHarness
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, io.MultiReader(bytes.NewReader(head[:n]), r)); err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return false, verdictHarness
	}
	if err := tmp.Sync(); err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return false, verdictHarness
	}
	// Only Go binaries are in scope. buildinfo survives -s -w, so a stripped
	// Go binary is still recognised as one.
	if _, err := buildinfo.ReadFile(tmp.Name()); err != nil {
		return false, verdictOK
	}
	ok, err := hasDWARF(kind, tmp.Name())
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return true, verdictHarness
	}
	if !ok {
		fmt.Fprintf(stdout, "::error::%s has no DWARF debug information; release binaries ship unstripped (see %s)\n", label, record)
		return true, verdictMissing
	}
	section, err := compressedDWARF(kind, tmp.Name())
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
		return true, verdictHarness
	}
	if section != "" {
		fmt.Fprintf(stdout, "::error::%s carries compressed DWARF (%s); release binaries are built with -compressdwarf=false (see %s)\n", label, section, record)
		return true, verdictCompressed
	}
	fmt.Fprintf(stdout, "ok   %s\n", label)
	return true, verdictOK
}

type binKind int

const (
	kindNone binKind = iota
	kindELF
	kindMachO
	kindFat
)

func magicKind(b []byte) binKind {
	if len(b) < 4 {
		return kindNone
	}
	switch {
	case bytes.Equal(b, []byte{0x7f, 'E', 'L', 'F'}):
		return kindELF
	case bytes.Equal(b, []byte{0xfe, 0xed, 0xfa, 0xcf}),
		bytes.Equal(b, []byte{0xcf, 0xfa, 0xed, 0xfe}),
		bytes.Equal(b, []byte{0xfe, 0xed, 0xfa, 0xce}),
		bytes.Equal(b, []byte{0xce, 0xfa, 0xed, 0xfe}):
		return kindMachO
	case bytes.Equal(b, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return kindFat
	}
	return kindNone
}

// hasDWARF opens the binary and reports whether its DWARF data yields at
// least one entry. A missing or empty DWARF section is false, not an error.
func hasDWARF(kind binKind, path string) (bool, error) {
	switch kind {
	case kindELF:
		f, err := elf.Open(path)
		if err != nil {
			return false, err
		}
		defer f.Close()
		return dwarfPresent(f.DWARF()), nil
	case kindMachO:
		f, err := macho.Open(path)
		if err != nil {
			return false, err
		}
		defer f.Close()
		return dwarfPresent(f.DWARF()), nil
	case kindFat:
		ff, err := macho.OpenFat(path)
		if err != nil {
			return false, err
		}
		defer ff.Close()
		if len(ff.Arches) == 0 {
			return false, errors.New("fat Mach-O holds no architectures")
		}
		for _, a := range ff.Arches {
			if !dwarfPresent(a.DWARF()) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, fmt.Errorf("unsupported binary kind %d", kind)
}

func dwarfPresent(d *dwarf.Data, err error) bool {
	if err != nil || d == nil {
		return false
	}
	e, err := d.Reader().Next()
	return err == nil && e != nil
}

// compressedDWARF returns the name of the first compressed DWARF section in
// the binary, or "" when its DWARF is stored uncompressed. A fat Mach-O is
// compressed when any architecture slice is.
func compressedDWARF(kind binKind, path string) (string, error) {
	switch kind {
	case kindELF:
		f, err := elf.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return elfCompressedDWARF(f), nil
	case kindMachO:
		f, err := macho.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return machoCompressedDWARF(f), nil
	case kindFat:
		ff, err := macho.OpenFat(path)
		if err != nil {
			return "", err
		}
		defer ff.Close()
		for _, a := range ff.Arches {
			if name := machoCompressedDWARF(a.File); name != "" {
				return name, nil
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("unsupported binary kind %d", kind)
}

func elfCompressedDWARF(f *elf.File) string {
	for _, s := range f.Sections {
		if strings.HasPrefix(s.Name, ".zdebug_") {
			return s.Name
		}
		if strings.HasPrefix(s.Name, ".debug_") && s.Flags&elf.SHF_COMPRESSED != 0 {
			return s.Name
		}
	}
	return ""
}

func machoCompressedDWARF(f *macho.File) string {
	for _, s := range f.Sections {
		if strings.HasPrefix(s.Name, "__zdebug_") {
			return s.Name
		}
	}
	return ""
}
