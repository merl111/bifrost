package qemu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildLaunchArgsValidatesOwnedPathsAndResources(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := LaunchConfig{DiskPath: disk, SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qemu.pid"), Machine: "q35", Architecture: "x86_64", Accelerator: "tcg", MemoryMB: 512, CPUs: 2, NetworkNAT: true}
	args, err := BuildLaunchArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-drive", "format=qcow2", "-machine q35", "-accel tcg", "-netdev user,id=bifrostnet", "-display none", "-qmp unix:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %q does not contain %q", joined, want)
		}
	}
	if _, err := BuildLaunchArgs(LaunchConfig{DiskPath: disk, SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qmp.sock")}); err == nil {
		t.Fatal("same socket and pid path accepted")
	}
	if _, err := BuildLaunchArgs(LaunchConfig{DiskPath: disk, SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qemu.pid"), Accelerator: "-monitor"}); err == nil {
		t.Fatal("freeform accelerator accepted")
	}
}

func TestBuildLaunchArgsRejectsMissingDiskAndExistingControlFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := LaunchConfig{DiskPath: filepath.Join(dir, "missing"), SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qemu.pid")}
	if _, err := BuildLaunchArgs(cfg); err == nil {
		t.Fatal("missing disk accepted")
	}
	disk := filepath.Join(dir, "disk")
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qmp.sock"), []byte("owned by someone"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DiskPath = disk
	if _, err := BuildLaunchArgs(cfg); err == nil || !strings.Contains(err.Error(), "socket_path") {
		t.Fatalf("existing socket path error = %v", err)
	}
}

func TestBuildLaunchArgsRejectsOptionGrammarPathsAndSelectsArchitectureBinary(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{filepath.Join(dir, "disk,unsafe.qcow2"), filepath.Join(dir, "disk\\unsafe.qcow2")} {
		if err := os.WriteFile(bad, []byte("disk"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildLaunchArgs(LaunchConfig{DiskPath: bad, SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qemu.pid")}); err == nil {
			t.Fatalf("unsafe disk path %q accepted", bad)
		}
	}
	args, err := BuildLaunchArgs(LaunchConfig{DiskPath: disk, SocketPath: filepath.Join(dir, "qmp.sock"), PIDPath: filepath.Join(dir, "qemu.pid"), Architecture: "riscv64"})
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "qemu-system-riscv64" {
		t.Fatalf("architecture binary = %q", args[0])
	}
}
