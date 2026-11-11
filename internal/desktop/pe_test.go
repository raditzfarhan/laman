package desktop

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestMakeWindowedFlipsOnlyTheSubsystem(t *testing.T) {
	src, err := os.Executable() // the test binary is a real console PE
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), WindowedName)
	if err := MakeWindowed(src, dest); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(dest)
	if len(a) != len(b) {
		t.Fatal("size changed")
	}
	off := int(binary.LittleEndian.Uint32(a[0x3c:])) + 4 + 20 + 68
	if got := binary.LittleEndian.Uint16(a[off:]); got != subsystemConsole {
		t.Fatalf("source subsystem = %d, want console", got)
	}
	if got := binary.LittleEndian.Uint16(b[off:]); got != subsystemGUI {
		t.Fatalf("copy subsystem = %d, want GUI", got)
	}
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	if diff != 1 { // console(3) -> GUI(2) changes a single byte
		t.Fatalf("%d bytes differ, want 1", diff)
	}
}

func TestSetSubsystemRejectsGarbage(t *testing.T) {
	if err := setSubsystem([]byte("not an exe at all, definitely not"), subsystemGUI); err == nil {
		t.Fatal("expected an error")
	}
}
