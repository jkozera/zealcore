package zealindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"math/rand"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

func MapType(t string) string {
	typeAliases := map[string]string{
		"Package Attributes":          "Attribute",
		"Private Attributes":          "Attribute",
		"Protected Attributes":        "Attribute",
		"Public Attributes":           "Attribute",
		"Static Package Attributes":   "Attribute",
		"Static Private Attributes":   "Attribute",
		"Static Protected Attributes": "Attribute",
		"Static Public Attributes":    "Attribute",
		"XML Attributes":              "Attribute",
		// Binding
		"binding": "Binding",
		// Category
		"cat":    "Category",
		"Groups": "Category",
		"Pages":  "Category",
		// Class
		"cl":             "Class",
		"specialization": "Class",
		"tmplt":          "Class",
		// Constant
		"data":          "Constant",
		"econst":        "Constant",
		"enumdata":      "Constant",
		"enumelt":       "Constant",
		"clconst":       "Constant",
		"structdata":    "Constant",
		"writerid":      "Constant",
		"Notifications": "Constant",
		// Constructor
		"structctr":           "Constructor",
		"Public Constructors": "Constructor",
		// Enumeration
		"enum":         "Enumeration",
		"Enum":         "Enumeration",
		"Enumerations": "Enumeration",
		// Event
		"event":            "Event",
		"Public Events":    "Event",
		"Inherited Events": "Event",
		"Private Events":   "Event",
		// Field
		"Data Fields": "Field",
		// Function
		"dcop":                              "Function",
		"func":                              "Function",
		"ffunc":                             "Function",
		"signal":                            "Function",
		"slot":                              "Function",
		"grammar":                           "Function",
		"Function Prototypes":               "Function",
		"Functions/Subroutines":             "Function",
		"Members":                           "Function",
		"Package Functions":                 "Function",
		"Private Member Functions":          "Function",
		"Private Slots":                     "Function",
		"Protected Member Functions":        "Function",
		"Protected Slots":                   "Function",
		"Public Member Functions":           "Function",
		"Public Slots":                      "Function",
		"Signals":                           "Function",
		"Static Package Functions":          "Function",
		"Static Private Member Functions":   "Function",
		"Static Protected Member Functions": "Function",
		"Static Public Member Functions":    "Function",
		// Guide
		"doc": "Guide",
		// Namespace
		"ns": "Namespace",
		// Macro
		"macro": "Macro",
		// Method
		"clm":               "Method",
		"enumcm":            "Method",
		"enumctr":           "Method",
		"enumm":             "Method",
		"intfctr":           "Method",
		"intfcm":            "Method",
		"intfm":             "Method",
		"intfsub":           "Method",
		"instsub":           "Method",
		"instctr":           "Method",
		"instm":             "Method",
		"structcm":          "Method",
		"structm":           "Method",
		"structsub":         "Method",
		"Class Methods":     "Method",
		"Inherited Methods": "Method",
		"Instance Methods":  "Method",
		"Private Methods":   "Method",
		"Protected Methods": "Method",
		"Public Methods":    "Method",
		// Operator
		"intfopfunc": "Operator",
		"opfunc":     "Operator",
		// Property
		"enump":                "Property",
		"intfdata":             "Property",
		"intfp":                "Property",
		"instp":                "Property",
		"structp":              "Property",
		"Inherited Properties": "Property",
		"Private Properties":   "Property",
		"Protected Properties": "Property",
		"Public Properties":    "Property",
		// Protocol
		"intf": "Protocol",
		// Structure
		"struct":          "Structure",
		"Data Structures": "Structure",
		"Struct":          "Structure",
		// Type
		"tag":             "Type",
		"tdef":            "Type",
		"Data Types":      "Type",
		"Package Types":   "Type",
		"Private Types":   "Type",
		"Protected Types": "Type",
		"Public Types":    "Type",
		"Typedefs":        "Type",
		// Variable
		"var": "Variable",
		// docbooks:
		"attribute":        "Attribute",
		"class":            "Class",
		"method":           "Method",
		"Class Structures": "Structure",
		"Classes":          "Class",
		"constant":         "Constant",
		"member":           "Method",
		"property":         "Property",
		"typedef":          "Type",
		"Flags":            "Constant",
		"Enums":            "Enumeration",
		"function":         "Function",
		"variable":         "Variable",
		"":                 "Unknown",
	}
	res, exists := typeAliases[t]
	if exists {
		return res
	}
	return t
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

func ExtractDocs(repoId string, title string, f io.Reader, contentType string, size int64, downloadProgressHandlers ProgressHandlers) {
	var tr *tar.Reader
	progressReader := NewReaderWithProgress(f)
	gz, err := gzip.NewReader(progressReader)
	if err != nil {
		// try uncompressed?
		tr = tar.NewReader(progressReader)
	} else {
		check(err)
		tr = tar.NewReader(gz)
	}

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
			// Fixup name for docsets where title != root dir name (like "Lua 5.1" has a "Lua" root dir)
			name := strings.SplitAfterN(toWrite.Hdr.Name, "/", 2)[1]
			name = title + ".docset/" + name
			_, err = db.Exec(
				"INSERT INTO files(path, blob) values(?, ?)", name, toWrite.gz)
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
			v(repoId, title, progress, size)
		}
		downloadProgressHandlers.Lock.RUnlock()
	}

	wg.Wait()
	db.Close()
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
		if res != nil {
			res.Close()
		}
		if db != nil {
			db.Close()
		}
		if err != nil {
			return err
		}
		return errors.New("not found: " + path)
	}
}

