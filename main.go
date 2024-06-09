package main

import (
	"crypto/sha1"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

	fileView    *tview.TextView
	infoView    *tview.Table
	logView     *tview.Table
	lineNumbers *tview.TextView
	menubar     *tview.TextView
	titlebar    *tview.TextView
	flexRoot    *tview.Flex
	flexMain    *tview.Flex

	filePath      string
	data          *blameData
	lineCount     int
	revListDesc   []string
	githubBaseURL string

	currentLine        int
	readingLineNumber  *string
	readingSearchQuery *string

	searchMode  bool
	searchQuery string
	matchCount  int

	chBlame chan *blameData
	log     []string
}

func (c *container) menuContent() string {
	if c.data == nil {
		return ""
	}

	if c.readingSearchQuery != nil {
		return fmt.Sprintf("search: %s", *c.readingSearchQuery)
	}

	type key struct {
		code  string
		descr string
	}

	if c.searchMode {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("query: [#e54304]%s[#000000] - ", c.searchQuery))
		keys := []key{
			{code: "n p", descr: "next/previous"},
			{code: "ESC", descr: "quit search"},
		}
		for _, k := range keys {
			str := fmt.Sprintf(
				"[#4CAF50]%s[#000000] [#000000]%s[#000000] ",
				k.code,
				k.descr,
			)
			b.WriteString(str)
		}
		return b.String()
	}

	var b strings.Builder
	keys := []key{
		{code: "↑↓", descr: "scroll"},
		{code: "<>", descr: "file rev"},
		{code: "b a", descr: "before/after line rev"},
		{code: "l", descr: "commit summary"},
		{code: "g", descr: "open gh pr"},
		{code: "/", descr: "search"},
		{code: "ESC", descr: "quit"},
	}
	for _, k := range keys {
		str := fmt.Sprintf(
			"[#4CAF50]%s[#000000] [#000000]%s[#000000] ",
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
		SetWrap(false).
		SetWordWrap(false).
		SetRegions(true).
		SetToggleHighlights(false).
		SetText("Loading...")

	c.fileView.
		SetTextColor(tcell.ColorBlack.TrueColor()).
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.infoView = tview.NewTable()

	c.infoView.
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.logView = tview.NewTable()
	c.logView.
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.menubar = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(false)
	c.menubar.
		SetTextColor(tcell.GetColor("#000000").TrueColor()).
		SetBackgroundColor(tcell.GetColor("#e8ecf0").TrueColor())
	c.menubar.SetText(c.menuContent())

	c.titlebar = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(false)
	c.titlebar.
		SetTextColor(tcell.GetColor("#000000").TrueColor()).
		SetBackgroundColor(tcell.GetColor("#e8ecf0").TrueColor())
	c.titlebar.SetText(filepath.Base(c.filePath))

	c.lineNumbers = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)

	c.lineNumbers.SetBackgroundColor(tcell.ColorWhite.TrueColor())
	c.lineNumbers.SetTextColor(tcell.GetColor("#9e9e9e").TrueColor())

	c.flexMain = tview.NewFlex().
		AddItem(c.lineNumbers, 5, 1, false).
		AddItem(c.fileView, 0, 5, true).
		AddItem(c.infoView, 0, 1, true).
		AddItem(c.logView, 0, 2, true)

	c.flexRoot = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(c.titlebar, 1, 1, false).
		AddItem(c.flexMain, 0, 1, true).
		AddItem(c.menubar, 1, 1, false)

	c.app.SetRoot(c.flexRoot, true)

	go func() {
		var err error
		c.revListDesc, err = c.revList(filePath)
		if err != nil {
			fmt.Println("failed to get rev list")
			os.Exit(1)
		}

		c.setGithubBaseURL(filePath)

		out, err := blame(filePath, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to get initial blame output err=%v\n", err)
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

			title := filepath.Base(c.filePath)
			youngestRev := c.data.sortedCommits[0]
			title += fmt.Sprintf(" @ [%s]%s[#000000]: %s", youngestRev.color, youngestRev.sha[:8], youngestRev.summary)
			c.titlebar.SetText(title)

			maxAuthorLen := 0
			for _, c := range out.sortedCommits {
				if len(c.author.name) > maxAuthorLen {
					maxAuthorLen = len(c.author.name)
				}
			}

			lineCount := fmt.Sprintf("%v", len(c.data.lines))

			c.infoView.Clear()
			for i := range out.lines {
				cm := out.lineCommits[i]
				paddedAuthor := cm.author.name + strings.Repeat(" ", maxAuthorLen-len(cm.author.name))

				author := tview.NewTableCell(paddedAuthor).
					SetTextColor(cm.author.color.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				authorTime := tview.NewTableCell(cm.authorTime.Format("2006-01-02")).
					SetTextColor(tcell.GetColor("#2E7D32").TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				sha := tview.NewTableCell(cm.sha[:8]).
					SetTextColor(cm.color.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				c.infoView.SetCell(i, 0, author)
				c.infoView.SetCell(i, 1, authorTime)
				c.infoView.SetCell(i, 2, sha)
			}

			c.flexMain.ResizeItem(c.infoView, maxAuthorLen+1+10+1+8, 3)
			c.flexMain.ResizeItem(c.lineNumbers, len(lineCount)+2, 1)
			c.flexMain.ResizeItem(c.logView, 43, 4)

			c.scrollTo(0)
			c.menubar.SetText(c.menuContent())
			c.app.Draw()
		}
	}
}

func (c *container) render() {
	c.renderMainContent()
	c.renderLogContent(nil)
}

func (c *container) renderLogContent(selectedCommit *commit) {
	c.logView.Clear()

	currentLineCommit := c.data.lineCommits[c.currentLine]
	if currentLineCommit == nil {
		c.log = append(c.log, "current line commit not found")
		return
	}

	lineCommit := c.data.lineCommits[c.currentLine]
	if lineCommit == nil {
		c.log = append(c.log, fmt.Sprintf("failed to find commit for line %v", c.currentLine+1))
		return
	}

	for i, cm := range c.data.sortedCommits {
		bgColor := tcell.ColorWhite.TrueColor()
		if selectedCommit != nil && cm.sha == selectedCommit.sha {
			bgColor = tcell.GetColor("#e8ecf0").TrueColor()
		}

		sha := tview.NewTableCell(fmt.Sprintf(" [%s]%s[#8e8e8e]%s", cm.color, cm.sha[:8], cm.sha[8:])).
			SetTextColor(cm.color.TrueColor()).
			SetBackgroundColor(bgColor)
		author := tview.NewTableCell(fmt.Sprintf(" [%s]%s", cm.author.color, cm.author.name)).
			SetTextColor(cm.color.TrueColor()).
			SetBackgroundColor(bgColor)
		authorTime := tview.NewTableCell(fmt.Sprintf(" [%s]%s", "#2e7d32", cm.authorTime)).
			SetTextColor(cm.color.TrueColor()).
			SetBackgroundColor(bgColor)
		summary := tview.NewTableCell(" " + cm.summary).
			SetTextColor(tcell.ColorBlack.TrueColor()).
			SetBackgroundColor(bgColor)

		empty := tview.NewTableCell("").
			SetTextColor(cm.color.TrueColor()).
			SetBackgroundColor(tcell.ColorWhite.TrueColor())

		c.logView.SetCell(5*i, 0, sha)
		c.logView.SetCell(5*i+1, 0, author)
		c.logView.SetCell(5*i+2, 0, authorTime)
		c.logView.SetCell(5*i+3, 0, summary)
		c.logView.SetCell(5*i+4, 0, empty)
	}
	c.logView.ScrollToBeginning()
}

func (c *container) renderMainContent() {
	_, _, width, _ := c.fileView.GetInnerRect()

	renderedLen := func(str string) int {
		res := 0
		for _, r := range str {
			if r == '\t' {
				res += tview.TabSize
			} else {
				res += 1
			}
		}
		return res
	}

	c.matchCount = 0
	lineCount := fmt.Sprintf("%v", len(c.data.lines))
	var fileBuilder strings.Builder
	var lineBuilder strings.Builder
	colCount := c.infoView.GetColumnCount()

	for i, line := range c.data.lines {
		if i > 0 {
			fileBuilder.WriteString("\n")
			lineBuilder.WriteString("\n")
		}
		escaped := tview.Escape(line)

		if c.searchMode {
			hasMatch := strings.Contains(escaped, c.searchQuery)
			for {
				if !strings.Contains(escaped, c.searchQuery) {
					break
				}
				repl := fmt.Sprintf(`["%d"][""]`, c.matchCount)
				escaped = strings.Replace(escaped, c.searchQuery, repl, 1)
				c.matchCount += 1
			}
			if hasMatch {
				escaped = strings.ReplaceAll(
					escaped,
					`"][""]`,
					fmt.Sprintf(`"]%s[""]`, c.searchQuery),
				)
			}
		}

		// file view
		if i == c.currentLine {
			padded := escaped
			delta := width - renderedLen(line)
			if delta > 0 {
				padded += strings.Repeat(" ", delta)
			}
			fileBuilder.WriteString("[#000000:#e8ecf0]" + padded + "[#000000:#ffffff]")
		} else {
			fileBuilder.WriteString(escaped)
		}

		// line view
		num := fmt.Sprintf("%v", i+1)
		num = strings.Repeat(" ", len(lineCount)-len(num)) + num
		num = " " + num + " "
		if i == c.currentLine {
			lineBuilder.WriteString("[#000000:#e8ecf0]" + num + "[#9e9e9e:#ffffff]")
		} else {
			lineBuilder.WriteString(num)
		}

		// info view
		for j := 0; j < colCount; j++ {
			cell := c.infoView.GetCell(i, j)
			if i == c.currentLine {
				cell.SetBackgroundColor(tcell.NewRGBColor(0xe8, 0xec, 0xf0))
			} else {
				cell.SetBackgroundColor(tcell.ColorWhite.TrueColor())
			}
		}
	}
	c.fileView.SetText(fileBuilder.String())
	c.lineNumbers.SetText(lineBuilder.String())

	if c.searchMode {
		regionIDs := make([]string, 0, c.matchCount)
		for i := 0; i < c.matchCount; i++ {
			regionIDs = append(regionIDs, fmt.Sprintf("%v", i))
		}
		c.fileView.Highlight(regionIDs...)
	}
}

var (
	scrollMargin = 3
)

func extractPullRequestReference(summary string) string {
	rxPRRef := regexp.MustCompile(`#([0-9]+)`)
	matches := rxPRRef.FindAllStringSubmatch(summary, -1)
	if len(matches) == 1 && len(matches[0]) == 2 {
		return matches[0][1]
	}
	return ""
}

func openURL(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Run()
	case "linux":
		exec.Command("xdg-open", url).Run()
	case "windows":
		exec.Command("cmd.exe", "/C", "start", url).Run()
	}
}

func (c *container) stop() {
	c.app.Stop()
	for _, line := range c.log {
		fmt.Println(line)
	}
}

func (c *container) scrollDown() {
	rowOffset, _ := c.fileView.GetScrollOffset()
	c.currentLine = min(c.lineCount-1, c.currentLine+1)

	_, _, _, height := c.fileView.GetInnerRect()
	if c.currentLine >= rowOffset+height-scrollMargin {
		rowOffset += 1
	}

	c.scrollTo(rowOffset)
}

func (c *container) scrollTo(offset int) {
	c.fileView.ScrollTo(offset, 0)
	c.infoView.SetOffset(offset, 0)
	c.lineNumbers.ScrollTo(offset, 0)

	c.render()
}

func (c *container) scrollToLogEntry() {
	lineCommit := c.data.lineCommits[c.currentLine]
	if lineCommit == nil {
		c.log = append(c.log, fmt.Sprintf("failed to find commit for line %v", c.currentLine+1))
		return
	}

	offset := -1
	for i, cm := range c.data.sortedCommits {
		if cm.sha != lineCommit.sha {
			continue
		}
		offset = i
		break
	}
	if offset == -1 {
		c.log = append(c.log, fmt.Sprintf("failed to find offset for commit %#v", lineCommit.sha))
		return
	}

	c.renderLogContent(lineCommit)
	c.logView.SetOffset(offset*5, 0)
}

func (c *container) scrollUp() {
	rowOffset, _ := c.fileView.GetScrollOffset()
	c.currentLine = max(0, c.currentLine-1)

	if c.currentLine < rowOffset+scrollMargin {
		rowOffset -= 1
	}
	c.scrollTo(rowOffset)
}

func (c *container) gotoLine(nr int) {
	c.currentLine = nr
	_, _, _, height := c.fileView.GetInnerRect()

	rowOffset := max(0, c.currentLine-(height/2))
	rowOffset = min(c.lineCount-1, rowOffset)

	c.scrollTo(rowOffset)
}

func (c *container) setKeys() {
	c.fileView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if c.searchMode {
			switch event.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC, tcell.KeyCR:
				c.searchMode = false
				c.readingSearchQuery = nil
				c.searchQuery = ""
				c.menubar.SetText(c.menuContent())
				c.render()

			case tcell.KeyRune:
				switch event.Rune() {
				case 'n':
				case 'p':
				default:
					return nil
				}

				key := event.Rune()
				highlights := c.fileView.GetHighlights()
				index, _ := strconv.Atoi(highlights[0])
				if key == 'n' {
					index = (index + 1) % c.matchCount
				} else if key == 'p' {
					index = (index - 1 + c.matchCount) % c.matchCount
				}
				c.fileView.Highlight(strconv.Itoa(index)).ScrollToHighlight()
			}
			return nil
		}

		if c.readingSearchQuery != nil {
			switch event.Key() {
			case tcell.KeyEnter:
				if c.readingSearchQuery == nil || len(*c.readingSearchQuery) == 0 {
					c.searchMode = false
					c.searchQuery = ""
					c.readingSearchQuery = nil
					c.menubar.SetText(c.menuContent())
					return nil
				}

				c.searchMode = true
				c.searchQuery = *c.readingSearchQuery
				c.readingSearchQuery = nil
				c.menubar.SetText(c.menuContent())
				c.render()

				return nil

			case tcell.KeyBackspace, tcell.KeyBackspace2:
				if (c.readingSearchQuery) == nil || len(*c.readingSearchQuery) == 0 {
					return nil
				}
				current := *c.readingSearchQuery
				next := current[:len(current)-1]
				c.readingSearchQuery = &next
				c.menubar.SetText(c.menuContent())
				return nil

			case tcell.KeyEscape, tcell.KeyCtrlC:
				c.searchMode = false
				c.searchQuery = ""
				c.readingSearchQuery = nil
				return nil

			case tcell.KeyRune:
				r := event.Rune()
				next := *c.readingSearchQuery + string(r)
				c.readingSearchQuery = &next
				c.menubar.SetText(c.menuContent())
				return nil
			}
		}

		switch event.Key() {
		case tcell.KeyEscape, tcell.KeyCtrlC:
			c.stop()
			return nil
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				c.stop()
				return nil
			}
		}

		if c.data == nil {
			c.readingLineNumber = nil
			return nil
		}

		switch event.Key() {
		case tcell.KeyDown:
			c.scrollDown()
		case tcell.KeyUp:
			c.scrollUp()

		case tcell.KeyRune:
			switch event.Rune() {
			case 'g':
				c.openPullRequest()
			case 'G':
				c.gotoReadLine()
			case 'l':
				c.scrollToLogEntry()
			case '<':
				c.previousFileRevision()
			case '>':
				c.nextFileRevision()
			case 'a':
				c.afterLineRevision()
			case 'b':
				c.beforeLineRevision()
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				c.readLineNumber(event.Rune())
				return nil
			case '/':
				empty := ""
				c.readingSearchQuery = &empty
				c.menubar.SetText(c.menuContent())
			}
		}

		c.readingLineNumber = nil
		return nil
	})
}

