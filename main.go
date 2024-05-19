package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
		chBlame: make(chan *blameData),
		log:     []string{},
	}
	return &c
}

type container struct {
	app *tview.Application

	fileView *tview.TextView
	infoView *tview.Table
	menubar  *tview.TextView
	flex     *tview.Flex

	filePath    string
	data        *blameData
	lineCount   int
	revListDesc []string

	currentLine int

	chBlame chan *blameData
	log     []string
}

func (c *container) menuContent() string {
	var b strings.Builder
	if c.data != nil && len(c.data.sortedCommits) > 0 {
		youngestRev := c.data.sortedCommits[0]
		b.WriteString(fmt.Sprintf("[#68c25f]%s[#000000] ", youngestRev.sha[:7]))
	}

	type key struct {
		code  string
		descr string
	}
	keys := []key{
		{code: "↑↓", descr: "to scroll"},
		{code: "p", descr: "previous rev"},
		{code: "P", descr: "previous rev to line"},
		{code: "n", descr: "next rev"},
	}
	for _, k := range keys {
		str := fmt.Sprintf(
			"[#2e2e2e]%s[#000000] [#aeaeae]%s[#000000] ",
			k.code,
			k.descr,
		)
		b.WriteString(str)
	}

	return b.String()
}

func (c *container) run(filePath string) {
	c.filePath = filePath

	c.fileView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(false).
		SetText("Loading...")

	c.fileView.
		SetTextColor(tcell.ColorBlack.TrueColor()).
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.infoView = tview.NewTable()

	c.infoView.
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.menubar = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(false).
		SetWordWrap(false)

	c.menubar.
		SetBackgroundColor(tcell.ColorWhite.TrueColor()).
		SetBorder(true)

	c.menubar.SetText(c.menuContent())

	c.flex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(
			tview.NewFlex().
				AddItem(c.fileView, 0, 6, true).
				AddItem(c.infoView, 35, 4, true),
			0, 1, true,
		).
		AddItem(c.menubar, 3, 1, false)

	c.app.SetRoot(c.flex, true)

	go func() {
		var err error
		c.revListDesc, err = revList(filePath)
		if err != nil {
			fmt.Println("failed to get rev list")
			os.Exit(1)
		}

		out, err := blame(filePath, "")
		if err != nil {
			fmt.Println("failed to get initial blame output")
			os.Exit(1)
		}
		c.chBlame <- out
	}()
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
			c.data = out
			c.lineCount = len(out.lines)
			c.currentLine = 0

			for i := range out.lines {
				cm := out.lineCommits[i]

				author := tview.NewTableCell(cm.author.name).
					SetTextColor(cm.author.color.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				authorTime := tview.NewTableCell(cm.authorTime.Format("2006-01-02")).
					SetTextColor(tcell.GetColor("#F57F17").TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				sha := tview.NewTableCell(cm.sha[:7]).
					SetTextColor(cm.color.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				c.infoView.SetCell(i, 0, author)
				c.infoView.SetCell(i, 1, authorTime)
				c.infoView.SetCell(i, 2, sha)
			}

			c.infoView.SetOffset(0, 0)
			c.fileView.ScrollTo(0, 0)

			c.highlightCurrentLine()
			c.menubar.SetText(c.menuContent())
			c.app.Draw()
		}
	}
}

func (c *container) highlightCurrentLine() {
	_, _, width, _ := c.fileView.GetInnerRect()

	renderedLen := func(str string) int {
		res := 0
		for _, r := range str {
			if r == '\t' {
				res += 4
			} else {
				res += 1
			}
		}
		return res
	}

	var b strings.Builder
	for i, line := range c.data.lines {
		if i > 0 {
			b.WriteString("\n")
		}
		escaped := tview.Escape(line)
		if i == c.currentLine {
			padded := escaped
			delta := width - renderedLen(line)
			if delta > 0 {
				padded += strings.Repeat(" ", delta)
			}
			b.WriteString("[#000000:#e8ecf0]" + padded + "[#000000:#ffffff]")
		} else {
			b.WriteString(escaped)
		}
	}

	c.fileView.SetText(b.String())

	colCount := c.infoView.GetColumnCount()
	for row := max(0, c.currentLine-1); row <= min(c.currentLine+1, c.lineCount); row++ {
		for i := 0; i < colCount; i++ {
			cell := c.infoView.GetCell(row, i)
			if row == c.currentLine {
				cell.SetBackgroundColor(tcell.NewRGBColor(0xe8, 0xec, 0xf0))
			} else {
				cell.SetBackgroundColor(tcell.ColorWhite.TrueColor())
			}
		}
	}
}

var (
	scrollMargin = 3
)

func (c *container) setKeys() {
	c.fileView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		_, _, _, height := c.fileView.GetInnerRect()

		switch event.Key() {
		case tcell.KeyDown:
			rowOffset, _ := c.fileView.GetScrollOffset()
			c.currentLine = min(c.lineCount-1, c.currentLine+1)

			if c.currentLine >= rowOffset+height-scrollMargin {
				rowOffset += 1
			}
			c.fileView.ScrollTo(rowOffset, 0)
			c.infoView.SetOffset(rowOffset, 0)

			c.highlightCurrentLine()

		case tcell.KeyUp:
			rowOffset, _ := c.fileView.GetScrollOffset()
			c.currentLine = max(0, c.currentLine-1)

			if c.currentLine < rowOffset+scrollMargin {
				rowOffset -= 1
			}
			c.fileView.ScrollTo(rowOffset, 0)
			c.infoView.SetOffset(rowOffset, 0)

			c.highlightCurrentLine()

		case tcell.KeyEscape, tcell.KeyCtrlC:
			c.app.Stop()
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				c.app.Stop()
				for _, line := range c.log {
					fmt.Println(line)
				}

			case 'n': // next rev
				if c.data == nil {
					return nil
				}
				youngestSha := c.data.sortedCommits[0].sha
				for i, rev := range c.revListDesc {
					if rev != youngestSha {
						continue
					}
					if i == 0 {
						c.warn("reached youngest revision")
						return nil
					}
					nextSha := c.revListDesc[i-1]
					c.newRevision(nextSha)
					return nil
				}
				return nil

			case 'p': // previous rev
				if c.data == nil {
					return nil
				}

				youngestSha := c.data.sortedCommits[0].sha
				for i, rev := range c.revListDesc {
					if rev != youngestSha {
						continue
					}
					c.warn(fmt.Sprintf("i=%v len()=%v", i, len(c.revListDesc)))
					if i == len(c.revListDesc)-1 {
						c.warn("reached oldest revision")
						return nil
					}
					nextSha := c.revListDesc[i+1]
					c.newRevision(nextSha)
					return nil
				}
				return nil

			case 'P': // previous to line
				if c.data == nil {
					return nil
				}

				lineMeta := c.data.lineCommits[c.currentLine]
				beforeRev := fmt.Sprintf("%s^", lineMeta.sha)
				go func(rev string) {
					out, err := blame(c.filePath, rev)
					if err != nil {
						c.warn("reached oldest revision")
						return
					}
					c.chBlame <- out
				}(beforeRev)
			}
		}
		return nil
	})
}