func chooseRandomMirror(path string) string {
	cities := []string{"sanfrancisco", "newyork", "london", "frankfurt"};
	city := cities[rand.Int() % len(cities)]
	return fmt.Sprintf("https://%s.kapeli.com/%s", city, path);
}

func (d DashRepo) StartDocsetInstallById(id string, downloadProgressHandlers ProgressHandlers, completed func()) string {
	item, ok := (*d.kapeliItems)[id]
	if !ok {
		return ""
	}
	var resp *http.Response
	var err error
	if item.SourceId == "com.kapeli.contrib" {
		resp, err = http.Get(chooseRandomMirror("feeds/zzz/user_contributed/build/" + item.ContribRepoKey + "/" + item.Archive))
	} else {
		resp, err = http.Get("https://go.zealdocs.org/d/com.kapeli/" + item.Name + "/latest")
	}
	if err == nil {
		go (func() {
			ExtractDocs(item.SourceId, (*d.kapeliItems)[item.Id].Title, resp.Body, resp.Header["Content-Type"][0], resp.ContentLength, downloadProgressHandlers)
			_, err = GetCacheDB().Exec("INSERT INTO installed_docs(available_doc_id) VALUES (?)", id)
			check(err)
			completed()
			downloadProgressHandlers.Lock.RLock()
			// report 100% only after new index is created:
			for _, v := range downloadProgressHandlers.Map {
				v(item.SourceId, (*d.kapeliItems)[item.Id].Title, resp.ContentLength, resp.ContentLength)
			}
			downloadProgressHandlers.Lock.RUnlock()
		})()
		return item.Name
	}
	return ""
}


func (d DashRepo) StartDocsetInstallByIo(iostream io.ReadCloser, repoItem RepoItem, len int64, downloadProgressHandlers ProgressHandlers, completed func()) string {
	go (func() {
		(*d.kapeliItems)[repoItem.Id] = repoItem
		ExtractDocs("com.kapeli.local", repoItem.Title, iostream, "", len, downloadProgressHandlers)
		_, err := GetCacheDB().Exec("INSERT INTO installed_docs(available_doc_id) VALUES (?)", repoItem.Id)
		check(err)
		completed()
		downloadProgressHandlers.Lock.RLock()
		// report 100% only after new index is created:
		for _, v := range downloadProgressHandlers.Map {
			v("com.kapeli.local", (*d.kapeliItems)[repoItem.Id].Title, len, len)
		}
		downloadProgressHandlers.Lock.RUnlock()
	})()
	return repoItem.Title
}

