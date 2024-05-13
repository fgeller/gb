package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	filePath string

	ready    bool
	viewport viewport.Model
	content  blameContent

	lineNumber, columnNumber int
}

type lineInfo struct {
	author    string
	timestamp time.Time
	commit    string
}
type blameContent struct {
	ready    bool
	lines    []string
	metadata map[int]*lineInfo // zero-indexed
}

func (c blameContent) render() string {
	return strings.Join(c.lines, "\n")
}

func newBlameContent(out string) blameContent {
	res := blameContent{
		metadata: map[int]*lineInfo{},
		lines:    []string{},
		ready:    true,
	}
	for _, rawLine := range strings.Split(out, "\n") {
		var isMetaLine = !strings.HasPrefix(rawLine, "\t")
		if !isMetaLine {
			res.lines = append(res.lines, rawLine)
		}

		meta, hasMeta := res.metadata[len(res.lines)]

		if !hasMeta {
			if isMetaLine {
				meta = &lineInfo{}
			} else {
				// copy meta from prev line
				prevMeta := *res.metadata[len(res.lines)-1]
				meta = &prevMeta
			}
			res.metadata[len(res.lines)] = meta
		}

		if strings.HasPrefix(rawLine, "author") {
			meta.author = rawLine[len("author "):]
		}
	}

	return res
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case blameContent:
		m.content = msg

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height)
			m.viewport.YPosition = 0
			m.viewport.SetContent(m.content.render())
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

func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "blame", "--porcelain", "-M", "-C", m.filePath)
		buf := make([]byte, 0, 10*1024*1024)
		cmd.Stdout = bufio.NewWriter(bytes.NewBuffer(buf))
		err := cmd.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to run git blame err=%v", err)
		}

		return newBlameContent(string(buf))
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("file path is a required argument")
		os.Exit(1)
	}
	filePath := os.Args[0]

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

	p := tea.NewProgram(
		model{filePath: filePath},
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err = p.Run()
	if err != nil {
		fmt.Printf("failed to start err=%v", err)
		os.Exit(1)
	}
}