func (c *container) gotoReadLine() {
	if c.readingLineNumber == nil {
		return
	}
	i, err := strconv.Atoi(*c.readingLineNumber)
	if err != nil {
		c.log = append(c.log, fmt.Sprintf("failed to convert read line number err=%v", err))
		return
	}
	if i < 1 || i > len(c.data.lines) {
		c.log = append(c.log, fmt.Sprintf("read line number %#v is out of bunds", i))
		return
	}
	c.gotoLine(i - 1)
}

func (c *container) readLineNumber(rn rune) {
	strRune := string(rn)
	if c.readingLineNumber == nil {
		c.readingLineNumber = &strRune
		return
	}

	newNumber := *c.readingLineNumber + strRune
	c.readingLineNumber = &newNumber
}

func (c *container) openPullRequest() {
	cm := c.data.lineCommits[c.currentLine]
	c.showLogSummary()

	if c.githubBaseURL == "" {
		return
	}

	prRef := extractPullRequestReference(cm.summary)
	if prRef == "" {
		return
	}

	prURL, err := url.JoinPath(c.githubBaseURL, "pull", prRef)
	if err != nil {
		c.log = append(c.log, fmt.Sprintf("failed to construct pr url err=%v", err))
		return
	}

	openURL(prURL)
}

func (c *container) showLogSummary() {
	cm := c.data.lineCommits[c.currentLine]
	c.info(fmt.Sprintf("[#4CAF50]%s[#000000]: %s", cm.sha[:8], cm.summary))
}

