// Copyright 2011 Google Inc. All Rights Reserved.
// This file is available under the Apache license.

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	_ "net/http/pprof"
)

var (
	port  *string = flag.String("port", "3903", "HTTP port to listen on.")
	logs  *string = flag.String("logs", "", "List of files to monitor.")
	progs *string = flag.String("progs", "", "Directory containing programs")

	one_shot      *bool = flag.Bool("one_shot", false, "Run once on a log file, dump json, and exit.")
	dump_bytecode *bool = flag.Bool("dump_bytecode", false, "Dump bytecode of programs and exit.")
)

func OneShot(logfile string, lines chan string) error {
	l, err := os.Open(logfile)
	if err != nil {
		return fmt.Errorf("Failed to open log file %q: %s", logfile, err)
	}
	defer l.Close()

	r := bufio.NewReader(l)

	for {
		line, err := r.ReadString('\n')
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return fmt.Errorf("Failed to read from %q: %s", logfile, err)
		default:
			lines <- line
		}
	}
	return nil
}

func StartEmtail(lines chan string, pathnames []string) {
	t := NewTailer(lines)
	if t == nil {
		log.Fatal("Couldn't create a tailer.")
	}

	for _, pathname := range pathnames {
		t.Tail(pathname)
	}
}

type console struct {
	lines []string
}

func (c *console) Write(p []byte) (n int, err error) {
	s := ""
	for i, width := 0, 0; i < len(p); i += width {
		var r rune
		r, width = utf8.DecodeRune(p[i:])
		s += string(r)
	}
	c.lines = append(c.lines, s)
	return len(s), nil
}

func (c *console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`<a href="/csv">csv</a>, <a href="/json">json</a>`))
	w.Write([]byte("<pre>"))
	for _, l := range c.lines {
		w.Write([]byte(l))
	}
	w.Write([]byte("</pre>"))
}

func main() {
	flag.Parse()

	if *progs == "" {
		log.Fatalf("No emtail program directory specified; use -progs")
	}
	if *logs == "" {
		log.Fatalf("No logs specified to tail; use -logs")
	}

	fis, err := ioutil.ReadDir(*progs)
	if err != nil {
		log.Fatalf("Failed to list programs in %q: %s", *progs, err)
	}

	e := &engine{}
	errors := 0
	for _, fi := range fis {
		if fi.IsDir() {
			continue
		}
		if filepath.Ext(fi.Name()) != ".em" {
			continue
		}
		f, err := os.Open(fmt.Sprintf("%s/%s", *progs, fi.Name()))
		if err != nil {
			log.Printf("Failed to read program %q: %s\n", fi.Name(), err)
			continue
		}
		defer f.Close()
		v, errs := Compile(fi.Name(), f)
		if errs != nil {
			errors = 1
			for _, e := range errs {
				log.Print(e)
			}
			continue
		}
		if *dump_bytecode {
			fmt.Printf("Prog %s\n", fi.Name())
			fmt.Println("Metrics")
			for i, m := range metrics {
				if m.Program == v.name {
					fmt.Printf(" %8d %s\n", i, m)
				}
			}
			fmt.Println("REs")
			for i, re := range v.re {
				fmt.Printf(" %8d /%s/\n", i, re)
			}
			w := new(tabwriter.Writer)
			w.Init(os.Stdout, 0, 0, 1, ' ', tabwriter.AlignRight)

			fmt.Fprintln(w, "disasm\tl\top\topnd\t")
			for n, i := range v.prog {
				fmt.Fprintf(w, "\t%d\t%s\t%d\t\n", n, opNames[i.op], i.opnd)
			}
			w.Flush()
		}
		e.addVm(v)
		log.Printf("loaded %s", fi.Name())
	}

	if *compile_only || *dump_bytecode {
		os.Exit(errors)
	}

	var pathnames []string
	for _, pathname := range strings.Split(*logs, ",") {
		if pathname != "" {
			pathnames = append(pathnames, pathname)
		}
	}
	if len(pathnames) == 0 {
		log.Fatal("No logs to tail.")
	}

	lines := make(chan string)
	go e.run(lines)

	if *one_shot {
		for _, pathname := range pathnames {
			err := OneShot(pathname, lines)
			if err != nil {
				log.Fatalf("Failed one shot mode for %q: %s\n", pathname, err)
			}
		}
		b, err := json.MarshalIndent(metrics, "", "  ")
		if err != nil {
			log.Fatalf("Failed to marshal metrics into json: %s", err)
		}
		os.Stdout.Write(b)
		WriteMetrics()
	} else {
		StartEmtail(lines, pathnames)

		c := &console{}
		log.SetOutput(c)

		http.Handle("/", c)
		http.HandleFunc("/json", handleJson)
		http.HandleFunc("/csv", handleCsv)
		StartMetricPush()

		log.Fatal(http.ListenAndServe(":"+*port, nil))
	}
}