func getRepo(repoId int) []RepoItem {
	dbRes, _ := GetCacheDB().Query("SELECT id, json FROM available_docs WHERE repo_id = ?", repoId)
	var items []RepoItem
	var item RepoItem
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

func (d DashRepo) GetInstalled() []RepoItem {
	var items []RepoItem
	var item RepoItem
	var id string
	rows, err := GetCacheDB().Query(
		"SELECT id, json FROM installed_docs i " +
		"INNER JOIN available_docs a " +
		"ON i.available_doc_id = a.id " +
		"WHERE a.repo_id = ?", d.repoId)
	check(err)
	var rawJson []byte
	for rows.Next() {
		err = rows.Scan(&id, &rawJson)
		json.Unmarshal(rawJson, &item)
		item.Id = id
		item.SymbolCounts = (*d.symbolCounts)[item.Title]
		items = append(items, item)
	}
	rows.Close()
	return items
}

func (d DashRepo) GetSymbols(index GlobalIndex, id, tp string) [][]string {
	q, err := cache.Query("SELECT json FROM available_docs WHERE id=?", id)
	if err == nil && q.Next() {
		var jsonDoc []byte
		q.Scan(&jsonDoc)
		var doc RepoItem
		json.Unmarshal(jsonDoc, &doc)
		id = doc.Title
	} else {
		return make([][]string, 0)
	}
	q.Close()
	var res [][]string
	for i, s := range *index.Types {
		name := (*index.DocsetNames)[(*index.Docsets)[i]]
		if name[0] != d.Name() {
			continue
		}

		if s == tp && (name[1] == id) {
			res = append(res, []string{(*index.All)[i], "docs/" + (*index.Paths)[i]})
		}
	}
	return res
}

func (d DashRepo) GetChapters(id, path string) [][]string {
	return make([][]string, 0)
}

func (d DashRepo) GetPage(path string, w io.Writer) error {
	for i, name := range *d.docsetNames {
		prefix := "/" + name + ".docset/"
		if strings.HasPrefix(path, prefix) {
			return ExtractFile((*d.docsetDbs)[i], path[1:], w)
		}
	}
	return errors.New("not found")
}

func (d DashRepo) updateRepo(updateDbFrom *[]RepoItem) {
	cacheDb := GetCacheDB()
	if updateDbFrom != nil {
		cacheDb.Exec("CREATE TABLE IF NOT EXISTS available_docs (id integer primary key autoincrement, repo_id, name, json)")
		tx, error := cacheDb.Begin()
		check(error)
		for i, item := range *updateDbFrom {
			itemJson, _ := json.Marshal(item)
			dbRes, err := tx.Query("SELECT id FROM available_docs WHERE name = ?", item.Name)
			var id string
			if err == nil && dbRes.Next() {
				dbRes.Scan(&id)
				tx.Exec("UPDATE available_docs SET json = ? WHERE id = ?", itemJson, id)
			} else {
				tx.Exec("INSERT INTO available_docs (repo_id, name, json) VALUES (?, ?, ?)", d.repoId, item.Name, itemJson)
				dbRes.Close()
				dbRes, err := tx.Query("SELECT id FROM available_docs WHERE name = ?", item.Name)
				if err == nil && dbRes.Next() {
					dbRes.Scan(&id)
				}
			}
			(*updateDbFrom)[i].Id = id
			dbRes.Close()
		}
		tx.Commit()
	}
	items := getRepo(d.repoId)
	for _, item := range items {
		(*d.kapeliItems)[item.Id] = item
	}
}

type DashRepo struct {
	kapeliItems  *map[string]RepoItem
	docsetNames  *[]string
	docsetDbs    *[]string
	docsetIcons  *map[string]DocsetIcons
	symbolCounts *map[string]map[string]int
	repoId       int // 1 - Dash, 2 - user contrib, 3 - local
}

func _NewDashRepo(repoId int) DashRepo {
	items := make(map[string]RepoItem)
	var names []string
	var dbs []string
	icons := make(map[string]DocsetIcons)
	counts := make(map[string]map[string]int)
	res := DashRepo{&items, &names, &dbs, &icons, &counts, repoId}
	res.GetAvailableForInstall()
	res.updateRepo(nil)

	rows, _ := GetCacheDB().Query("SELECT json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id = a.id")
	for rows.Next() {
		var data []byte
		var item RepoItem
		rows.Scan(&data)
		json.Unmarshal(data, &item)
		(*res.docsetIcons)[item.Name] = DocsetIcons{item.Icon, item.Icon2x}
	}
	rows.Close()

	return res
}

func NewDashRepo() DashRepo {
	return _NewDashRepo(1)
}

func NewDashContribRepo() DashRepo {
	return _NewDashRepo(2)
}

func NewDashLocalRepo() DashRepo {
	return _NewDashRepo(3)
}

func (d DashRepo) Name() string {
	if d.repoId == 1 {
		return "com.kapeli"
	} else if d.repoId == 2 {
		return "com.kapeli.contrib"
	} else {
		return "com.kapeli.local"
	}
}

func (d DashRepo) getAvailableForInstallDash() ([]RepoItem, error) {
	res, err := http.Get("http://api.zealdocs.org/v1/docsets")
	if err == nil {
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return nil, err
		} else {
			var items []RepoItem
			json.Unmarshal(body, &items)
			d.updateRepo(&items)
			return items, nil
		}
	} else {
		return nil, err
	}
}

