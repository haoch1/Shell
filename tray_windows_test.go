package main

import (
	"testing"
	"unsafe"

	"github.com/haoch1/Shell/internal/local"
)

func TestTrayWin64StructLayoutAndHostOrdering(t *testing.T) {
	var nid notifyIconDataW
	if got := unsafe.Sizeof(nid); got != 976 {
		t.Fatalf("expected Win64 NOTIFYICONDATAW size 976, got %d", got)
	}
	if off := unsafe.Offsetof(nid.hWnd); off != 8 {
		t.Fatalf("expected hWnd offset 8, got %d", off)
	}
	if off := unsafe.Offsetof(nid.hIcon); off != 32 {
		t.Fatalf("expected hIcon offset 32, got %d", off)
	}
	if off := unsafe.Offsetof(nid.szTip); off != 40 {
		t.Fatalf("expected szTip offset 40, got %d", off)
	}
	if off := unsafe.Offsetof(nid.hBalloonIcon); off != 968 {
		t.Fatalf("expected hBalloonIcon offset 968, got %d", off)
	}

	hosts := []local.Profile{
		{ID: "a", Name: "Alpha", LastUsedAt: 100},
		{ID: "b", Name: "Beta", Pinned: true, LastUsedAt: 10},
		{ID: "c", Name: "Gamma", LastUsedAt: 500},
	}
	ordered := trayHostChoices(hosts, 2)
	if len(ordered) != 2 || ordered[0].ID != "b" || ordered[1].ID != "c" {
		t.Fatalf("unexpected tray quick-connect ordering: %+v", ordered)
	}
}