func (c *container) newRevision(rev string) {
	go func() {
		out, err := blame(c.filePath, rev)
		if err != nil {
			c.warn(err.Error())
		}
		c.chBlame <- out
	}()
}

func (c *container) warn(msg string) {
	go func() {
		c.menubar.SetText(fmt.Sprintf("[#ff5544]%s[#000000]", msg))
		c.app.Draw()
		<-time.After(2 * time.Second)
		c.menubar.SetText(c.menuContent())
		c.app.Draw()
	}()
}

func revList(filePath string) ([]string, error) {
	args := []string{"rev-list", "HEAD", "--", filePath}
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Dir(filePath)
	buf, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	revList := strings.Split(strings.TrimSpace(string(buf)), "\n")
	return revList, nil

}

func blame(filePath string, upTo string) (*blameData, error) {
	if upTo != "" {
		cmd := exec.Command("git", "rev-parse", upTo)
		cmd.Dir = filepath.Dir(filePath)
		err := cmd.Run()
		if err != nil {
			return nil, err
		}
	}

	args := []string{"blame", "--porcelain", "-M", "-C"}
	if upTo != "" {
		args = append(args, upTo)
	}
	args = append(args, "--", filePath)
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Dir(filePath)
	buf, err := cmd.Output()
	if err != nil {
		fmt.Printf("failed to run git blame err=%v\n", err)
		os.Exit(1)
	}

	return parseBlameOutput(string(buf)), nil
}