type ContribItem struct {
	Name    string
	Icon    string
	Icon2x  string `json:"icon@2x"`
	Archive string
}

func (d DashRepo) getAvailableForInstallContrib() ([]RepoItem, error) {
	res, err := http.Get(chooseRandomMirror("feeds/zzz/user_contributed/build/index.json"))
	if err == nil {
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return nil, err
		} else {
			var items map[string]map[string]ContribItem
			json.Unmarshal(body, &items)
			var repoItems []RepoItem
			for key, item := range items["docsets"] {
				repoItems = append(repoItems, RepoItem{
					"com.kapeli.contrib",
					item.Name,
					item.Name,
					make([]string, 0),
					"",
					item.Icon,
					item.Icon2x,
					"",
					RepoItemExtra{""},
					"",
					item.Archive,
					key,
					make(map[string]int),
				})
			}
			d.updateRepo(&repoItems)
			return repoItems, nil
		}
	} else {
		return nil, err
	}
}

func (d DashRepo) GetAvailableForInstall() ([]RepoItem, error) {
	repo := getRepo(d.repoId)
	if len(repo) > 0 {
		return repo, nil
	}
	if d.repoId == 1 {
		return d.getAvailableForInstallDash()
	} else {
		return d.getAvailableForInstallContrib()
	}
}

func (d DashRepo) ImportAll(idx GlobalIndex) {
	files, err := ioutil.ReadDir(".")
	check(err)

	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".zealdocset") {
			continue
		}

		res, err := GetCacheDB().Query(
			"SELECT id, repo_id from available_docs WHERE name=? " +
				"AND id in (SELECT available_doc_id FROM installed_docs)",
			strings.Replace(name, ".zealdocset", "", -1),
		)

		var repoId int
		var id string
		check(err)
		if res.Next() {
			res.Scan(&id, &repoId)
			res.Close()
		} else {
			res.Close()
			continue
		}
		if repoId != d.repoId {
			continue
		}

		d.IndexDocById(idx, id)
		check(err)
	}
}