func (c *container) previousFileRevision() {
	youngestSha := c.data.sortedCommits[0].sha
	nextRev := revBefore(c.revListDesc, youngestSha)
	if nextRev == "" {
		c.warn("reached oldest rev")
		return
	}
	c.newRevision(nextRev)
}

func (c *container) nextFileRevision() {
	youngestSha := c.data.sortedCommits[0].sha
	nextRev := revAfter(c.revListDesc, youngestSha)
	if nextRev == "" {
		c.warn("reached youngest rev")
		return
	}
	c.newRevision(nextRev)
}

func (c *container) afterLineRevision() {
	lineCommit := c.data.lineCommits[c.currentLine]
	nextRev := revAfter(c.revListDesc, lineCommit.sha)
	if nextRev == "" {
		c.warn("reached youngest rev")
		return
	}
	c.newRevision(nextRev)
}

func (c *container) beforeLineRevision() {
	lineCommit := c.data.lineCommits[c.currentLine]
	nextRev := revBefore(c.revListDesc, lineCommit.sha)
	if nextRev == "" {
		c.warn("reached oldest revision")
		return
	}
	c.newRevision(nextRev)
}

func revBefore(revList []string, rev string) string {
	for i, r := range revList {
		if r != rev {
			continue
		}

		if i == len(revList)-1 {
			return ""
		}
		return revList[i+1]
	}
	return ""
}

