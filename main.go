package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("file path is a required argument")
		os.Exit(1)
	}
	filePath := os.Args[1]

	fh, err := os.OpenFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		fmt.Printf("can't open given file %#v\n", filePath)
		os.Exit(1)
	}

	err = fh.Close()
	if err != nil {
		fmt.Println("failed to close the file")
		os.Exit(1)
	}

	new().run(filePath)
}

func new() *container {
	c := container{
		app:     tview.NewApplication(),
		chBlame: make(chan blameData),
		log:     []string{},
	}
	return &c
}

type container struct {
	app *tview.Application

	fileView *tview.TextView
	infoView *tview.Table

	flex  *tview.Flex
	frame *tview.Frame

	lineCount   int
	currentLine int

	chBlame chan blameData
	log     []string
}

func (c *container) run(filePath string) {

	c.fileView = tview.NewTextView().
		SetDynamicColors(true).
		SetText("Loading...")

	c.fileView.
		SetTextColor(tcell.ColorBlack.TrueColor()).
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.infoView = tview.NewTable()

	c.infoView.
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.flex = tview.NewFlex().
		AddItem(c.fileView, 0, 6, true).
		AddItem(c.infoView, 35, 4, true)

		// c.flex.SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.app.SetRoot(c.flex, true)

	go func() { c.chBlame <- blame(filePath) }()
	go func() { c.receive() }()

	c.setKeys()
	err := c.app.Run()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func (c *container) receive() {
	for {
		select {
		case out := <-c.chBlame:
			c.lineCount = len(out.lines)
			c.fileView.SetText(strings.Join(out.lines, "\n"))
			for i := range out.lines {
				md := out.metadata[i]

				// lineNr := tview.NewTableCell(fmt.Sprintf("%v", i)).
				// 	SetTextColor(tcell.ColorBlack.TrueColor()).
				// 	SetBackgroundColor(tcell.ColorWhite.TrueColor())

				author := tview.NewTableCell(md.author).
					SetTextColor(tcell.ColorBlack.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				authorTime := tview.NewTableCell(md.authorTime.Format("2006-01-02")).
					SetTextColor(tcell.ColorBlack.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				sha := tview.NewTableCell(md.sha[:7]).
					SetTextColor(tcell.ColorBlack.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				// c.infoView.SetCell(i, 0, lineNr)
				c.infoView.SetCell(i, 1, author)
				c.infoView.SetCell(i, 2, authorTime)
				c.infoView.SetCell(i, 3, sha)
			}
			c.infoView.SetOffset(0, 0)
			c.app.Draw()
		case <-time.After(time.Second):
		}
	}
}

func (c *container) setKeys() {
	c.fileView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyDown:
			row, _ := c.infoView.GetOffset()
			c.infoView.SetOffset(row+1, 0)
			c.currentLine = min(c.lineCount, c.currentLine+1)
			c.log = append(c.log, fmt.Sprintf("row=%v currentLine=%v", row, c.currentLine))

		case tcell.KeyUp:
			row, _ := c.infoView.GetOffset()
			c.infoView.SetOffset(row-1, 0)
			c.currentLine = max(0, c.currentLine-1)
			c.log = append(c.log, fmt.Sprintf("row=%v currentLine=%v", row, c.currentLine))

		case tcell.KeyEscape, tcell.KeyCtrlC:
			c.app.Stop()
		case tcell.KeyRune:
			if event.Rune() == 'q' {
				c.app.Stop()
				for _, line := range c.log {
					fmt.Println(line)
				}
			}
		}
		return event
	})
}

func blame(filePath string) blameData {
	cmd := exec.Command("git", "blame", "--porcelain", "-M", "-C", filePath)
	cmd.Dir = filepath.Dir(filePath)
	buf, err := cmd.Output()
	if err != nil {
		fmt.Printf("failed to run git blame err=%v\n", err)
		os.Exit(1)
	}

	return parseBlameOutput(string(buf))
}

type commit struct {
	author     string
	authorTime time.Time
	sha        string
}

type blameData struct {
	lines    []string
	metadata map[int]*commit
}

// parses the git blame output
func parseBlameOutput(out string) blameData {
	res := blameData{
		metadata: map[int]*commit{},
		lines:    []string{},
	}

	commits := map[string]*commit{}
	currentSHA := ""

	for _, rawLine := range strings.Split(string(out), "\n") {
		var isFileLine = strings.HasPrefix(rawLine, "\t")
		if isFileLine {
			trimmed := strings.TrimPrefix(rawLine, "\t")
			res.lines = append(res.lines, trimmed)
			res.metadata[len(res.lines)-1] = commits[currentSHA]
			continue
		}

		trimmed := rawLine
		isStart := strings.Index(trimmed, " ") == 40
		if isStart {
			currentSHA = trimmed[:40]
		}

		meta, hasMeta := commits[currentSHA]
		if !hasMeta {
			meta = &commit{sha: currentSHA}
			commits[currentSHA] = meta
		}

		if strings.HasPrefix(rawLine, "author ") {
			meta.author = rawLine[len("author "):]
			if meta.author == "Not Committed Yet" {
				meta.author = "uncommitted"
			}
		}
		if strings.HasPrefix(rawLine, "author-time ") {
			trimmed := strings.TrimPrefix(rawLine, "author-time ")
			num, err := strconv.ParseInt(trimmed, 10, 64)
			if err != nil {
				fmt.Printf("failed to parse author time err=%v\n", err)
				os.Exit(1)
			}
			meta.authorTime = time.Unix(num, 0)
		}

	}

	return res
}
