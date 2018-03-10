package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zealdocs/zealcore/zealindex"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type repoItemExtra struct {
	IndexFilePath string
}

type repoItem struct {
	SourceId string
	Name     string
	Title    string
	Versions []string
	Revision string
	Icon     string
	Icon2x   string
	Extra    repoItemExtra
	Id       string
}

type postItem struct {
	Id string
}

func MakeSearchServer(index, indexMunged, paths []string) func(*websocket.Conn) {
	return func(ws *websocket.Conn) {
		lastQuery := 0
		searcher := zealindex.NewSearcher(index, indexMunged, paths, &lastQuery)

		input := make([]byte, 1024)
		for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {
			inStr := string(bytes.Trim(input, "\x00"))
			input = make([]byte, 1024)

			firstRes := true

			resultCb := func(res zealindex.Result) {
				js, err := json.Marshal(res)
				check(err)
				if firstRes {
					ws.Write([]byte(" "))
				}
				firstRes = false
				ws.Write([]byte(js))
			}

			timeCb := func(curQuery int, t time.Duration) {
				ws.Write([]byte(strconv.Itoa(curQuery) + ";" + fmt.Sprint(t)))
			}

			go zealindex.SearchAllDocs(&searcher, inStr, resultCb, timeCb)
		}
	}
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
    readIndex *int64
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

func extractDocs(title string, f io.Reader, size int64, downloadProgressHandlers progressHandlers) {
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
			progress := 100 * *progressReader.readIndex / size
			if progress > 99 {
				progress = 99  // don't send 100% until the db is closed
			}
			v(title, progress)
		}
		downloadProgressHandlers.Lock.RUnlock()
	}

	wg.Wait()
	db.Close()

	downloadProgressHandlers.Lock.RLock()
	for _, v := range downloadProgressHandlers.Map {
		v(title, 100)
	}
	downloadProgressHandlers.Lock.RUnlock()
}

func extractFile(dbName string, path string, w io.Writer) error {
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

func importRows(db *sql.DB, all, allMunged, paths *[]string, docsetName string) {
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
		*allMunged = append(*allMunged, zealindex.Munge(col))
		if fragment != "" {
			fragment = "#" + fragment
		}
		*paths = append(*paths, docsetName+"/Contents/Resources/Documents/"+path+fragment)
	}
	rows.Close()
}

type progressHandlers struct {
	Map map[int]func(string, int64)
	Lock sync.RWMutex
}

func NewProgressHandlers() progressHandlers {
	return progressHandlers{make(map[int]func(string, int64)), sync.RWMutex{}}
}

type progressReport struct {
	Docset string
	Progress int64
}

