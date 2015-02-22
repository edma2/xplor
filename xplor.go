// 2010 - Mathieu Lonjaret

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"9fans.net/go/acme"
	"9fans.net/go/plan9"
	"9fans.net/go/plumb"
)

var (
	root       string
	w          *acme.Win
	PLAN9      = os.Getenv("PLAN9")
	showHidden bool
	dirflag   = []byte("+ ")
	nodirflag = []byte("  ")
	newLine = []byte("\n")
	// scratch space reused by every readLine call. ok since we're never concurrent.
	readLineBytes = make([]byte, 512)
)

const (
	INDENT    = "	"
	BINDENT = '	'
)

type dir struct {
	charaddr string
	depth    int
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: xplor [path] \n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()

	switch len(args) {
	case 0:
		root, _ = os.Getwd()
	case 1:
		root = path.Clean(args[0])
		if root[0] != '/' {
			cwd, _ := os.Getwd()
			root = path.Join(cwd, root)
		}
	default:
		usage()
	}

	err := initWindow()
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		return
	}

	for word := range events() {
		if len(word) >= 6 && word[0:6] == "DotDot" {
			doDotDot()
			continue
		}
		if len(word) >= 6 && word[0:6] == "Hidden" {
			toggleHidden()
			continue
		}
		if len(word) >= 3 && word[0:3] == "Win" {
			if PLAN9 != "" {
				cmd := path.Join(PLAN9, "bin/win")
				doExec(word[3:len(word)], cmd)
			}
			continue
		}
		// yes, this does not cover all possible cases. I'll do better if anyone needs it.
		if len(word) >= 5 && word[0:5] == "Xplor" {
			cmd, err := exec.LookPath("xplor")
			if err != nil {
				fmt.Fprintf(os.Stderr, err.Error())
				continue
			}
			doExec(word[5:len(word)], cmd)
			continue
		}
		if word[0] == 'X' {
			onExec(word[1:len(word)])
			continue
		}
		onLook(word)
	}
}

func initWindow() error {
	var err error = nil
	w, err = acme.New()
	if err != nil {
		return err
	}

	title := "xplor-" + root
	w.Name(title)
	w.Write("tag", []byte("DotDot Win Xplor Hidden"))
	return printDirContents(root, 0)
}


func printDirContents(dirpath string, depth int) (err error) {
	currentDir, err := os.OpenFile(dirpath, os.O_RDONLY, 0644)
	if err != nil {
		return err
	}
	// TODO(mpl): use Walk instead? meh, not a fan.
	names, err := currentDir.Readdirnames(-1)
	if err != nil {
		return err
	}
	currentDir.Close()
	sort.Strings(names)

	indents := make([]byte, depth)
	for k,_ := range indents {
		indents[k] = BINDENT
	}
	var buf bytes.Buffer
	var fi os.FileInfo
	for _, v := range names {
		if strings.HasPrefix(v, ".") && !showHidden {
			continue
		}
		fullpath := path.Join(dirpath, v)
		fi, err = os.Stat(fullpath)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}	
			// Skip (most likely) broken symlinks
			fmt.Fprintf(os.Stderr, "Skipping %v because %v\n", v, err)
			continue
		}
		if fi.IsDir() {
			buf.Write(dirflag)
		} else {
			buf.Write(nodirflag)
		}
		buf.Write(indents)
		buf.Write([]byte(v))
		buf.Write(newLine)
		if fi.IsDir() && len(names) == 1 {
			w.Write("data", buf.Bytes())
			buf.Reset()
			printDirContents(fullpath, depth+1)
		}
	}
	if buf.Len() > 0 {
		w.Write("data", buf.Bytes())
	}

	if depth == 0 {
		//lame trick for now to dodge the out of range issue, until my address-foo gets better
		w.Write("body", []byte("\n\n\n"))
	}

	return err
}

// the returned slice contents will be mutated by the next call to readLine
func readLine(addr string) ([]byte, error) {
	err := w.Addr("%s", addr)
	if err != nil {
		return nil, err
	}
	n, err := w.Read("xdata", readLineBytes)
	if err != nil {
		return nil, err
	}
	// remove dirflag, if any
	if n < 2 {
		return readLineBytes[0 : n-1], nil
	}
	return readLineBytes[2 : n-1], nil
}

func getDepth(line []byte) (depth int, trimedline string) {
	trimedline = strings.TrimLeft(string(line), INDENT)
	depth = (len(line) - len(trimedline)) / len(INDENT)
	return depth, trimedline
}

func isFolded(charaddr string) (bool, error) {
	var err error = nil
	var b []byte
	addr := "#" + charaddr + "+1-"
	b, err = readLine(addr)
	if err != nil {
		return true, err
	}
	depth, _ := getDepth(b)
	addr = "#" + charaddr + "+-"
	b, err = readLine(addr)
	if err != nil {
		return true, err
	}
	nextdepth, _ := getDepth(b)
	return (nextdepth <= depth), err
}

func getParents(charaddr string, depth int, prevline int) string {
	var addr string
	if depth == 0 {
		return ""
	}
	if prevline == 1 {
		addr = "#" + charaddr + "-+"
	} else {
		addr = "#" + charaddr + "-" + fmt.Sprint(prevline-1)
	}
	for {
		b, err := readLine(addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			os.Exit(1)
		}
		newdepth, line := getDepth(b)
		if newdepth < depth {
			fullpath := path.Join(getParents(charaddr, newdepth, prevline), line)
			return fullpath
		}
		prevline++
		addr = "#" + charaddr + "-" + fmt.Sprint(prevline-1)
	}
	return ""
}

