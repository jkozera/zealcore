package zealindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"io"
	"runtime"
	"sync"
)

func NewProgressHandlers() progressHandlers {
	return progressHandlers{make(map[int]func(string, int64, int64)), sync.RWMutex{}}
}

type progressHandlers struct {
	Map  map[int]func(string, int64, int64)
	Lock sync.RWMutex
}

type gzJob struct {
	Hdr  *tar.Header
	toGz []byte
}

type gzRes struct {
	Hdr *tar.Header
	gz  []byte
}

type ReaderWithProgress struct {
	underlyingReader io.Reader
	readIndex        *int64
}

func NewReaderWithProgress(underlying io.Reader) ReaderWithProgress {
	progress := int64(0)
	return ReaderWithProgress{underlying, &progress}
}

func (r ReaderWithProgress) Read(p []byte) (n int, err error) {
	n, err = r.underlyingReader.Read(p)
	*r.readIndex += int64(n)
	return n, err
}

func ExtractDocs(title string, f io.Reader, contentType string, size int64, downloadProgressHandlers progressHandlers) {
	progressReader := NewReaderWithProgress(f)
	gz, err := gzip.NewReader(progressReader)
	check(err)

	tr := tar.NewReader(gz)

	db, err := sql.Open("sqlite3", title+".zealdocset")
	check(err)

	db.Exec("DROP TABLE files")
	db.Exec("CREATE TABLE files(path, blob)")

	threads := runtime.NumCPU()
	toGzChan := make(chan gzJob, threads)
	toWriteChan := make(chan gzRes, threads)

	var wg sync.WaitGroup

	for i := 0; i < threads; i += 1 {
		go (func() {
			for {
				toGz := <-toGzChan
				var gzBlob bytes.Buffer
				zw := gzip.NewWriter(&gzBlob)
				_, err = zw.Write(toGz.toGz)
				check(err)
				check(zw.Close())
				wg.Add(1)
				toWriteChan <- gzRes{toGz.Hdr, gzBlob.Bytes()}
				wg.Done()
			}
		})()
	}

	go (func() {
		for {
			toWrite := <-toWriteChan
			_, err = db.Exec(
				"INSERT INTO files(path, blob) values(?, ?)", toWrite.Hdr.Name, toWrite.gz)
			check(err)
			wg.Done()
		}
	})()

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
		_, err = io.ReadFull(tr, buf)
		check(err)
		wg.Add(1)
		toGzChan <- gzJob{hdr, buf}

		downloadProgressHandlers.Lock.RLock()
		for _, v := range downloadProgressHandlers.Map {
			progress := *progressReader.readIndex
			if progress >= size {
				progress = size - 1 // don't send 100% until the db is closed
			}
			v(title, progress, size)
		}
		downloadProgressHandlers.Lock.RUnlock()
	}

	wg.Wait()
	db.Close()

	downloadProgressHandlers.Lock.RLock()
	for _, v := range downloadProgressHandlers.Map {
		v(title, size, size)
	}
	downloadProgressHandlers.Lock.RUnlock()
}

func ExtractFile(dbName string, path string, w io.Writer) error {
	db, err := sql.Open("sqlite3", dbName)
	var res *sql.Rows
	if err == nil {
		res, err = db.Query("SELECT blob FROM files WHERE path = ?", path)
	}
	if err == nil && res.Next() {
		var blob []byte
		res.Scan(&blob)
		buf := bytes.NewBuffer(blob)
		gz, err := gzip.NewReader(buf)
		if err != nil {
			res.Close()
			db.Close()
			return err
		} else {
			_, err = io.Copy(w, gz)
			res.Close()
			db.Close()
			return err
		}
	} else {
		if err != nil {
			db.Close()
			return err
		} else {
			res.Close()
			db.Close()
			return errors.New("not found: " + path)
		}
	}
}

func ImportRows(db *sql.DB, all, allMunged, paths *[]string, docsets *[]int, docsetName string, docsetNum int) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	check(err)

	var col string
	var path string
	var fragment string
	hasSearchIndex := false
	for rows.Next() {
		err = rows.Scan(&col)
		if col == "searchIndex" {
			hasSearchIndex = true
		}
	}
	rows.Close()

	if !hasSearchIndex {
		db.Exec("CREATE VIEW IF NOT EXISTS searchIndexView AS" +
			"  SELECT" +
			"    ztokenname AS name," +
			"    ztypename AS type," +
			"    zpath AS path," +
			"    zanchor AS fragment" +
			"  FROM ztoken" +
			"  INNER JOIN ztokenmetainformation" +
			"    ON ztoken.zmetainformation = ztokenmetainformation.z_pk" +
			"  INNER JOIN zfilepath" +
			"    ON ztokenmetainformation.zfile = zfilepath.z_pk" +
			"  INNER JOIN ztokentype" +
			"    ON ztoken.ztokentype = ztokentype.z_pk")
	} else {
		db.Exec("CREATE VIEW IF NOT EXISTS searchIndexView AS" +
			"  SELECT" +
			"    name, type, path, '' AS fragment" +
			"  FROM searchIndex")
	}

	rows, err = db.Query("select name, path, coalesce(fragment, '') FROM searchIndexView")
	check(err)

	for rows.Next() {
		err = rows.Scan(&col, &path, &fragment)
		check(err)
		*all = append(*all, col)
		*allMunged = append(*allMunged, Munge(col))
		*docsets = append(*docsets, docsetNum)
		if fragment != "" {
			fragment = "#" + fragment
		}
		*paths = append(*paths, docsetName+"/Contents/Resources/Documents/"+path+fragment)
	}
	rows.Close()
}
