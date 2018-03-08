package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"os"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {
	f, err := os.Open("AngularJS.tgz")
	check(err)

	gz, err := gzip.NewReader(f)
	check(err)

	tr := tar.NewReader(gz)

	db, err := sql.Open("sqlite3", "AngularJS.zealdocset")
	check(err)

	db.Exec("DROP TABLE files")
	db.Exec("CREATE TABLE files(path, blob)")

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		check(err)

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		var buf = make([]byte, hdr.Size)
		io.ReadFull(tr, buf)

		var gzBlob bytes.Buffer
		zw := gzip.NewWriter(&gzBlob)
		_, err = zw.Write(buf)
		check(err)
		check(zw.Close())

		_, err = db.Exec(
			"INSERT INTO files(path, blob) values(?, ?)", hdr.Name, gzBlob.Bytes())
		check(err)
	}

	db.Close()
}