func (d DashRepo) IndexDocById(idx GlobalIndex, id string) {
	var err error

	f, err := ioutil.TempFile("", "zealdb")
	check(err)
	fShm, err := os.Create(f.Name() + "-shm")
	check(err)
	fWal, err := os.Create(f.Name() + "-wal")
	check(err)
	var docsetName, name string

	q, err := GetCacheDB().Query(
		"SELECT available_doc_id, json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id=a.id WHERE a.id = ?",
		id)
	check(err)
	var dsid []byte
	if q.Next() {
		var value []byte
		var item RepoItem
		q.Scan(&dsid, &value)
		json.Unmarshal(value, &item)
		name = item.Title + ".zealdocset"
		docsetName = item.Title + ".docset"
		q.Close()
	}

	check(ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx", f))
	ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-shm", fShm)
	ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-wal", fWal)
	shortName := strings.Replace(docsetName, ".docset", "", 1)
	(*d.docsetNames) = append(*d.docsetNames, shortName)
	(*idx.DocsetNames) = append(*idx.DocsetNames, []string{d.Name(), shortName, string(dsid)})
	(*d.docsetDbs) = append(*d.docsetDbs, name)
	f.Close()
	fShm.Close()
	fWal.Close()

	db, err := sql.Open("sqlite3", f.Name())

	if err == nil {
		ImportRows(db, idx.All, idx.AllMunged, idx.Paths, idx.Docsets, idx.Types, docsetName, len(*idx.DocsetNames)-1)

		curCounts := make(map[string]int)
		dbRes, _ := db.Query("SELECT type, COUNT(*) FROM searchIndexView GROUP BY type")
		for dbRes.Next() {
			var tp string
			var count int
			dbRes.Scan(&tp, &count)
			curCounts[MapType(tp)] += count
		}
		dbRes.Close()
		(*d.symbolCounts)[strings.Replace(docsetName, ".docset", "", 1)] = curCounts
		db.Close()
	} else {
		fmt.Println(err.Error())
	}

	os.Remove(f.Name())
	os.Remove(fShm.Name())
	os.Remove(fWal.Name())
}

func ImportRows(db *sql.DB, all, allMunged, paths *[]string, docsets *[]int, types *[]string, docsetName string, docsetNum int) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	check(err)

	var col string
	var tp string
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

	rows, err = db.Query("SELECT name, type, path, coalesce(fragment, '') FROM searchIndexView ORDER BY name ASC")
	check(err)

	re, err := regexp.Compile("<dash_entry_.*>")
	check(err)

	for rows.Next() {
		err = rows.Scan(&col, &tp, &path, &fragment)
		check(err)
		*all = append(*all, col)
		*allMunged = append(*allMunged, Munge(col))
		*docsets = append(*docsets, docsetNum)
		*types = append(*types, MapType(tp))
		if fragment != "" {
			fragment = "#" + fragment
		}
		path = re.ReplaceAllString(path, "");
		fragment = re.ReplaceAllString(fragment, "");
		*paths = append(*paths, docsetName+"/Contents/Resources/Documents/"+path+fragment)
	}
	rows.Close()
}

func removeFromIndex(title string, idx GlobalIndex) {
	var num int
	for i, name := range *idx.DocsetNames {
		if name[0] == "com.kapeli" && name[1] == title {
			num = i
		}
	}
	idx.Lock.Lock()

	oldAll := *idx.All
	oldAllMunged := *idx.AllMunged
	oldDocsets := *idx.Docsets
	oldTypes := *idx.Types
	oldPaths := *idx.Paths

	var all []string
	var allMunged []string
	var docsets []int
	var types []string
	var paths []string

	for i := 0; i < len(oldAll); i++ {
		if oldDocsets[i] != num {
			all = append(all, oldAll[i])
			allMunged = append(allMunged, oldAllMunged[i])
			docsets = append(docsets, oldDocsets[i])
			types = append(types, oldTypes[i])
			paths = append(paths, oldPaths[i])
		}
	}

	(*idx.All) = all
	(*idx.AllMunged) = allMunged
	(*idx.Paths) = paths
	(*idx.Docsets) = docsets
	(*idx.Types) = types
	idx.Lock.Unlock()
}

func (d DashRepo) RemoveDocset(id string, idx GlobalIndex) bool {
	q, err := GetCacheDB().Query(
		"SELECT json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id=a.id WHERE a.id = ?",
		id)
	var title string
	if err == nil && q.Next() {
		var value []byte
		var item RepoItem
		q.Scan(&value)
		json.Unmarshal(value, &item)
		title = item.Title
		if os.Remove(item.Title+".zealdocset") == nil {
			q.Close()
			_, err := GetCacheDB().Exec("DELETE FROM installed_docs WHERE available_doc_id = ?", id)
			if err != nil {
				fmt.Println(err.Error())
			}
			removeFromIndex(title, idx)
			return true
		}
	}
	if q != nil {
		q.Close()
	}
	return false
}
