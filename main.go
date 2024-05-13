package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	filePath string
	logger   *slog.Logger

	ready    bool
	viewport viewport.Model
	content  content

	lineNumber, columnNumber int
}

type commitInfo struct {
	author     string
	authorTime time.Time
	sha        string
}
type content struct {
	ready    bool
	lines    []string
	metadata map[int]*commitInfo // zero-indexed
}

func formatStr(str string, goalLen int) string {
	strLen := 0
	res := ""
	for _, r := range str {
		rLen := 1
		if r == '\t' {
			rLen = 4
		}
		if strLen+rLen > goalLen {
			break
		}
		res += string(r)
		strLen += rLen
	}

	if strLen < goalLen {
		res += strings.Repeat(" ", goalLen-strLen)
	}
	return res

}

// parses the git blame output
func (m *model) setContent(out gitBlameOutput) {
	res := content{
		metadata: map[int]*commitInfo{},
		lines:    []string{},
		ready:    true,
	}

	commits := map[string]*commitInfo{}
	currentSHA := ""

	for _, rawLine := range strings.Split(string(out), "\n") {
		var isFileLine = strings.HasPrefix(rawLine, "\t")
		if isFileLine {
			trimmed := strings.TrimPrefix(rawLine, "\t")
			res.lines = append(res.lines, trimmed)
			res.metadata[len(res.lines)-1] = commits[currentSHA]
			continue
		}

		trimmed := strings.TrimPrefix(rawLine, "previous ")
		isStart := strings.Index(trimmed, " ") == 40
		if isStart {
			currentSHA = trimmed[:40]
		}

		meta, hasMeta := commits[currentSHA]
		if !hasMeta {
			meta = &commitInfo{sha: currentSHA}
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

	m.logger.With(
		"line_count", len(res.lines),
	).Info("parsed git blame output")
	m.content = res

	authorMaxWidth := 0
	for _, ci := range commits {
		authorMaxWidth = max(authorMaxWidth, len(ci.author))
	}

	authorWidth := min(10, authorMaxWidth)
	fileWidth := int(0.6 * float64(m.viewport.Width))
	infoWidth := m.viewport.Width - fileWidth - 3

	m.logger.With(
		"author_width", authorWidth,
		"file_width", fileWidth,
		"info_width", infoWidth,
	).Info("widths")

	combined := ""
	for i, line := range res.lines {
		if i > 0 {
			combined += "\n"
		}

		filePartLen := 0
		filePart := ""
		for _, r := range line {
			rLen := 1
			if r == '\t' {
				rLen = 4
			}
			if rLen+filePartLen > fileWidth {
				break
			}
			filePart += string(r)
			filePartLen += rLen
		}
		if filePartLen < fileWidth {
			filePart += strings.Repeat(" ", fileWidth-filePartLen)
		}

		md := res.metadata[i]
		infoLine := fmt.Sprintf(
			"%s %s",
			formatStr(md.author, authorWidth),
			md.authorTime.Format(time.DateOnly),
		)
		infoPart := infoLine[:min(infoWidth, len(infoLine))]
		if len(infoPart) < infoWidth {
			infoPart += strings.Repeat(" ", infoWidth-len(infoPart))
		}

		combined += filePart + " | " + infoPart
	}

	m.viewport.SetContent(combined)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case gitBlameOutput:
		m.setContent(msg)

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height)
			m.viewport.YPosition = 0
			m.ready = true
		}
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {

		case "q", "ctrl+c", "esc":
			return m, tea.Quit

		case "down":
			m.lineNumber = min(len(m.content.lines), m.lineNumber+1)

		case "up":
			m.lineNumber = max(0, m.lineNumber-1)

		default:
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if !m.ready || !m.content.ready {
		return "\n Initializing..."
	}

	return m.viewport.View()
}

func (m model) fileView() string {
	return strings.Join(m.content.lines, "\n")
}

func (m model) infoView() string {
	if !m.content.ready {
		return ""
	}

	infoBar := ""
	for i := range m.content.lines {
		md := m.content.metadata[i]
		if i > 0 {
			infoBar += "\n"
		}
		line := fmt.Sprintf(" | %s", md.author)
		infoBar += line
	}

	return infoBar
}

type gitBlameOutput string

func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "blame", "--porcelain", "-M", "-C", m.filePath)
		buf, err := cmd.Output()
		if err != nil {
			fmt.Printf("failed to run git blame err=%v\n", err)
			os.Exit(1)
		}

		m.logger.Info("loaded initial blame output")

		return gitBlameOutput(string(buf))
	}
}

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

	logFile, err := os.Create("tmp.log")
	if err != nil {
		fmt.Println("failed to create log file")
	}
	defer logFile.Close()

	initialModel := model{
		filePath: filePath,
		logger:   slog.New(slog.NewTextHandler(logFile, nil)),
	}
	p := tea.NewProgram(
		initialModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err = p.Run()
	if err != nil {
		fmt.Printf("failed to start err=%v", err)
		os.Exit(1)
	}

	fmt.Printf("log file: %s\n", logFile.Name())
}
