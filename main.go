package main

import (
	"bytes"
	"container/list"
	"flag"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
	"unicode/utf8"

	//"io"
	//"encoding/xml"
	//"io/ioutil"

)

var (
	listenaddress string
	listenport    string
	basedir       string
	verbose       bool
	debug         bool
	dircache      int64
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.StringVar(&listenaddress, "address", "", "The address the dav server should listen to")
	flag.StringVar(&listenport, "port", "8080", "The port the dav server should listen to")
	flag.StringVar(&basedir, "base", "."+string(os.PathSeparator), "The start directory for the files to be served")
	flag.BoolVar(&debug, "debug", false, "Whether or not to display a lot of debug info")
	flag.BoolVar(&verbose, "verbose", false, "Whether or not to display verbose info")
	flag.Int64Var(&dircache, "dircache", 300, "Seconds to cache dir results")
	flag.Parse()
	basedir, _ = filepath.Abs(basedir)
	basedir = basedir + string(os.PathSeparator)
	//daemonize

	cachewrite = make(chan cacheEntry) //prepare the cache writing channel
	go cachemanager()
	go cacheprunesignal()

	http.HandleFunc("/", webdav)
	err := http.ListenAndServe(listenaddress+":"+listenport, nil)
	if err != nil {
		printlnstderr("Could not get listening address. Exiting")
		os.Exit(6)
	}
}

func printlnstdout(a ...interface{}) (n int, err error) {
	if verbose {
		return fmt.Fprintln(os.Stdout, a)
	}
	return 0, nil
}
func printlndebug(a ...interface{}) (n int, err error) {
	if debug {
		return fmt.Fprintln(os.Stdout, a)
	}
	return 0, nil
}
func printlnstderr(a ...interface{}) (n int, err error) {
	return fmt.Fprintln(os.Stderr, a)
}

//********************************************************************************************************************
//------------
type cacheEntry struct {
	timeadded   int64
	requestpath string
	data        []byte
}

var (
	cachemap   map[string][]byte
	cachewrite chan cacheEntry
)

func cachemanager() { //simple caching algorithm: with every addition we expire until we have only uptodate info
	var (
		cacheditems = list.New()
		newentry    cacheEntry
	)
	cachemap = make(map[string][]byte)
	for { //keep looping forever
		newentry = <-cachewrite
		printlndebug("New entry in channel")
		if newentry.timeadded > 0 { //adding a new cache entry if timeadded >0 (unixtime should be or else something artificial is going on)
			if _, found := cachemap[newentry.requestpath]; found { //if the data is already in the cachemap
				printlndebug("Already in map")
			} else { //add a new entry to the cachemap and put the entry to the back of the list
				printlndebug("adding", newentry.requestpath)
				cacheditems.PushBack(newentry)
				cachemap[newentry.requestpath] = newentry.data
			}
		} else { //we were just called to prune the cache
			printlndebug("Pruning")
			for done := false; !done; { //we go through the list
				checkentry := cacheditems.Front() //get the first element
				if checkentry == nil {
					printlndebug("List is empty")
					done = true //no sense in continuing
				} else {
					printlndebug("checking", checkentry.Value.(cacheEntry).requestpath, " ", time.Now().Unix()-checkentry.Value.(cacheEntry).timeadded)
					if (time.Now().Unix() - checkentry.Value.(cacheEntry).timeadded) > dircache { //if it has been expired
						printlndebug("expiring ", checkentry.Value.(cacheEntry).requestpath)
						delete(cachemap, checkentry.Value.(cacheEntry).requestpath)
						cacheditems.Remove(checkentry)
					} else { //we are done pruning the cache
						printlndebug("nothing left to expire")
						done = true
					}
				}
			}
		}
	}
} //cachemanager

func cacheprunesignal() {
	var newentry cacheEntry
	for {
		time.Sleep(time.Second)
		newentry = cacheEntry{}
		newentry.timeadded = 0
		cachewrite <- newentry
		printlndebug("sent alarm for pruning")
	}
}

