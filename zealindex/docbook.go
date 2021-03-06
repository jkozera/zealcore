package zealindex

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"github.com/kyoh86/xdg"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path"
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

type DocbooksRepo struct {
	docBooks     *[]Docbook
	names        *[]string
	paths        *[]string
	symbolCounts *map[string]map[string]int
}

func NewDocbooksRepo() DocbooksRepo {
	var docBooks []Docbook
	var names []string
	var paths []string
	counts := make(map[string]map[string]int)
	return DocbooksRepo{&docBooks, &names, &paths, &counts}
}

func (d DocbooksRepo) Name() string {
	return "org.gnome"
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

func (d DocbooksRepo) GetAvailableForInstall() ([]RepoItem, error) {
	return make([]RepoItem, 0), nil
}

func (d DocbooksRepo) StartDocsetInstallById(id string, handlers ProgressHandlers, completed func()) string {
	return ""
}

func (d DocbooksRepo) GetInstalled() []RepoItem {
	var gnomeIconBytes, gnomeIcon2xBytes []byte
	var err error
	if _, err := os.Stat("/app/share/icons/gnome/16x16/places/gnome-foot.png"); os.IsNotExist(err) {
		gnomeIconBytes, err = ioutil.ReadFile("/usr/share/icons/gnome/16x16/places/gnome-foot.png")
		gnomeIcon2xBytes, err = ioutil.ReadFile("/usr/share/icons/gnome/32x32/places/gnome-foot.png")
	} else {
		gnomeIconBytes, err = ioutil.ReadFile("/app/share/icons/gnome/16x16/places/gnome-foot.png")
		gnomeIcon2xBytes, err = ioutil.ReadFile("/app/share/icons/gnome/32x32/places/gnome-foot.png")

	}
	var gnomeIcon, gnomeIcon2x string
	if err == nil {
		gnomeIcon = base64.StdEncoding.EncodeToString(gnomeIconBytes)
		gnomeIcon2x = base64.StdEncoding.EncodeToString(gnomeIcon2xBytes)
	} else {
		gnomeIcon = ""
		gnomeIcon2x = ""
	}
	var items []RepoItem
	for i, docbook := range *d.names {
		if docbook != "" {
			newItem := RepoItem{
				d.Name(),
				(*d.names)[i],
				(*d.names)[i],
				[]string{},
				"",
				gnomeIcon,
				gnomeIcon2x,
				(*d.docBooks)[i].Language,
				RepoItemExtra{""},
				(*d.names)[i],
				"",
				"",
				(*d.symbolCounts)[(*d.names)[i]],
			}
			items = append(items, newItem)
		}
	}
	return items
}

func (d DocbooksRepo) GetSymbols(index GlobalIndex, id, tp string) [][]string {
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

func (d DocbooksRepo) GetPage(qPath string, w io.Writer) error {
	for i, name := range *d.names {
		prefix := "/" + name + ".docbook/"
		if strings.HasPrefix(qPath, prefix) {
			f, err := os.Open((*d.paths)[i] + qPath[len(prefix):])
			if err == nil {
				_, err := io.Copy(w, f)
				return err
			} else {
				return err
			}
		}
		curPath := (*d.paths)[i]
		prefix = "/" + path.Base(curPath) + "/"
		if strings.HasPrefix(qPath, prefix) {
			// cross-docbook references -- ideally should be a redirect
			f, err := os.Open((*d.paths)[i] + qPath[len(prefix):])
			if err == nil {
				_, err := io.Copy(w, f)
				return err
			} else {
				return err
			}
		}
	}
	return errors.New("not found")
}

func (d DocbooksRepo) GetChapters(id, path string) [][]string {
	var res [][]string
	for i, name := range *d.names {
		if id == name {
			chaps := (*d.docBooks)[i].Chapters
			var chap DocbookSub
			parts := strings.Split(path, "/")
			for i := 0; i < len(parts); i += 1 {
				for _, chap2 := range chaps {
					unescaped, err := url.QueryUnescape(parts[i])
					check(err)
					if chap2.Name == unescaped {
						chap = chap2
						chaps = chap.Subs
						break
					}
				}
			}

			for _, subchap := range chaps {
				res = append(res, []string{subchap.Name, "docs/" + name + ".docbook/" + subchap.Link})
			}
		}
	}
	return res
}

type newDocBook struct {
	db   Docbook
	path string
	name string
}

func (dr DocbooksRepo) IndexDocById(idx GlobalIndex, id string) {
	re := regexp.MustCompile("(.*) \\(([^()]+) ([^()]+)\\)")

	for _, d := range *dr.docBooks {
		if d.Name == id {
			docsetNum := len(*(idx.DocsetNames))
			*(idx.DocsetNames) = append(*(idx.DocsetNames), []string{dr.Name(), d.Name, d.Name})
			(*dr.symbolCounts)[d.Name] = make(map[string]int)
			processKw := func(kw DocbookKw) {
				kwStr := kw.Name
				typeMatched := ""
				if re.MatchString(kwStr) {
					sm := re.FindStringSubmatch(kwStr)
					if sm[2] != "built-in" {
						kwStr = sm[2] + "." + sm[1]
						// replace values like `getv() (GObject.Object method)` with values like `GObject.Object.getv()`
						// (for consistency with Zeal/Dash)
					}
					typeMatched = sm[3]
				}

				*(idx.All) = append(*(idx.All), kwStr)
				*(idx.AllMunged) = append(*(idx.AllMunged), Munge(kwStr))
				*(idx.Docsets) = append(*(idx.Docsets), docsetNum)
				if typeMatched != "" {
					*(idx.Types) = append(*(idx.Types), MapType(typeMatched))
					(*dr.symbolCounts)[d.Name][MapType(typeMatched)] += 1
				} else {
					*(idx.Types) = append(*(idx.Types), MapType(kw.Type))
					(*dr.symbolCounts)[d.Name][MapType(kw.Type)] += 1
				}
				*(idx.Paths) = append(*(idx.Paths), d.Name+".docbook/"+kw.Link)
			}
			for _, c := range d.Functions {
				processKw(c)
			}
			for _, c := range d.Keywords {
				processKw(c)
			}
		}
	}
}

func (dr DocbooksRepo) ImportAll(idx GlobalIndex) {
	*(dr.docBooks) = make([]Docbook, 0)
	*(dr.names) = make([]string, 0)
	*(dr.paths) = make([]string, 0)
	*(dr.symbolCounts) = make(map[string]map[string]int)

	dirs := xdg.DataDirs()
	dirs = append(dirs, xdg.DataHome())

	input := make(chan newDocBook, 16)
	count := 0
	found := make(map[string]bool)

	for _, dataDir := range dirs {
		for _, dir := range []string{dataDir + "/devhelp/books/", dataDir + "/gtk-doc/html/"} {
			files, _ := ioutil.ReadDir(dir)
			for _, f := range files {
				files2, _ := ioutil.ReadDir(dir + f.Name())
				for _, f2 := range files2 {
					name := f2.Name()
					if strings.HasSuffix(name, ".devhelp.gz") {
						if !found[name] {
							count += 1
							go (func(path, path2 string) {
								f3, _ := os.Open(path)
								db := LoadDocBook(f3, true)
								input <- newDocBook{db, path2, db.Name}
							})(dir+f.Name()+"/"+name, dir+f.Name()+"/")
						}
						found[name] = true
					}
					if strings.HasSuffix(name, ".devhelp2") || strings.HasSuffix(name, ".devhelp") {
						if !found[name] {
							count += 1
							go (func(path, path2 string) {
								f3, _ := os.Open(path)
								db := LoadDocBook(f3, false)
								input <- newDocBook{db, path2, db.Name}
							})(dir+f.Name()+"/"+name, dir+f.Name()+"/")
						}
						found[name] = true
					}
				}
			}
		}
	}

	for count > 0 {
		count -= 1
		n := <-input

		(*dr.docBooks) = append((*dr.docBooks), n.db)
		(*dr.paths) = append((*dr.paths), n.path)
		(*dr.names) = append((*dr.names), n.name)
	}

	for _, d := range *dr.docBooks {
		dr.IndexDocById(idx, d.Name)
	}
}

func (dr DocbooksRepo) RemoveDocset(id string, idx GlobalIndex) bool {
	return false
}

func (d DocbooksRepo) StartDocsetInstallByIo(iostream io.ReadCloser, repoItem RepoItem, len int64, downloadProgressHandlers ProgressHandlers, completed func()) string {
	return ""
}