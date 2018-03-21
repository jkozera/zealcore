package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/kyoh86/xdg"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io/ioutil"
	"mime"
	"os"
	"path/filepath"
	"sort"
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
				res.Path = "docs/" + res.Path
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

func createGlobalIndex(sources []zealindex.DocsRepo) zealindex.GlobalIndex {
	var all []string
	var allMunged []string
	var paths []string
	var docsets []int
	var types []string
	var docsetNames [][]string

	idx := zealindex.GlobalIndex{&all, &allMunged, &paths, &docsets, &types, &docsetNames, sync.RWMutex{}}

	for _, source := range sources {
		source.ImportAll(idx)
	}

	return idx
}

func main() {
	dataDir := xdg.DataHome() + "/zealcore"
	os.Mkdir(dataDir, 0700)
	os.Chdir(dataDir)

	var index zealindex.GlobalIndex

	repos := []zealindex.DocsRepo{
		zealindex.NewDashRepo(),
		zealindex.NewDocbooksRepo(),
	}
	reposByName := make(map[string]zealindex.DocsRepo)
	for _, source := range repos {
		reposByName[source.Name()] = source
	}

	index = createGlobalIndex(repos)

	router := gin.Default()
	router.Static("/html", "./html")
	router.GET("/index", func(c *gin.Context) {
		c.Data(200, "application/json", []byte("[{\"name\": \"api.zealdocs.org\", \"id\": 1}]"))
	})
	router.GET("/index/:id/repos", func(c *gin.Context) {
		indexId, err := strconv.Atoi(c.Param("id"))
		if err == nil && indexId == 1 {
			c.Data(200, "application/json", []byte("[{\"name\": \"com.kapeli\", \"id\": 1}]"))
			return
		}
		c.Data(404, "text/plain", []byte("not found"))
	})
	router.GET("/repo/:id/items", func(c *gin.Context) {
		repoId, err := strconv.Atoi(c.Param("id"))
		if err == nil && repoId == 1 {
			var b []byte
			items, err := repos[repoId-1].GetAvailableForInstall()
			if err == nil {
				sort.Slice(items, func(i, j int) bool {
					return strings.Compare(items[i].Name, items[j].Name) < 0
				})
				b, err = json.Marshal(items)
			}
			if err != nil {
				c.Data(500, "text/plain", []byte(err.Error()))
			} else {
				c.Data(200, "application/json", b)
			}
			return
		}
		c.Data(404, "text/plain", []byte("not found"))
	})

	downloadProgressHandlers := zealindex.NewProgressHandlers()

	router.POST("/item", func(c *gin.Context) {
		var item postItem
		body, err := ioutil.ReadAll(c.Request.Body)
		if err == nil {
			err = json.Unmarshal(body, &item)
		}
		if err == nil {
			for _, repo := range repos {
				installingName := repo.StartDocsetInstallById(item.Id, downloadProgressHandlers, func() {
					index.Lock.Lock()
					repo.IndexDocById(index, item.Id)
					index.Lock.Unlock()
				})
				if installingName != "" {
					c.Data(200, "text/plain", []byte(installingName))
					return
				}
			}
			c.Data(404, "text/plain", []byte("not found"))
		} else {
			c.Data(500, "text/plain", []byte(err.Error()))
		}
	})
	router.DELETE("/item/:id", func(c *gin.Context) {
		removed := false
		for _, repo := range repos {
			if repo.RemoveDocset(c.Param("id"), index) {
				removed = true
				break
			}
		}
		if removed {
			c.Data(200, "text/plain", []byte("OK"))
		} else {
			c.Data(404, "text/plain", []byte("Not found"))
		}
	})
	router.GET("/item", func(c *gin.Context) {
		var items []zealindex.RepoItem

		for _, repo := range repos {
			for _, docset := range repo.GetInstalled() {
				items = append(items, docset)
			}
		}

		sort.Slice(items, func(i, j int) bool {
			return strings.Compare(items[i].Name, items[j].Name) < 0
		})

		b, _ := json.Marshal(items)
		c.Data(200, "application/json", b)
	})

	router.GET("/item/:docset/:type/*path", func(c *gin.Context) {
		if c.Param("type") != "symbols" && c.Param("type") != "chapters" {
			c.Data(404, "text/plain", []byte("Not found: "+c.Param("type")+"."))
		}
		id := c.Param("docset")
		if c.Param("type") == "chapters" {
			for _, repo := range repos {
				res := repo.GetChapters(id, c.Param("path")[1:])
				if len(res) > 0 {
					b, _ := json.Marshal(res)
					c.Data(200, "application/json", b)
					return
				}
			}
		} else {
			for _, repo := range repos {
				res := repo.GetSymbols(index, id, c.Param("path")[1:])
				if len(res) > 0 {
					b, _ := json.Marshal(res)
					c.Data(200, "application/json", b)
					return
				}
			}
		}
		c.Data(200, "application/json", []byte("[]"))
	})

	router.GET("/search", func(c *gin.Context) {
		websocket.Handler(MakeSearchServer(&index)).ServeHTTP(c.Writer, c.Request)
	})
	lastDownloadHandler := 0

	router.GET("/download_progress", func(c *gin.Context) {
		websocket.Handler(func(ws *websocket.Conn) {
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
		}).ServeHTTP(c.Writer, c.Request)
	})

	pathHandler := func(c *gin.Context) {
		found := false
		var err error
		c.Writer.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(c.Request.URL.Path)))
		for _, repo := range repos {
			err := repo.GetPage(c.Param("path"), c.Writer)
			if err == nil {
				found = true
				break
			}
		}
		if !found {
			res := "not found: " + c.Request.URL.Path
			if err != nil {
				res += ";error = " + err.Error()
			}
			c.Data(404, "text/plain", []byte(res))
		}
	}

	router.GET("/docs/*path", pathHandler)
	router.GET("/usr/share/gtk-doc/html/*path", pathHandler)

	router.Run("127.0.0.1:12340")
}