type author struct {
	name  string
	color tcell.Color
}

type commit struct {
	author     *author
	authorTime time.Time
	sha        string
	color      tcell.Color
}

type blameData struct {
	lines         []string
	lineCommits   map[int]*commit
	sortedCommits []*commit
}

func parseBlameOutput(out string) *blameData {
	res := blameData{
		lineCommits:   map[int]*commit{},
		lines:         []string{},
		sortedCommits: []*commit{},
	}

	commits := map[string]*commit{}
	currentSHA := ""

	for _, rawLine := range strings.Split(string(out), "\n") {
		var isFileLine = strings.HasPrefix(rawLine, "\t")
		if isFileLine {
			trimmed := strings.TrimPrefix(rawLine, "\t")
			res.lines = append(res.lines, trimmed)
			res.lineCommits[len(res.lines)-1] = commits[currentSHA]
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
			meta.author = &author{}
			meta.author.name = rawLine[len("author "):]
			if meta.author.name == "Not Committed Yet" {
				meta.author.name = "uncommitted"
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

	authors := map[string]*author{}

	for _, c := range commits {
		if c.sha == "0000000000000000000000000000000000000000" {
			c.color = tcell.GetColor("#ee6002")
			continue
		}
		c.author.color = tcell.GetColor(authorColor(c.author.name))
		authors[c.author.name] = c.author
		res.sortedCommits = append(res.sortedCommits, c)
	}
	sort.SliceStable(res.sortedCommits, func(i, j int) bool {
		ci, cj := res.sortedCommits[i], res.sortedCommits[j]
		return ci.authorTime.After(cj.authorTime)
	})

	commitShades := generateShades("#01579B", "#4FC3F7", len(res.sortedCommits))
	for i, c := range res.sortedCommits {
		c.color = commitShades[i]
	}

	return &res
}

func hashString(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func intToHex(n int) string {
	return fmt.Sprintf("%02x", n)
}

func authorColor(a string) string {
	hash := hashString(a)

	r := int((hash >> 16) & 0xFF)
	g := int((hash >> 8) & 0xFF)
	b := int(hash & 0xFF)

	return rgbToHex(r, g, b)
}

func hexToRGB(hex string) (int, int, int) {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func rgbToHex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func interpolateColor(r1, g1, b1, r2, g2, b2 int, factor float64) (int, int, int) {
	r := int(float64(r1) + (float64(r2)-float64(r1))*factor)
	g := int(float64(g1) + (float64(g2)-float64(g1))*factor)
	b := int(float64(b1) + (float64(b2)-float64(b1))*factor)
	return r, g, b
}

func generateShades(dark, light string, count int) []tcell.Color {
	r1, g1, b1 := hexToRGB(dark)
	r2, g2, b2 := hexToRGB(light)

	shades := make([]tcell.Color, count)
	for i := 0; i < count; i++ {
		factor := float64(i) / float64(count-1)
		r, g, b := interpolateColor(r1, g1, b1, r2, g2, b2, factor)
		shades[i] = tcell.GetColor(rgbToHex(r, g, b))
	}

	return shades
}
