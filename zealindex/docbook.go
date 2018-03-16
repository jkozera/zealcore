package zealindex

import (
	"compress/gzip"
	"encoding/xml"
	"github.com/kyoh86/xdg"
	"io"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
)

type DocbookSub struct {
	Link string       `xml:"link,attr"`
	Name string       `xml:"name,attr"`
	Subs []DocbookSub `xml:"sub"`
}

type DocbookKw struct {
	Link  string `xml:"link,attr"`
	Name  string `xml:"name,attr"`
	Type  string `xml:"type,attr"`
	Since string `xml:"since,attr"`
}

type Docbook struct {
	XMLName   xml.Name     `xml:"book"`
	Language  string       `xml:"language,attr"`
	Link      string       `xml:"link,attr"`
	Name      string       `xml:"name,attr"`
	Title     string       `xml:"title,attr"`
	Version   string       `xml:"version,attr"`
	Chapters  []DocbookSub `xml:"chapters>sub"`
	Functions []DocbookKw  `xml:"functions>function"`
	Keywords  []DocbookKw  `xml:"functions>keyword"`
}

func LoadDocBook(f *os.File, gz bool) Docbook {
	var r io.Reader

	if gz {
		r, _ = gzip.NewReader(f)
	} else {
		r = f
	}
	buf, _ := ioutil.ReadAll(r)
	res := Docbook{}
	_ = xml.Unmarshal(buf, &res)
	return res
}

func ImportAllDocbooks(all, allMunged, paths *[]string, docsets *[]int, types *[]string, docsetNames *[]string) ([]string, []Docbook) {
	var docBooks []Docbook
	var docsetDocbooks []string
	dirs := xdg.DataDirs()
	dirs = append(dirs, xdg.DataHome())

	for _, dataDir := range dirs {
		for _, dir := range []string{dataDir + "/devhelp/books/", dataDir + "/gtk-doc/html/"} {
			files, _ := ioutil.ReadDir(dir)
			for _, f := range files {
				files2, _ := ioutil.ReadDir(dir + f.Name())
				for _, f2 := range files2 {
					name := f2.Name()
					if strings.HasSuffix(name, ".devhelp.gz") {
						f3, _ := os.Open(dir + f.Name() + "/" + name)
						docBooks = append(docBooks, LoadDocBook(f3, true))
						docsetDocbooks = append(docsetDocbooks, dir+f.Name()+"/")
					}
					if strings.HasSuffix(name, ".devhelp2") || strings.HasSuffix(name, ".devhelp") {
						f3, _ := os.Open(dir + f.Name() + "/" + name)
						docBooks = append(docBooks, LoadDocBook(f3, false))
						docsetDocbooks = append(docsetDocbooks, dir+f.Name()+"/")
					}
				}
			}
		}
	}

	re := regexp.MustCompile("(.*) \\(([^()]+) [^()]+\\)")

	for _, d := range docBooks {
		docsetNum := len(*docsetNames)
		*docsetNames = append(*docsetNames, d.Name)
		processKw := func(kw DocbookKw) {
			kwStr := kw.Name
			if re.MatchString(kwStr) {
				sm := re.FindStringSubmatch(kwStr)
				if sm[2] != "built-in" {
					kwStr = sm[2] + "." + sm[1]
					// replace values like `getv() (GObject.Object method)` with values like `GObject.Object.getv()`
					// (for consistency with Zeal/Dash)
				}
			}

			*all = append(*all, kwStr)
			*allMunged = append(*allMunged, Munge(kwStr))
			*docsets = append(*docsets, docsetNum)
			*types = append(*types, kw.Type)
			*paths = append(*paths, d.Name+".docbook/"+kw.Link)
		}
		for _, c := range d.Functions {
			processKw(c)
		}
		for _, c := range d.Keywords {
			processKw(c)
		}
	}

	return docsetDocbooks, docBooks
}