//------------
func webdav(w http.ResponseWriter, r *http.Request) {
	// fmt.Println(r);
	switch r.Method {
	case "HEAD","GET":
		headget(w, r)
	case "PROPFIND":
		propfind(w, r)
	default:
		//printlnstderr("Issue with method: " + r.Method)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// sendHTTPStatus is an HTTP status code.
type sendHTTPStatus int

// senderr, when deferred, recovers panics of
// type sendHTTPStatus and writes the corresponding
// HTTP status to the response.  If the value is not
// of type sendHTTPStatus, it re-panics.
func senderr(w http.ResponseWriter) {
	err := recover()
	if stat, ok := err.(sendHTTPStatus); ok {
		w.WriteHeader(int(stat))
	} else if err != nil {
		if debug {
			panic(err)
		}
	}
}


// HEADGET Method
func headget(w http.ResponseWriter, r *http.Request) {
	defer senderr(w)
	printlndebug("GET")
	r.URL.Path, _ = url.QueryUnescape(r.URL.Path)
	printlndebug(r.URL.Path)
	requestpath := r.URL.Path[1:]
	filename, err := filepath.Abs(filepath.Clean(basedir + requestpath))
	printlndebug(filename)
	printlndebug("-----------------------------")
	if err != nil {
		if verbose {
			printlnstderr(err)
		}
		panic(sendHTTPStatus(http.StatusNotFound))
	}
	file, err := os.Open(filename)
	defer file.Close()
	if err != nil {
		panic(sendHTTPStatus(http.StatusNotFound))
	}
	http.ServeContent(w, r, "", time.Time{}, file)
}

//PROPFIND Method
func propfind(w http.ResponseWriter, r *http.Request) {
	printlndebug("PROPFIND")
	r.URL.Path, _ = url.QueryUnescape(r.URL.Path)
	printlndebug(r.URL.Path)
	defer senderr(w)
	depth := r.Header.Get("Depth")
	switch depth {
	case "0", "1": //basically we only barf if something else is requested
	case "", "infinity":
		panic(sendHTTPStatus(http.StatusForbidden))
	default:
		panic(sendHTTPStatus(http.StatusBadRequest))
	}

	requestpath := r.URL.Path[1:]
	if len(requestpath) > 0 && requestpath[len(requestpath)-1] == '/' {
		requestpath = requestpath[:len(requestpath)-1]
	}

	if _, found := cachemap[requestpath+"/"]; found {
		printlndebug("sending cached data for ", requestpath+"/")
		buf := bytes.NewBuffer(cachemap[requestpath+"/"]) //We create a copy of the data so we can 'drain' it into the writer
		buf.WriteTo(w)                                    //flush it to the client.
	} else {
		dirname, err := filepath.Abs(filepath.Clean(basedir + requestpath))
		if err != nil {
			printlnstderr(err)
			panic(sendHTTPStatus(http.StatusBadRequest))
		}
		d, err := os.Open(dirname)
		defer d.Close()
		if err != nil {
			if debug { //Because it is not really an error... the file is just not there
				printlnstderr(err)
			}
			panic(sendHTTPStatus(http.StatusNotFound))
		}
		fi, err := d.Readdir(-1)
		if err != nil {
			printlnstderr(err)
			panic(sendHTTPStatus(http.StatusNotFound))
		}

		rhostname := r.Host
		//Now we know that we can read the dir. We can build the list
		buf := new(bytes.Buffer)
		buf.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:ns0="urn:uuid:c2f41010-65b3-11d1-a29f-00aa00c14882/">
`)) //Write the preamble

		for _, fi := range fi {

			fname := fi.Name()
			fsize := fi.Size()
			ftime := fi.ModTime()
			fcreationtime := ftime.Format(time.RFC3339) //2012-12-15T08:31:00Z
			fmodtime := ftime.Format(time.RFC1123)      //Wed, 12 Dec 2012 15:30:23 GMT

			if !fi.IsDir() {
				printlndebug(fi.Name(), fi.Size(), "bytes ", fmodtime)

				buf.Write([]byte(`<D:response>
<D:href>http://` + rhostname + `/` + url.QueryEscape(requestpath) + `/` + url.QueryEscape(fname) + `</D:href>
<D:propstat>
<D:prop>
<D:creationdate ns0:dt="dateTime.tz">` + fcreationtime + `</D:creationdate><D:getcontentlanguage>en</D:getcontentlanguage><D:getcontentlength>` + strconv.FormatInt(int64(fsize), 10) + `</D:getcontentlength><D:getcontenttype>` + mime.TypeByExtension(filepath.Ext(fname)) + `</D:getcontenttype><D:getlastmodified ns0:dt="dateTime.rfc1123">` + fmodtime + `</D:getlastmodified></D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
</D:response>
`))
			} else {
				tempurl := ``
				if url.QueryEscape(requestpath) != "" {
					tempurl = `http://` + rhostname + `/` + url.QueryEscape(requestpath) + `/` + url.QueryEscape(fname) + `/`
				} else {
					tempurl = `http://` + rhostname + `/` + url.QueryEscape(fname) + `/`
				}
				buf.Write([]byte(`<D:response>
<D:href>` + tempurl + `</D:href>
<D:propstat>
<D:prop>
<D:creationdate ns0:dt="dateTime.tz">` + fcreationtime + `</D:creationdate><D:getcontentlanguage>en</D:getcontentlanguage><D:getcontentlength>` + strconv.FormatInt(int64(utf8.RuneCountInString(tempurl)), 10) + `</D:getcontentlength><D:getcontenttype>httpd/unix-directory</D:getcontenttype><D:getlastmodified ns0:dt="dateTime.rfc1123">` + fmodtime + `</D:getlastmodified><D:resourcetype><D:collection/></D:resourcetype></D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
</D:response>
`))
			}
		}

		buf.Write([]byte(`</D:multistatus>
	`)) //closing up

		w.WriteHeader(207)                                  //Go does not have http-status 207 'yet'
		w.Header().Set("content-length", string(buf.Len())) //Always set content-length
		//Basically we now know everything about the response so we *could* cache it
		cachebuf := buf.Bytes()
		newcacheentry := cacheEntry{}
		newcacheentry.timeadded = time.Now().Unix()
		newcacheentry.requestpath = requestpath + "/"
		newcacheentry.data = cachebuf
		cachewrite <- newcacheentry
		buf.WriteTo(w) //flush it to the client.
	}

}
