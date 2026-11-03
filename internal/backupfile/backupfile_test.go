package backupfile

import (
	"bytes"
	"errors"
	"testing"
)

func TestSealOpen(t *testing.T) {
	data := []byte("строки бэкапа")
	file, err := Seal(data, "пароль-1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, data) {
		t.Fatal("данные лежат в файле открытым текстом")
	}
	got, err := Open(file, "пароль-1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Open: %q %v", got, err)
	}

	cases := []struct {
		name string
		file []byte
		pass string
		want error
	}{
		{"чужой пароль", file, "пароль-2", ErrPassword},
		{"не бэкап", []byte("hello"), "пароль-1", ErrNotBackup},
		{"обрезан", file[:len(file)-1], "пароль-1", ErrPassword},
		{"только метка", []byte(magic), "пароль-1", ErrPassword},
		{"подменён байт", flip(file, len(file)-3), "пароль-1", ErrPassword},
		{"подменена соль", flip(file, len(magic)+1), "пароль-1", ErrPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Open(tc.file, tc.pass); !errors.Is(err, tc.want) {
				t.Fatalf("Open: %v, ждали %v", err, tc.want)
			}
		})
	}

	// Каждый файл со своей солью и nonce.
	again, _ := Seal(data, "пароль-1")
	if bytes.Equal(again, file) {
		t.Fatal("два файла с одинаковым содержимым совпали побайтно")
	}
}

func flip(b []byte, i int) []byte {
	c := append([]byte(nil), b...)
	c[i] ^= 0xff
	return c
}
