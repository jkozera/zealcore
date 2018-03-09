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

func extractDocs(title string, f io.Reader) {
	gz, err := gzip.NewReader(f)
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
	}

	wg.Wait()

	db.Close()
}

func extractFile(db *sql.DB, path string, w io.Writer) error {
	res, err := db.Query("SELECT blob FROM files WHERE path = ?", path)
	if err == nil && res.Next() {
		var blob []byte
		res.Scan(&blob)
		buf := bytes.NewBuffer(blob)
		gz, err := gzip.NewReader(buf)
		if err != nil {
			return err
		} else {
			_, err = io.Copy(w, gz)
			return err
		}
	} else {
		if err != nil {
			return err
		} else {
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
}

func main() {

	var all []string
	var allMunged []string
	var paths []string
	kapeliNames := make(map[string]string)
	kapeliTitles := make(map[string]string)
	var docsetNames []string
	var docsetDbs []*sql.DB

	var db *sql.DB
	var err error

	files, err := ioutil.ReadDir(".")
	check(err)

	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".zealdocset") {
			continue
		}

		db, err = sql.Open("sqlite3", name)
		check(err)
		f, err := ioutil.TempFile("", "zealdb")
		check(err)
		docsetName := strings.Replace(name, ".zealdocset", ".docset", 1)
		check(extractFile(db, docsetName+"/Contents/Resources/docSet.dsidx", f))
		docsetNames = append(docsetNames, docsetName)
		docsetDbs = append(docsetDbs, db)
		f.Close()

		db2, err := sql.Open("sqlite3", f.Name())
		if err == nil {
			importRows(db2, &all, &allMunged, &paths, docsetName)
			db2.Close()
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
				extractDocs(kapeliTitles[item.Id], resp.Body)
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
	err = http.ListenAndServe(":12340", nil)
	check(err)
}
