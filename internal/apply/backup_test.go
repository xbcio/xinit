package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, rel string, data string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCopiesContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello\n", 0o644)
	writeFile(t, dir, "bin/b.sh", "#!/bin/sh\n", 0o755)

	rel, err := Backup(dir, "20261008-153000", []string{"a.txt", "bin/b.sh", "missing.txt", "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if rel != ".xinit/backup/20261008-153000" {
		t.Fatalf("备份目录 = %q", rel)
	}
	data, err := os.ReadFile(filepath.Join(dir, rel, "a.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("a.txt 备份内容不对：%q, %v", data, err)
	}
	fi, err := os.Stat(filepath.Join(dir, rel, "bin/b.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec 位必须随内容一起备份；mode=%v", fi.Mode())
	}
	// 缺失路径与目录不入备份
	if _, err := os.Stat(filepath.Join(dir, rel, "missing.txt")); !os.IsNotExist(err) {
		t.Error("缺失路径不该产生备份条目")
	}
	if _, err := os.Stat(filepath.Join(dir, rel, "sub")); !os.IsNotExist(err) {
		t.Error("目录不该产生备份条目")
	}
}

func TestBackupSameSecondSuffixes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one", 0o644)

	first, err := Backup(dir, "20261008-153000", []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// 改内容再备份一次：第一次的备份必须原封不动（D5：中途失败重跑不会
	// 拿被改写的磁盘覆盖第一次的原文）
	writeFile(t, dir, "a.txt", "two", 0o644)
	second, err := Backup(dir, "20261008-153000", []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if first != ".xinit/backup/20261008-153000" || second != ".xinit/backup/20261008-153000-2" {
		t.Fatalf("同秒第二次应加 -2：%q / %q", first, second)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, first, "a.txt")); string(got) != "one" {
		t.Fatalf("第一次的备份被覆盖：%q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, second, "a.txt")); string(got) != "two" {
		t.Fatalf("第二次备份内容：%q", got)
	}
}

func TestBackupEmptyTargetsCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	rel, err := Backup(dir, "20261008-153000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "" {
		t.Fatalf("无目标应返回空串；got %q", rel)
	}
	if _, err := os.Stat(filepath.Join(dir, ".xinit")); !os.IsNotExist(err) {
		t.Error("无目标时不该创建 .xinit/backup 目录")
	}
}

func TestBackupSkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.txt", "x", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	rel, err := Backup(dir, "20261008-153000", []string{"link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, rel, "link.txt")); !os.IsNotExist(err) {
		t.Error("符号链接不跟随、不备份")
	}
	if strings.Contains(rel, "..") {
		t.Errorf("备份目录不应含 ..：%q", rel)
	}
}
