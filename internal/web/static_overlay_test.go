package web

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadIndexHTML_Overlay(t *testing.T) {
	dir := t.TempDir()

	// Без кастомных файлов — вшитый index.html.
	s := &Server{staticDir: dir}
	b, err := s.readIndexHTML("cabinet.html")
	if err != nil {
		t.Fatalf("embedded fallback: %v", err)
	}
	if !strings.Contains(string(b), "<!DOCTYPE html>") && !strings.Contains(string(b), "<!doctype html>") {
		t.Fatalf("embedded index.html не похож на HTML")
	}
	embedded := string(b)

	// Общий кастомный index.html перекрывает вшитый для обеих поверхностей.
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("CUSTOM-COMMON"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, specific := range []string{"cabinet.html", "miniapp.html"} {
		b, err = s.readIndexHTML(specific)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "CUSTOM-COMMON" {
			t.Fatalf("%s: ожидался общий кастомный index.html, получено %q", specific, b)
		}
	}

	// Специфичный файл приоритетнее общего.
	if err := os.WriteFile(filepath.Join(dir, "cabinet.html"), []byte("CUSTOM-CABINET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ = s.readIndexHTML("cabinet.html"); string(b) != "CUSTOM-CABINET" {
		t.Fatalf("cabinet.html должен перекрывать index.html, получено %q", b)
	}
	if b, _ = s.readIndexHTML("miniapp.html"); string(b) != "CUSTOM-COMMON" {
		t.Fatalf("miniapp без miniapp.html должен получать общий index.html, получено %q", b)
	}

	// Пустая/несуществующая папка — вшитый файл.
	s2 := &Server{staticDir: filepath.Join(dir, "nope")}
	if b, err = s2.readIndexHTML("cabinet.html"); err != nil || string(b) != embedded {
		t.Fatalf("несуществующая папка должна отдавать вшитый index.html (err=%v)", err)
	}
	s3 := &Server{}
	if b, err = s3.readIndexHTML("cabinet.html"); err != nil || string(b) != embedded {
		t.Fatalf("пустой staticDir должен отдавать вшитый index.html (err=%v)", err)
	}
}

func TestOverlayFS_Open(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qrcode.min.js"), []byte("CUSTOM-JS"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{staticDir: dir}
	fsys, err := s.staticFS()
	if err != nil {
		t.Fatal(err)
	}

	// Кастомный файл перекрывает вшитый.
	f, err := fsys.Open("qrcode.min.js")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	_ = f.Close()
	if string(got) != "CUSTOM-JS" {
		t.Fatalf("ожидался кастомный qrcode.min.js, получено %q", got)
	}

	// Отсутствующий в папке файл падает на вшитый.
	f, err = fsys.Open("index.html")
	if err != nil {
		t.Fatalf("fallback на вшитый index.html: %v", err)
	}
	got, _ = io.ReadAll(f)
	_ = f.Close()
	if !strings.Contains(string(got), "<script") {
		t.Fatalf("вшитый index.html не похож на SPA")
	}

	// Попытка выйти из папки не должна читать файлы вне её.
	if _, err := fsys.Open("../secret"); err == nil {
		t.Fatal("выход из папки через .. должен быть запрещён")
	}

	// Каталоги не отдаются (иначе FileServer нарисует листинг папки).
	if err := os.MkdirAll(filepath.Join(dir, "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "img", "logo.svg"), []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open("img"); err == nil {
		t.Fatal("каталог не должен открываться")
	}
	if f, err := fsys.Open("img/logo.svg"); err != nil {
		t.Fatalf("файл в подпапке должен отдаваться: %v", err)
	} else {
		_ = f.Close()
	}
}
