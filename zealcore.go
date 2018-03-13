package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"path/filepath"
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

func MakeSearchServer(index *zealindex.GlobalIndex) func(*websocket.Conn) {
	return func(ws *websocket.Conn) {
		lastQuery := 0
		searcher := zealindex.NewSearcher(index, &lastQuery)

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

type progressReport struct {
	Docset   string
	Received int64
	Total    int64
}

func createGlobalIndex() (idx zealindex.GlobalIndex, dbs []string) {
	var all []string
	var allMunged []string
	var paths []string
	var docsets []int
	var types []string
	var docsetNames []string
	var docsetDbs []string

	files, err := ioutil.ReadDir(".")
	check(err)

	i := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".zealdocset") {
			continue
		}

		f, err := ioutil.TempFile("", "zealdb")
		check(err)
		f_shm, err := os.Create(f.Name() + "-shm")
		check(err)
		f_wal, err := os.Create(f.Name() + "-wal")
		check(err)
		docsetName := strings.Replace(name, ".zealdocset", ".docset", 1)
		check(zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx", f))
		zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-shm", f_shm)
		zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-wal", f_wal)
		docsetNames = append(docsetNames, strings.Replace(docsetName, ".docset", "", 1))
		docsetDbs = append(docsetDbs, name)
		f.Close()

		db, err := sql.Open("sqlite3", f.Name())
		if err == nil {
			zealindex.ImportRows(db, &all, &allMunged, &paths, &docsets, &types, docsetName, i)
			db.Close()
		}
		os.Remove(f.Name())
		os.Remove(f_shm.Name())
		os.Remove(f_wal.Name())
		check(err)
		i += 1
	}

	fmt.Println(len(all))

	return zealindex.GlobalIndex{&all, &allMunged, &paths, &docsets, &types, docsetNames, nil, sync.RWMutex{}}, docsetDbs
}

func getRepo(cacheDB *sql.DB) []repoItem {
	dbRes, _ := cacheDB.Query("SELECT id, json FROM available_docs")
	var items []repoItem
	var item repoItem
	for dbRes.Next() {
		var id string
		var value []byte
		dbRes.Scan(&id, &value)
		json.Unmarshal(value, &item)
		item.Id = id
		items = append(items, item)
	}
	dbRes.Close()
	return items
}

func getRepoJson(cacheDb *sql.DB) []byte {
	items := getRepo(cacheDb)
	if len(items) > 0 {
		b, _ := json.Marshal(items)
		return b
	} else {
		return make([]byte, 0)
	}
}

func updateRepo(cacheDb *sql.DB, kapeliItems *map[string]repoItem, updateDbFrom []repoItem) {
	if updateDbFrom != nil {
		cacheDb.Exec("DROP TABLE available_docs")
		cacheDb.Exec("CREATE TABLE available_docs (id integer primary key autoincrement, repo_id, name, json)")
		for _, item := range updateDbFrom {
			itemJson, _ := json.Marshal(item)
			dbRes, err := cacheDb.Query("SELECT id FROM available_docs WHERE name = ?", item.Name)
			if err == nil && dbRes.Next() {
				var id string
				dbRes.Scan(&id)
				cacheDb.Exec("UPDATE available_docs SET json = ? WHERE id = ?", itemJson, id)
			} else {
				cacheDb.Exec("INSERT INTO available_docs (repo_id, name, json) VALUES (1, ?, ?)", item.Name, itemJson)
			}
			dbRes.Close()
		}
	}
	items := getRepo(cacheDb)
	for _, item := range items {
		(*kapeliItems)[item.Id] = item
	}
}

func main() {
	kapeliItems := make(map[string]repoItem)
	docsetIcons := make(map[string]zealindex.DocsetIcons)

	var index zealindex.GlobalIndex
	var docsetDbs []string

	var cache *sql.DB
	var err error

	cache, err = sql.Open("sqlite3", "zealcore_cache.sqlite3")
	check(err)
	cache.Exec("CREATE TABLE IF NOT EXISTS kv (key, value)")
	cache.Exec("CREATE TABLE IF NOT EXISTS installed_docs (available_doc_id)")
	cache.Exec("CREATE TABLE IF NOT EXISTS available_docs (id integer primary key autoincrement, repo_id, name, json)")

	updateRepo(cache, &kapeliItems, nil)

	rows, _ := cache.Query("SELECT json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id = a.id")
	for rows.Next() {
		var data []byte
		var item repoItem
		rows.Scan(&data)
		json.Unmarshal(data, &item)
		docsetIcons[item.Name] = zealindex.DocsetIcons{item.Icon, item.Icon2x}
	}
	rows.Close()

	index, docsetDbs = createGlobalIndex()
	index.DocsetIcons = docsetIcons

	http.Handle("/html/", http.FileServer(http.Dir(".")))
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
				repo := getRepoJson(cache)
				if len(repo) > 0 {
					w.Header().Set("Content-Type", "application/json")
					w.Write(repo)
					return
				}
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
						updateRepo(cache, &kapeliItems, items)
						w.Write(getRepoJson(cache))
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

	downloadProgressHandlers := zealindex.NewProgressHandlers()
	http.HandleFunc("/item", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var item postItem
			body, err := ioutil.ReadAll(r.Body)
			if err == nil {
				err = json.Unmarshal(body, &item)
			}
			var resp *http.Response
			if err == nil {
				resp, err = http.Get("https://go.zealdocs.org/d/com.kapeli/" + kapeliItems[item.Id].Name + "/latest")
			}
			if err == nil {
				go (func() {
					zealindex.ExtractDocs(kapeliItems[item.Id].Title, resp.Body, resp.Header["Content-Type"][0], resp.ContentLength, downloadProgressHandlers)
					cache.Exec("INSERT INTO installed_docs(available_doc_id) VALUES (?)", kapeliItems[item.Id].Id)
					newIndex, newDocsetDbs := createGlobalIndex()
					docsetDbs = newDocsetDbs
					docsetIcons[kapeliItems[item.Id].Name] = zealindex.DocsetIcons{kapeliItems[item.Id].Icon, kapeliItems[item.Id].Icon2x}
					newIndex.DocsetIcons = docsetIcons
					index.UpdateWith(&newIndex)
				})()
				w.Write([]byte(kapeliItems[item.Id].Name))
			}
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
			}
		} else {
			var items []repoItem
			var item repoItem
			rows, err := cache.Query("SELECT json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id = a.id")
			check(err)
			var rawJson []byte
			for rows.Next() {
				err = rows.Scan(&rawJson)
				json.Unmarshal(rawJson, &item)
				items = append(items, item)
			}
			rows.Close()
			b, _ := json.Marshal(items)
			w.Write(b)
		}
	})

	http.Handle("/search", websocket.Handler(MakeSearchServer(&index)))
	lastDownloadHandler := 0

	http.Handle("/download_progress", websocket.Handler(func(ws *websocket.Conn) {
		lastDownloadHandler += 1
		curDownloadHandler := lastDownloadHandler
		lastProgresses := make(map[string]int64)
		handler := func(docset string, received int64, total int64) {
			if lastProgresses[docset] != received {
				lastProgresses[docset] = received
				data, err := json.Marshal(progressReport{docset, received, total})
				if err == nil {
					ws.Write(data)
				}
				if received >= total {
					ws.Close()
				}
			}
		}
		downloadProgressHandlers.Lock.Lock()
		downloadProgressHandlers.Map[curDownloadHandler] = handler
		downloadProgressHandlers.Lock.Unlock()

		input := make([]byte, 1024)
		for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {
		}

		downloadProgressHandlers.Lock.Lock()
		delete(downloadProgressHandlers.Map, curDownloadHandler)
		downloadProgressHandlers.Lock.Unlock()
	}))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		found := false
		for i, name := range index.DocsetNames {
			if strings.HasPrefix(r.URL.Path, "/"+name+".docset/") {
				w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(r.URL.Path)))
				err := zealindex.ExtractFile(docsetDbs[i], r.URL.Path[1:], w)
				if err != nil {
					w.WriteHeader(404)
					w.Write([]byte(err.Error()))
				}
				found = true
			}
		}
		if !found {
			w.WriteHeader(404)
			w.Write([]byte("Not found."))
		}
	})

	err = http.ListenAndServe(":12340", nil)
	check(err)
}