// TODO(mpl): maybe break this one in a fold and unfold functions
func onLook(charaddr string) {
	// reconstruct full path and check if file or dir
	addr := "#" + charaddr + "+1-"
	b, err := readLine(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		return
	}
	depth, line := getDepth(b)
	fullpath := path.Join(root, getParents(charaddr, depth, 1), line)
	fi, err := os.Stat(fullpath)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		return
	}

	if !fi.IsDir() {
		// not a dir -> send that file to the plumber
		port, err := plumb.Open("send", plan9.OWRITE)
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			return
		}
		defer port.Close()
		msg := &plumb.Message{
			Src:  "xplor",
			Dst:  "",
			Dir:  "/",
			Type: "text",
			Data: []byte(fullpath),
		}
		if err := msg.Send(port); err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
		}
		return
	}

	folded, err := isFolded(charaddr)
	if err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		return
	}
	if folded {
		// print dir contents
		addr = "#" + charaddr + "+2-1-#0"
		err = w.Addr("%s", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error()+addr)
			return
		}
		err = printDirContents(fullpath, depth+1)
		if err != nil {
			fmt.Fprint(os.Stderr, err.Error())
		}
	} else {
		// fold, ie delete lines below dir until we hit a dir of the same depth
		addr = "#" + charaddr + "+-"
		nextdepth := depth + 1
		nextline := 1
		for nextdepth > depth {
			err = w.Addr("%s", addr)
			if err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				return
			}
			b, err = readLine(addr)
			if err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				return
			}
			nextdepth, _ = getDepth(b)
			nextline++
			addr = "#" + charaddr + "+" + fmt.Sprint(nextline-1)
		}
		nextline--
		addr = "#" + charaddr + "+-#0,#" + charaddr + "+" + fmt.Sprint(nextline-2)
		err = w.Addr("%s", addr)
		if err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			return
		}
		w.Write("data", []byte(""))
	}
}

func getFullPath(charaddr string) (fullpath string, err error) {
	// reconstruct full path and print it to Stdout
	addr := "#" + charaddr + "+1-"
	b, err := readLine(addr)
	if err != nil {
		return fullpath, err
	}
	depth, line := getDepth(b)
	fullpath = path.Join(root, getParents(charaddr, depth, 1), line)
	return fullpath, err
}

func doDotDot() {
	// blank the window
	err := w.Addr("0,$")
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
	w.Write("data", []byte(""))

	// restart from ..
	root = path.Clean(root + "/../")
	title := "xplor-" + root
	w.Name(title)
	err = printDirContents(root, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func doExec(loc string, cmd string) {
	var fullpath string
	if loc == "" {
		fullpath = root
	} else {
		var err error
		charaddr := strings.SplitAfterN(loc, ",#", 2)
		fullpath, err = getFullPath(charaddr[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			return
		}
		fi, err := os.Stat(fullpath)
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			return
		}
		if !fi.IsDir() {
			fullpath, _ = path.Split(fullpath)
		}
	}
	var args []string = make([]string, 1)
	args[0] = cmd
	fds := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	_, err := os.StartProcess(args[0], args, &os.ProcAttr{Env: os.Environ(), Dir: fullpath, Files: fds})
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		return
	}
	return
}

// on a B2 click event we print the fullpath of the file to Stdout.
// This can come in handy for paths with spaces in it, because the
// plumber will fail to open them.  Printing it to Stdout allows us to do
// whatever we want with it when that happens.
// Also usefull with a dir path: once printed to stdout, a B3 click on
// the path to open it the "classic" acme way.
func onExec(charaddr string) {
	fullpath, err := getFullPath(charaddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		return
	}
	fmt.Fprintf(os.Stderr, fullpath+"\n")
}

func toggleHidden() {
	showHidden = !showHidden
}

func events() <-chan string {
	c := make(chan string, 10)
	go func() {
		for e := range w.EventChan() {
			switch e.C2 {
			case 'x': // execute in tag
				switch string(e.Text) {
				case "Del":
					w.Ctl("delete")
				case "Hidden":
					c <- "Hidden"
				case "DotDot":
					c <- "DotDot"
				case "Win":
					tmp := ""
					if e.Flag != 0 {
						tmp = string(e.Loc)
					}
					c <- ("Win" + tmp)
				case "Xplor":
					tmp := ""
					if e.Flag != 0 {
						tmp = string(e.Loc)
					}
					c <- ("Xplor" + tmp)
				default:
					w.WriteEvent(e)
				}
			case 'X': // execute in body
				c <- ("X" + fmt.Sprint(e.OrigQ0))
			case 'l': // button 3 in tag
				// let the plumber deal with it
				w.WriteEvent(e)
			case 'L': // button 3 in body
				w.Ctl("clean")
				//ignore expansions
				if e.OrigQ0 != e.OrigQ1 {
					continue
				}
				c <- fmt.Sprint(e.OrigQ0)
			}
		}
		w.CloseFiles()
		close(c)
	}()
	return c
}