func main() {

	var all []string
	var allMunged []string
	var paths []string
	kapeliNames := make(map[string]string)
	kapeliTitles := make(map[string]string)
	var docsetNames []string
	var docsetDbs []string

	var cache *sql.DB
	var err error


	cache, err = sql.Open("sqlite3", "zealcore_cache.sqlite3")
	check(err)
	cache.Exec("CREATE TABLE IF NOT EXISTS kv (key, value)")

	files, err := ioutil.ReadDir(".")
	check(err)

	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".zealdocset") {
			continue
		}

		f, err := ioutil.TempFile("", "zealdb")
		check(err)
		docsetName := strings.Replace(name, ".zealdocset", ".docset", 1)
		check(extractFile(name, docsetName+"/Contents/Resources/docSet.dsidx", f))
		docsetNames = append(docsetNames, docsetName)
		docsetDbs = append(docsetDbs, name)
		f.Close()

		db, err := sql.Open("sqlite3", f.Name())
		if err == nil {
			importRows(db, &all, &allMunged, &paths, docsetName)
			db.Close()
		}
		os.Remove(f.Name())
		check(err)
	}

	fmt.Println(len(all))

	http.Handle("/", http.FileServer(http.Dir("./html")))
	http.HandleFunc("/index", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[{\"name\": \"api.zealdocs.org\", \"id\": 1}]"))
	})
	http.HandleFunc("/index/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos") {
			indexId, err := strconv.Atoi(r.URL.Path[len("/index/") : len(r.URL.Path)-len("/repos")])
			if err == nil && indexId == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("[{\"name\": \"com.kapeli\", \"id\": 1}]"))
				return
			}
		}
		w.WriteHeader(404)
	})
	http.HandleFunc("/repo/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/items") {
			repoId, err := strconv.Atoi(r.URL.Path[len("/repo/") : len(r.URL.Path)-len("/items")])
			if err == nil && repoId == 1 {
				key := "repo.com.kapeli.items"
				dbRes, err := cache.Query("SELECT value FROM kv WHERE key=?", key)
				if err == nil && dbRes.Next() {
					var value []byte
					dbRes.Scan(&value)
					w.Header().Set("Content-Type", "application/json")
					w.Write(value)
					dbRes.Close()
					return
				}
				dbRes.Close()
				res, err := http.Get("http://api.zealdocs.org/v1/docsets")
				if err == nil {
					w.Header().Set("Content-Type", "application/json")
					body, err := ioutil.ReadAll(res.Body)
					if err != nil {
						w.WriteHeader(500)
						w.Write([]byte(err.Error()))
					} else {
						var items []repoItem
						json.Unmarshal(body, &items)
						for _, item := range items {
							kapeliNames[item.Id] = item.Name
							kapeliTitles[item.Id] = item.Title
						}
						w.Write(body)
						cache.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, body)
					}
					return
				} else {
					w.WriteHeader(500)
					w.Write([]byte(err.Error()))
					return
				}
			}
		}
		w.WriteHeader(404)
	})

	downloadProgressHandlers := NewProgressHandlers()
	http.HandleFunc("/item", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var item postItem
			body, err := ioutil.ReadAll(r.Body)
			if err == nil {
				err = json.Unmarshal(body, &item)
			}
			var resp *http.Response
			if err == nil {
				resp, err = http.Get("https://go.zealdocs.org:443/d/com.kapeli/" + kapeliNames[item.Id] + "/latest")
			}
			if err == nil {
				go (func() {
					extractDocs(kapeliTitles[item.Id], resp.Body, resp.ContentLength, downloadProgressHandlers)
				})()
				w.Write([]byte(kapeliNames[item.Id]))
			}
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
			}
		}
	})
	for i, name := range docsetNames {
		(func(i int) {
			http.HandleFunc("/"+name+"/", func(w http.ResponseWriter, r *http.Request) {
				err := extractFile(docsetDbs[i], r.URL.Path[1:], w)
				if err != nil {
					w.WriteHeader(404)
					w.Write([]byte(err.Error()))
				}
			})
		})(i)
	}
	http.Handle("/search", websocket.Handler(MakeSearchServer(all, allMunged, paths)))
	lastDownloadHandler := 0

	http.Handle("/download_progress", websocket.Handler(func(ws *websocket.Conn) {
		lastDownloadHandler += 1
		curDownloadHandler := lastDownloadHandler
		lastProgresses := make(map[string]int64)
		handler := func(docset string, progress int64) {
			if lastProgresses[docset] != progress {
				lastProgresses[docset] = progress
				data, err := json.Marshal(progressReport{docset, progress})
				if progress == 100 {
					ws.Close()
				}
				if err == nil {
					ws.Write(data)
				}
			}
		}
		downloadProgressHandlers.Lock.Lock()
		downloadProgressHandlers.Map[curDownloadHandler] = handler
		downloadProgressHandlers.Lock.Unlock()

		input := make([]byte, 1024)
		for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {}

		downloadProgressHandlers.Lock.Lock()
		delete(downloadProgressHandlers.Map, curDownloadHandler)
		downloadProgressHandlers.Lock.Unlock()
	}))
	err = http.ListenAndServe(":12340", nil)
	check(err)
}
