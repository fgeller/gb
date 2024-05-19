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

	fileView *tview.TextView
	infoView *tview.Table
	menubar  *tview.TextView
	titlebar *tview.TextView
	flex     *tview.Flex

	filePath      string
	data          *blameData
	lineCount     int
	revListDesc   []string
	githubBaseURL string

	currentLine int

	chBlame chan *blameData
	log     []string
}

func (c *container) menuContent() string {
	var b strings.Builder

	type key struct {
		code  string
		descr string
	}
	keys := []key{
		{code: "↑↓", descr: "scroll"},
		{code: "<>", descr: "file rev"},
		{code: "ba", descr: "before/after line rev"},
		{code: "l", descr: "commit summary"},
		{code: "g", descr: "open gh pr"},
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
		SetText("Loading...")

	c.fileView.
		SetTextColor(tcell.ColorBlack.TrueColor()).
		SetBackgroundColor(tcell.ColorWhite.TrueColor())

	c.infoView = tview.NewTable()

	c.infoView.
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

	c.flex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(c.titlebar, 1, 1, false).
		AddItem(
			tview.NewFlex().
				AddItem(c.fileView, 0, 6, true).
				AddItem(c.infoView, 35, 4, true),
			0, 1, true,
		).
		AddItem(c.menubar, 1, 1, false)

	c.app.SetRoot(c.flex, true)

	go func() {
		var err error
		c.revListDesc, err = revList(filePath)
		if err != nil {
			fmt.Println("failed to get rev list")
			os.Exit(1)
		}

		c.setGithubBaseURL(filePath)

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

			title := filepath.Base(c.filePath)
			youngestRev := c.data.sortedCommits[0]
			title += fmt.Sprintf(" @ [#4CAF50]%s[#000000] ", youngestRev.sha[:7])
			c.titlebar.SetText(title)

			maxAuthorLen := 0
			for _, c := range out.sortedCommits {
				if len(c.author.name) > maxAuthorLen {
					maxAuthorLen = len(c.author.name)
				}
			}

			for i := range out.lines {
				cm := out.lineCommits[i]
				paddedAuthor := cm.author.name + strings.Repeat(" ", maxAuthorLen-len(cm.author.name))

				author := tview.NewTableCell(paddedAuthor).
					SetTextColor(cm.author.color.TrueColor()).
					SetBackgroundColor(tcell.ColorWhite.TrueColor())

				authorTime := tview.NewTableCell(cm.authorTime.Format("2006-01-02")).
					SetTextColor(tcell.GetColor("#2E7D32").TrueColor()).
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
				res += tview.TabSize
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
	_, _, _, height := c.fileView.GetInnerRect()

	rowOffset, _ := c.fileView.GetScrollOffset()
	c.currentLine = min(c.lineCount-1, c.currentLine+1)

	if c.currentLine >= rowOffset+height-scrollMargin {
		rowOffset += 1
	}
	c.fileView.ScrollTo(rowOffset, 0)
	c.infoView.SetOffset(rowOffset, 0)

	c.highlightCurrentLine()
}
func (c *container) scrollUp() {
	rowOffset, _ := c.fileView.GetScrollOffset()
	c.currentLine = max(0, c.currentLine-1)

	if c.currentLine < rowOffset+scrollMargin {
		rowOffset -= 1
	}
	c.fileView.ScrollTo(rowOffset, 0)
	c.infoView.SetOffset(rowOffset, 0)

	c.highlightCurrentLine()
}

func (c *container) setKeys() {
	c.fileView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
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
			case 'l':
				c.showLogSummary()
			case '<':
				c.previousFileRevision()
			case '>':
				c.nextFileRevision()
			case 'a':
				c.afterLineRevision()
			case 'b':
				c.beforeLineRevision()
			}
		}

		return nil
	})
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
	c.info(fmt.Sprintf("[#4CAF50]%s[#000000]: %s", cm.sha[:7], cm.summary))
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

func (c *container) setGithubBaseURL(filePath string) {
	args := []string{"remote", "-v"}
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Dir(filePath)
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