func revAfter(revList []string, rev string) string {
	for i, r := range revList {
		if r != rev {
			continue
		}

		if i == 0 {
			return ""
		}
		return revList[i-1]
	}
	return ""
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

func (c *container) info(msg string) {
	go func() {
		c.menubar.SetText(fmt.Sprintf("[#000000]%s[#000000]", msg))
		c.app.Draw()
		<-time.After(2 * time.Second)
		c.menubar.SetText(c.menuContent())
		c.app.Draw()
	}()
}

func (c *container) revList(filePath string) ([]string, error) {
	cd, err := cmdDir(filePath)
	if err != nil {
		return nil, err
	}

	args := []string{"rev-list", "HEAD", "--", filePath}
	cmd := exec.Command("git", args...)
	cmd.Dir = cd
	buf, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	revList := strings.Split(strings.TrimSpace(string(buf)), "\n")
	return revList, nil
}

func cmdDir(fp string) (string, error) {
	if filepath.IsAbs(fp) {
		return filepath.Dir(fp), nil
	}

	return os.Getwd()
}

func (c *container) setGithubBaseURL(filePath string) {
	cd, err := cmdDir(filePath)
	if err != nil {
		c.log = append(c.log, fmt.Sprintf("failed to get cmd dir err=%v", err))
		return
	}

	args := []string{"remote", "-v"}
	cmd := exec.Command("git", args...)
	cmd.Dir = cd
	buf, err := cmd.Output()
	if err != nil {
		c.log = append(c.log, fmt.Sprintf("failed to run git remote err=%v", err))
		return
	}

	rxSSH := regexp.MustCompile(`git@github.com:(.+)\.git`)
	rxHTTP := regexp.MustCompile(`https://github.com/(.+)\.git`)

	for _, line := range strings.Split(strings.TrimSpace(string(buf)), "\n") {
		matches := rxSSH.FindAllStringSubmatch(line, -1)
		if len(matches) == 1 && len(matches[0]) == 2 {
			repo := matches[0][1]
			c.githubBaseURL = fmt.Sprintf("https://github.com/%v", repo)
			return
		}

		matches = rxHTTP.FindAllStringSubmatch(line, -1)
		if len(matches) == 1 && len(matches[0]) == 2 {
			repo := matches[0][1]
			c.githubBaseURL = fmt.Sprintf("https://github.com/%v", repo)
			return
		}
	}
	c.log = append(c.log, fmt.Sprintf("didn't find github base url"))
}

func blame(filePath string, upTo string) (*blameData, error) {
	cd, err := cmdDir(filePath)
	if err != nil {
		return nil, err
	}

	if upTo != "" {
		cmd := exec.Command("git", "rev-parse", upTo)
		cmd.Dir = cd
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
	cmd.Dir = cd
	buf, err := cmd.Output()
	if err != nil {
		return nil, err
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
	summary    string
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
		if strings.HasPrefix(rawLine, "summary ") {
			trimmed := strings.TrimPrefix(rawLine, "summary ")
			meta.summary = trimmed
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

	commitShades := generateShades("#00345d", "#4FC3F7", len(res.sortedCommits))
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

func hashToColor(hash [20]byte) string {
	r := hash[0]
	g := hash[1]
	b := hash[2]

	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func authorColor(author string) string {
	hash := sha1.Sum([]byte(author))
	return hashToColor(hash)
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
