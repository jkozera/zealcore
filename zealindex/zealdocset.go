package zealindex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
			v(title, progress, size)
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
		*paths = append(*paths, docsetName+"/Contents/Resources/Documents/"+path+fragment)
	}
	rows.Close()
}
